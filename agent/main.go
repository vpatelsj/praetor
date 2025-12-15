package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/apollo/praetor/agent/systemd"
	apiv1alpha1 "github.com/apollo/praetor/api/azure.com/v1alpha1"
	"github.com/apollo/praetor/gateway"
	"github.com/apollo/praetor/pkg/log"
	"github.com/apollo/praetor/pkg/version"
	"github.com/go-logr/logr"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	defaultHeartbeatSeconds = 15
	maxBackoff              = 30 * time.Second
	defaultStatePath        = "/var/lib/apollo/agent/state.json"
	runtimeSemantics        = "DaemonSet"
)

type managedItem struct {
	UnitName              string `json:"unitName"`
	LastActionAt          string `json:"lastActionAt,omitempty"`
	LastActionSpecHash    string `json:"lastActionSpecHash,omitempty"`
	LastActionDescription string `json:"lastActionDescription,omitempty"`
}

type agentState struct {
	Managed map[string]managedItem `json:"managed"`
}

type agent struct {
	deviceName        string
	gatewayURL        string
	deviceToken       string
	deviceTokenSecret string
	client            *http.Client
	logger            logr.Logger
	lastETag          string
	lastObserved      map[string]string
	lastDesired       *gateway.DesiredResponse
	managed           map[string]managedItem
	statePath         string
	heartbeat         time.Duration
	rnd               *rand.Rand
}

func main() {
	var deviceName string
	var gatewayURL string
	var deviceToken string
	var deviceTokenSecret string

	flag.StringVar(&deviceName, "device-name", getenv("APOLLO_DEVICE_NAME", ""), "Device identifier (env: APOLLO_DEVICE_NAME)")
	flag.StringVar(&gatewayURL, "gateway-url", getenv("APOLLO_GATEWAY_URL", ""), "Gateway base URL (env: APOLLO_GATEWAY_URL)")
	flag.StringVar(&deviceToken, "device-token", getenv("APOLLO_DEVICE_TOKEN", ""), "Shared device token (env: APOLLO_DEVICE_TOKEN)")
	flag.StringVar(&deviceTokenSecret, "device-token-secret", getenv("APOLLO_DEVICE_TOKEN_SECRET", ""), "HMAC secret for device-bound token (env: APOLLO_DEVICE_TOKEN_SECRET)")

	log.Setup()
	flag.Parse()

	logger := ctrllog.Log.WithName("agent")
	statePath := getenv("APOLLO_AGENT_STATE_FILE", defaultStatePath)

	if deviceName == "" {
		logger.Error(fmt.Errorf("missing device name"), "set --device-name or APOLLO_DEVICE_NAME")
		os.Exit(1)
	}
	if gatewayURL == "" {
		logger.Error(fmt.Errorf("missing gateway URL"), "set --gateway-url or APOLLO_GATEWAY_URL")
		os.Exit(1)
	}

	ag := &agent{
		deviceName:        deviceName,
		gatewayURL:        strings.TrimSuffix(gatewayURL, "/"),
		deviceToken:       strings.TrimSpace(deviceToken),
		deviceTokenSecret: strings.TrimSpace(deviceTokenSecret),
		client:            &http.Client{Timeout: 10 * time.Second},
		logger:            logger,
		lastObserved:      make(map[string]string),
		managed:           make(map[string]managedItem),
		statePath:         statePath,
		heartbeat:         time.Duration(defaultHeartbeatSeconds) * time.Second,
		rnd:               rand.New(rand.NewSource(time.Now().UnixNano())),
	}

	if err := ag.loadState(); err != nil {
		logger.Error(err, "load agent state", "path", statePath)
	}

	logger.Info("agent starting", "device", deviceName, "gateway", gatewayURL, "version", version.Version, "commit", version.Commit)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ag.run(ctx); err != nil {
		logger.Error(err, "agent stopped")
		os.Exit(1)
	}
}

func (a *agent) run(ctx context.Context) error {
	if err := a.pollDesired(ctx); err != nil {
		return err
	}

	desiredTicker := time.NewTicker(5 * time.Second)
	heartbeatTicker := time.NewTicker(a.heartbeat)
	defer desiredTicker.Stop()
	defer heartbeatTicker.Stop()

	backoff := 2 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-desiredTicker.C:
			if err := a.pollDesired(ctx); err != nil {
				a.logger.Error(err, "poll desired failed")
				a.sleepWithJitter(ctx, backoff)
				backoff = nextBackoff(backoff)
				continue
			}
			backoff = 2 * time.Second
		case <-heartbeatTicker.C:
			if err := a.sendReport(ctx, nil); err != nil {
				a.logger.Error(err, "heartbeat report failed")
				a.sleepWithJitter(ctx, backoff)
				backoff = nextBackoff(backoff)
				continue
			}
			backoff = 2 * time.Second
		}

		// Adjust heartbeat ticker if desired interval changed.
		heartbeatTicker.Reset(a.heartbeat)
	}
}

func (a *agent) pollDesired(ctx context.Context) error {
	desired, notModified, etag, err := a.fetchDesired(ctx)
	if err != nil {
		return err
	}
	if etag != "" {
		a.lastETag = etag
	}

	if desired != nil {
		a.lastDesired = desired
	} else if notModified {
		desired = a.lastDesired
	}

	if desired == nil {
		return nil
	}

	if desired.HeartbeatIntervalSeconds > 0 {
		a.heartbeat = time.Duration(desired.HeartbeatIntervalSeconds) * time.Second
	}

	obs, err := a.reconcile(ctx, desired)
	if err != nil {
		return err
	}
	if len(obs) == 0 {
		return nil
	}
	return a.sendReport(ctx, obs)
}

func (a *agent) fetchDesired(ctx context.Context) (*gateway.DesiredResponse, bool, string, error) {
	url := fmt.Sprintf("%s/v1/devices/%s/desired", a.gatewayURL, a.deviceName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, a.lastETag, err
	}
	if a.lastETag != "" {
		req.Header.Set("If-None-Match", a.lastETag)
	}
	if token := a.computeDeviceToken(); token != "" {
		req.Header.Set("X-Device-Token", token)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, false, a.lastETag, err
	}
	defer resp.Body.Close()

	etag := strings.TrimSpace(resp.Header.Get("ETag"))
	if etag == "" {
		etag = a.lastETag
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var desired gateway.DesiredResponse
		if err := json.NewDecoder(resp.Body).Decode(&desired); err != nil {
			return nil, false, etag, err
		}
		return &desired, false, etag, nil
	case http.StatusNotModified:
		return nil, true, etag, nil
	default:
		return nil, false, etag, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
}

func (a *agent) reconcile(ctx context.Context, desired *gateway.DesiredResponse) ([]gateway.Observation, error) {
	if desired == nil {
		return nil, nil
	}

	obs := make([]gateway.Observation, 0, len(desired.Items))
	managedNow := make(map[string]managedItem, len(desired.Items))

	for i := range desired.Items {
		item := desired.Items[i]
		key := itemKey(item.Namespace, item.Name)
		observation := gateway.Observation{
			Namespace:        item.Namespace,
			Name:             item.Name,
			ObservedSpecHash: item.SpecHash,
			PID:              0,
			StartTime:        "",
		}

		if item.Spec.RestartPolicy == apiv1alpha1.DeviceProcessRestartPolicyNever {
			msg := "DaemonSet semantics: agent will start service when stopped even if Restart=no; RestartPolicy affects systemd only."
			a.logger.Info("restartPolicy=Never does not disable runtime reconciliation", "namespace", item.Namespace, "name", item.Name, "unit", systemd.PathsFor(item.Namespace, item.Name).UnitName)
			observation.WarningMessage = stringPtr(msg)
		}

		if item.Spec.Execution.Backend != apiv1alpha1.DeviceProcessBackendSystemd {
			a.logger.Info("unsupported backend, skipping", "namespace", item.Namespace, "name", item.Name, "backend", item.Spec.Execution.Backend)
			observation.ProcessStarted = boolPtr(false)
			observation.Healthy = boolPtr(false)
			obs = append(obs, observation)
			continue
		}

		paths := systemd.PathsFor(item.Namespace, item.Name)
		unitContent, envContent, err := renderUnitFiles(item, paths.EnvPath)
		if err != nil {
			a.logger.Error(err, "render unit", "namespace", item.Namespace, "name", item.Name)
			observation.ProcessStarted = boolPtr(false)
			observation.Healthy = boolPtr(false)
			observation.ErrorMessage = stringPtr(err.Error())
			_ = stopAndDisableQuiet(ctx, a.logger, paths.UnitName)

			// Strict failure behavior: do not keep stale artifacts around on invalid spec.
			unitRemoved, _, removeErr := systemd.RemoveUnitWithDetails(ctx, paths.UnitName, paths.UnitPath, paths.EnvPath)
			if removeErr != nil {
				a.logger.Error(removeErr, "remove unit artifacts after render failure", "namespace", item.Namespace, "name", item.Name, "unit", paths.UnitName)
			} else if unitRemoved {
				if err := systemd.DaemonReload(ctx); err != nil {
					a.logger.Error(err, "daemon-reload after unit removal", "namespace", item.Namespace, "name", item.Name, "unit", paths.UnitName)
				}
			}
			obs = append(obs, observation)
			managedNow[key] = carryManaged(a.managed[key], paths.UnitName)
			continue
		}

		unitChanged, envChanged, err := systemd.EnsureUnitWithDetails(ctx, paths.UnitName, unitContent, paths.EnvPath, envContent)
		if err != nil {
			a.logger.Error(err, "ensure unit", "namespace", item.Namespace, "name", item.Name)
			observation.ProcessStarted = boolPtr(false)
			observation.Healthy = boolPtr(false)
			observation.ErrorMessage = stringPtr(err.Error())
			_ = stopAndDisableQuiet(ctx, a.logger, paths.UnitName)
			obs = append(obs, observation)
			managedNow[key] = carryManaged(a.managed[key], paths.UnitName)
			continue
		}

		if unitChanged {
			if err := systemd.DaemonReload(ctx); err != nil {
				a.logger.Error(err, "daemon-reload failed", "namespace", item.Namespace, "name", item.Name)
			}
		}

		prevManaged, hadPrev := a.managed[key]
		currentManaged := carryManaged(prevManaged, paths.UnitName)

		if !hadPrev {
			if err := systemd.EnableAndStart(ctx, paths.UnitName); err != nil {
				a.logger.Error(err, "enable/start failed", "namespace", item.Namespace, "name", item.Name, "unit", paths.UnitName)
				observation.ProcessStarted = boolPtr(false)
				observation.Healthy = boolPtr(false)
				observation.ErrorMessage = stringPtr(err.Error())
				_ = stopAndDisableQuiet(ctx, a.logger, paths.UnitName)
				obs = append(obs, observation)
				managedNow[key] = currentManaged
				continue
			}
			currentManaged = markAction(currentManaged, item.SpecHash, "enable-and-start")
		} else if unitChanged || envChanged {
			if err := systemd.Restart(ctx, paths.UnitName); err != nil {
				a.logger.Error(err, "restart failed", "namespace", item.Namespace, "name", item.Name, "unit", paths.UnitName)
				observation.ProcessStarted = boolPtr(false)
				observation.Healthy = boolPtr(false)
				observation.ErrorMessage = stringPtr(err.Error())
				_ = stopAndDisableQuiet(ctx, a.logger, paths.UnitName)
				obs = append(obs, observation)
				managedNow[key] = currentManaged
				continue
			}
			currentManaged = markAction(currentManaged, item.SpecHash, "restart")
		}

		pid, startTime, activeState, subState, err := systemd.Show(ctx, paths.UnitName)
		if err != nil {
			a.logger.Error(err, "show failed", "namespace", item.Namespace, "name", item.Name, "unit", paths.UnitName)
			observation.ProcessStarted = boolPtr(false)
			observation.Healthy = boolPtr(false)
			observation.ErrorMessage = stringPtr(err.Error())
		} else {
			// DaemonSet semantics: resource present => keep running.
			desiredRunning := true
			needStart := desiredRunning && (activeState != "active" || pid == 0)
			if needStart && shouldAttemptAction(currentManaged, item.SpecHash, 5*time.Second) {
				var actionErr error
				if activeState == "active" && pid == 0 {
					actionErr = systemd.Restart(ctx, paths.UnitName)
					currentManaged = markAction(currentManaged, item.SpecHash, "restart-drift")
				} else {
					actionErr = systemd.EnableAndStart(ctx, paths.UnitName)
					currentManaged = markAction(currentManaged, item.SpecHash, "enable-and-start-drift")
				}
				if actionErr != nil {
					a.logger.Error(actionErr, "drift correction failed", "namespace", item.Namespace, "name", item.Name, "unit", paths.UnitName)
					observation.ProcessStarted = boolPtr(false)
					observation.Healthy = boolPtr(false)
					observation.ErrorMessage = stringPtr(actionErr.Error())
					_ = stopAndDisableQuiet(ctx, a.logger, paths.UnitName)
				} else {
					pid, startTime, activeState, subState, err = systemd.Show(ctx, paths.UnitName)
					if err != nil {
						a.logger.Error(err, "show after drift correction failed", "namespace", item.Namespace, "name", item.Name, "unit", paths.UnitName)
						observation.ErrorMessage = stringPtr(err.Error())
					}
				}
			}

			processStarted := activeState == "active" && pid > 0
			observation.ProcessStarted = boolPtr(processStarted)
			observation.Healthy = boolPtr(processStarted)
			if !processStarted {
				// systemctl show may keep ExecMainStartTimestamp populated even after stop.
				observation.PID = 0
				observation.StartTime = ""
			} else {
				observation.PID = pid
				if !startTime.IsZero() {
					observation.StartTime = startTime.UTC().Format(time.RFC3339)
				} else {
					observation.StartTime = ""
				}
			}

			a.logger.V(1).Info("unit status", "namespace", item.Namespace, "name", item.Name, "unit", paths.UnitName, "active", activeState, "sub", subState, "pid", pid, "start", startTime)
		}

		obs = append(obs, observation)
		managedNow[key] = currentManaged
	}

	for key, managed := range a.managed {
		if _, ok := managedNow[key]; ok {
			continue
		}

		ns, name, err := splitKey(key)
		if err != nil {
			a.logger.Error(err, "parse managed key", "key", key)
			continue
		}

		paths := systemd.PathsFor(ns, name)
		if err := stopAndDisableQuiet(ctx, a.logger, managed.UnitName); err != nil {
			a.logger.Error(err, "stop/disable failed", "unit", managed.UnitName, "namespace", ns, "name", name)
		}

		unitRemoved, envRemoved, err := systemd.RemoveUnitWithDetails(ctx, managed.UnitName, paths.UnitPath, paths.EnvPath)
		if err != nil {
			a.logger.Error(err, "remove unit files failed", "unit", managed.UnitName, "namespace", ns, "name", name)
		}
		if unitRemoved {
			if err := systemd.DaemonReload(ctx); err != nil {
				a.logger.Error(err, "daemon-reload after removal failed", "unit", managed.UnitName, "namespace", ns, "name", name)
			}
		}
		if unitRemoved || envRemoved {
			a.logger.Info("removed unit artifacts", "namespace", ns, "name", name, "unit", managed.UnitName)
		}
	}

	a.managed = managedNow
	if err := a.persistState(); err != nil {
		a.logger.Error(err, "persist agent state", "path", a.statePath)
	}

	return obs, nil
}

func renderUnitFiles(item gateway.DesiredItem, envPath string) (string, string, error) {
	if len(item.Spec.Execution.Command) == 0 {
		return "", "", fmt.Errorf("missing command")
	}

	execStart, err := renderExecStart(item.Spec.Execution.Command, item.Spec.Execution.Args)
	if err != nil {
		return "", "", err
	}

	unit := &strings.Builder{}
	fmt.Fprintf(unit, "[Unit]\nDescription=Apollo DeviceProcess %s/%s\nAfter=network.target\n\n", item.Namespace, item.Name)
	fmt.Fprintf(unit, "[Service]\nType=simple\nExecStart=%s\n", execStart)
	if err := ValidateUnitField("workingDir", item.Spec.Execution.WorkingDir); err != nil {
		return "", "", err
	}
	if wd := strings.TrimSpace(item.Spec.Execution.WorkingDir); wd != "" {
		fmt.Fprintf(unit, "WorkingDirectory=%s\n", wd)
	}
	fmt.Fprintf(unit, "EnvironmentFile=-%s\n", envPath)
	systemdRestartMode := renderSystemdRestartMode(item.Spec.RestartPolicy)
	fmt.Fprintf(unit, "Restart=%s\n", systemdRestartMode)
	if err := ValidateUnitField("user", item.Spec.Execution.User); err != nil {
		return "", "", err
	}
	if user := strings.TrimSpace(item.Spec.Execution.User); user != "" {
		fmt.Fprintf(unit, "User=%s\n", user)
	}
	unit.WriteString("\n[Install]\nWantedBy=multi-user.target\n")

	envContent, err := RenderEnvFile(item.Spec.Execution.Env)
	if err != nil {
		return "", "", err
	}
	return unit.String(), envContent, nil
}

func renderExecStart(cmd []string, args []string) (string, error) {
	parts := append(append([]string{}, cmd...), args...)
	escaped := make([]string, 0, len(parts))
	for _, p := range parts {
		q, err := escapeSystemdArg(p)
		if err != nil {
			return "", err
		}
		escaped = append(escaped, q)
	}
	return strings.Join(escaped, " "), nil
}

func escapeSystemdArg(arg string) (string, error) {
	if arg == "" {
		return "\"\"", nil
	}
	if strings.ContainsAny(arg, "\n\r") {
		return "", fmt.Errorf("invalid ExecStart arg: contains newline")
	}
	if strings.ContainsAny(arg, " \"\\\t") {
		escaped := strings.ReplaceAll(arg, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		return "\"" + escaped + "\"", nil
	}
	return arg, nil
}

// ValidateUnitField rejects values that could inject additional systemd unit directives.
// We reject any ASCII control characters (< 0x20), including newlines.
func ValidateUnitField(label, value string) error {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 {
			return fmt.Errorf("invalid %s: contains control character", label)
		}
	}
	return nil
}

func renderSystemdRestartMode(policy apiv1alpha1.DeviceProcessRestartPolicy) string {
	switch policy {
	case apiv1alpha1.DeviceProcessRestartPolicyNever:
		return "no"
	case apiv1alpha1.DeviceProcessRestartPolicyOnFailure:
		return "on-failure"
	default:
		return "always"
	}
}

func carryManaged(prev managedItem, unitName string) managedItem {
	prev.UnitName = unitName
	return prev
}

func markAction(mi managedItem, specHash, desc string) managedItem {
	mi.LastActionAt = time.Now().UTC().Format(time.RFC3339)
	mi.LastActionSpecHash = specHash
	mi.LastActionDescription = desc
	return mi
}

func shouldAttemptAction(mi managedItem, specHash string, minInterval time.Duration) bool {
	if strings.TrimSpace(mi.LastActionAt) == "" {
		return true
	}
	if mi.LastActionSpecHash != "" && mi.LastActionSpecHash != specHash {
		return true
	}
	last, err := time.Parse(time.RFC3339, mi.LastActionAt)
	if err != nil {
		return true
	}
	return time.Since(last) >= minInterval
}

func stopAndDisableQuiet(ctx context.Context, logger logr.Logger, unitName string) error {
	err := systemd.StopAndDisable(ctx, unitName)
	if err == nil {
		return nil
	}
	if systemd.IsUnitNotFoundError(err) {
		logger.V(1).Info("unit not found during stop/disable", "unit", unitName)
		return nil
	}
	return err
}

func (a *agent) persistState() error {
	state := agentState{Managed: a.managed}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(a.statePath, data, 0o600)
}

func (a *agent) loadState() error {
	state, err := readStateFile(a.statePath)
	if err != nil {
		return err
	}
	a.managed = state.Managed
	return nil
}

func readStateFile(path string) (agentState, error) {
	state := agentState{Managed: make(map[string]managedItem)}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return state, err
	}
	if len(data) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Managed == nil {
		state.Managed = make(map[string]managedItem)
	}
	return state, nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-agent-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}

func splitKey(key string) (string, string, error) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid managed key: %s", key)
	}
	return parts[0], parts[1], nil
}

func (a *agent) sendReport(ctx context.Context, observations []gateway.Observation) error {
	url := fmt.Sprintf("%s/v1/devices/%s/report", a.gatewayURL, a.deviceName)
	reqBody := gateway.ReportRequest{
		AgentVersion: version.Version,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		Heartbeat:    true,
		Observations: observations,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := a.computeDeviceToken(); token != "" {
		req.Header.Set("X-Device-Token", token)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("report failed with status %d", resp.StatusCode)
	}

	for i := range observations {
		obs := observations[i]
		a.lastObserved[itemKey(obs.Namespace, obs.Name)] = obs.ObservedSpecHash
	}
	return nil
}

func boolPtr(v bool) *bool {
	return &v
}

func stringPtr(v string) *string {
	return &v
}

func itemKey(ns, name string) string {
	return ns + "/" + name
}

func (a *agent) computeDeviceToken() string {
	if a.deviceTokenSecret != "" {
		h := hmac.New(func() hash.Hash { return sha256.New() }, []byte(a.deviceTokenSecret))
		h.Write([]byte(a.deviceName))
		return hex.EncodeToString(h.Sum(nil))
	}
	return a.deviceToken
}

func (a *agent) sleepWithJitter(ctx context.Context, base time.Duration) {
	jitter := time.Duration(a.rnd.Int63n(int64(250 * time.Millisecond)))
	d := base + jitter
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

func nextBackoff(current time.Duration) time.Duration {
	n := current * 2
	if n > maxBackoff {
		return maxBackoff
	}
	return n
}

func getenv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
