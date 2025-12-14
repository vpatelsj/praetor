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
	"sort"
	"strconv"
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
)

type managedItem struct {
	UnitName string `json:"unitName"`
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
			obs = append(obs, observation)
			managedNow[key] = managedItem{UnitName: paths.UnitName}
			continue
		}

		unitChanged, envChanged, err := systemd.EnsureUnitWithDetails(ctx, paths.UnitName, unitContent, paths.EnvPath, envContent)
		if err != nil {
			a.logger.Error(err, "ensure unit", "namespace", item.Namespace, "name", item.Name)
			observation.ProcessStarted = boolPtr(false)
			observation.Healthy = boolPtr(false)
			obs = append(obs, observation)
			managedNow[key] = managedItem{UnitName: paths.UnitName}
			continue
		}

		if unitChanged {
			if err := systemd.DaemonReload(ctx); err != nil {
				a.logger.Error(err, "daemon-reload failed", "namespace", item.Namespace, "name", item.Name)
			}
		}

		if _, ok := a.managed[key]; !ok {
			if err := systemd.EnableAndStart(ctx, paths.UnitName); err != nil {
				a.logger.Error(err, "enable/start failed", "namespace", item.Namespace, "name", item.Name, "unit", paths.UnitName)
			}
		} else if unitChanged || envChanged {
			if err := systemd.Restart(ctx, paths.UnitName); err != nil {
				a.logger.Error(err, "restart failed", "namespace", item.Namespace, "name", item.Name, "unit", paths.UnitName)
			}
		}

		pid, startTime, activeState, subState, err := systemd.Show(ctx, paths.UnitName)
		if err != nil {
			a.logger.Error(err, "show failed", "namespace", item.Namespace, "name", item.Name, "unit", paths.UnitName)
			observation.ProcessStarted = boolPtr(false)
			observation.Healthy = boolPtr(false)
		} else {
			started := pid > 0 && (activeState == "active" || activeState == "activating" || activeState == "reloading")
			observation.ProcessStarted = boolPtr(started)
			observation.Healthy = boolPtr(started)
			if pid > 0 {
				observation.PID = &pid
			}
			if !startTime.IsZero() {
				ts := startTime.UTC().Format(time.RFC3339)
				observation.StartTime = &ts
			}

			a.logger.V(1).Info("unit status", "namespace", item.Namespace, "name", item.Name, "unit", paths.UnitName, "active", activeState, "sub", subState, "pid", pid, "start", startTime)
		}

		obs = append(obs, observation)
		managedNow[key] = managedItem{UnitName: paths.UnitName}
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
		if err := systemd.StopAndDisable(ctx, managed.UnitName); err != nil {
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

	unit := &strings.Builder{}
	fmt.Fprintf(unit, "[Unit]\nDescription=Apollo DeviceProcess %s/%s\nAfter=network.target\n\n", item.Namespace, item.Name)
	fmt.Fprintf(unit, "[Service]\nType=simple\nExecStart=%s\n", renderExecStart(item.Spec.Execution.Command, item.Spec.Execution.Args))
	if wd := strings.TrimSpace(item.Spec.Execution.WorkingDir); wd != "" {
		fmt.Fprintf(unit, "WorkingDirectory=%s\n", wd)
	}
	fmt.Fprintf(unit, "EnvironmentFile=-%s\n", envPath)
	fmt.Fprintf(unit, "Restart=%s\n", renderRestartPolicy(item.Spec.RestartPolicy))
	if user := strings.TrimSpace(item.Spec.Execution.User); user != "" {
		fmt.Fprintf(unit, "User=%s\n", user)
	}
	unit.WriteString("\n[Install]\nWantedBy=multi-user.target\n")

	envContent := renderEnvFile(item.Spec.Execution.Env)
	return unit.String(), envContent, nil
}

func renderExecStart(cmd []string, args []string) string {
	parts := append(append([]string{}, cmd...), args...)
	escaped := make([]string, 0, len(parts))
	for _, p := range parts {
		escaped = append(escaped, escapeSystemdArg(p))
	}
	return strings.Join(escaped, " ")
}

func escapeSystemdArg(arg string) string {
	if arg == "" {
		return "\"\""
	}
	if strings.ContainsAny(arg, " \"\\\t") {
		return strconv.Quote(arg)
	}
	return arg
}

func renderRestartPolicy(policy apiv1alpha1.DeviceProcessRestartPolicy) string {
	switch policy {
	case apiv1alpha1.DeviceProcessRestartPolicyNever:
		return "no"
	case apiv1alpha1.DeviceProcessRestartPolicyOnFailure:
		return "on-failure"
	default:
		return "always"
	}
}

func renderEnvFile(vars []apiv1alpha1.DeviceProcessEnvVar) string {
	if len(vars) == 0 {
		return ""
	}
	items := make([]apiv1alpha1.DeviceProcessEnvVar, len(vars))
	copy(items, vars)
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	b := &strings.Builder{}
	for _, v := range items {
		b.WriteString(v.Name)
		b.WriteByte('=')
		b.WriteString(v.Value)
		b.WriteByte('\n')
	}
	return b.String()
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
