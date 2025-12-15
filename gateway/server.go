package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	apiv1alpha1 "github.com/apollo/praetor/api/azure.com/v1alpha1"
	"github.com/apollo/praetor/pkg/conditions"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultHeartbeatSeconds = 15
	desiredETagHeader       = "ETag"
	deviceTokenHeader       = "X-Device-Token"
	maxReportBodyBytes      = 4 << 20
	connectedReason         = "AgentConnected"
	connectedMessage        = "device reported"
)

// DesiredItem describes a desired DeviceProcess instance for a device.
type DesiredItem struct {
	UID        string                        `json:"uid,omitempty"`
	Namespace  string                        `json:"namespace"`
	Name       string                        `json:"name"`
	Generation int64                         `json:"generation"`
	Spec       apiv1alpha1.DeviceProcessSpec `json:"spec"`
	SpecHash   string                        `json:"specHash"`
}

// DesiredResponse is returned to an agent polling for desired state.
type DesiredResponse struct {
	DeviceName               string        `json:"deviceName"`
	HeartbeatIntervalSeconds int           `json:"heartbeatIntervalSeconds"`
	Items                    []DesiredItem `json:"items"`
}

// ReportRequest is sent by the agent with heartbeat and observations.
type ReportRequest struct {
	AgentVersion string        `json:"agentVersion"`
	Timestamp    string        `json:"timestamp"`
	Heartbeat    bool          `json:"heartbeat"`
	Observations []Observation `json:"observations"`
}

// Observation reports the agent's view of a single DeviceProcess.
type Observation struct {
	Namespace        string  `json:"namespace"`
	Name             string  `json:"name"`
	ObservedSpecHash string  `json:"observedSpecHash"`
	ProcessStarted   *bool   `json:"processStarted,omitempty"`
	Healthy          *bool   `json:"healthy,omitempty"`
	PID              int64   `json:"pid"`
	StartTime        string  `json:"startTime"`
	ErrorMessage     *string `json:"errorMessage,omitempty"`
	WarningMessage   *string `json:"warningMessage,omitempty"`
}

const runtimeSemanticsDaemonSet = "DaemonSet"

// ReportResponse acknowledges a report.
type ReportResponse struct {
	Ack bool `json:"ack"`
}

// Gateway serves HTTP endpoints for devices and updates Kubernetes status.
// It implements manager.Runnable so it can be added to a controller-runtime Manager.
type Gateway struct {
	client   client.Client
	recorder recordEmitter
	log      logr.Logger

	addr            string
	authToken       string
	authSecret      string
	defaultInterval time.Duration
	staleMultiplier int

	mu             sync.RWMutex
	lastSeen       map[string]time.Time
	lastReport     map[string]time.Time
	heartbeatHints map[string]int

	server *http.Server
}

// recordEmitter captures the EventRecorder interface we need.
type recordEmitter interface {
	Event(object runtime.Object, eventtype, reason, message string)
	Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...any)
}

// New constructs a Gateway server instance.
func New(c client.Client, recorder recordEmitter, addr, token, tokenSecret string, defaultInterval time.Duration, staleMultiplier int) *Gateway {
	return &Gateway{
		client:          c,
		recorder:        recorder,
		log:             ctrl.Log.WithName("gateway"),
		addr:            addr,
		authToken:       strings.TrimSpace(token),
		authSecret:      strings.TrimSpace(tokenSecret),
		defaultInterval: defaultInterval,
		staleMultiplier: staleMultiplier,
		lastSeen:        make(map[string]time.Time),
		lastReport:      make(map[string]time.Time),
		heartbeatHints:  make(map[string]int),
	}
}

// Start runs the HTTP server and staleness loop until the context is cancelled.
func (g *Gateway) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	mux.HandleFunc("/v1/devices/", g.handleDevice)

	g.server = &http.Server{Addr: g.addr, Handler: mux}

	go g.stalenessLoop(ctx)

	errCh := make(chan error, 1)
	go func() {
		err := g.server.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = g.server.Shutdown(shutdownCtx)

	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	default:
	}

	return nil
}

func (g *Gateway) handleDevice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		http.NotFound(w, r)
		return
	}

	deviceName := parts[2]
	action := parts[3]

	if !g.authorize(r, deviceName) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	switch action {
	case "desired":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		g.handleDesired(ctx, w, r, deviceName)
	case "report":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		g.handleReport(ctx, w, r, deviceName)
	case "connect":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		g.handleConnect(ctx, w, r, deviceName)
	default:
		http.NotFound(w, r)
	}
}

func (g *Gateway) authorize(r *http.Request, _ string) bool {
	header := strings.TrimSpace(r.Header.Get(deviceTokenHeader))

	// Preferred: per-device HMAC token when secret is configured.
	if g.authSecret != "" {
		device := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/"), "v1/devices/")
		if idx := strings.Index(device, "/"); idx >= 0 {
			device = device[:idx]
		}
		if device != "" {
			expected := computeDeviceToken(g.authSecret, device)
			if hmac.Equal([]byte(header), []byte(expected)) {
				return true
			}
		}
	}

	// Fallback: shared token for dev/compat.
	if g.authToken == "" {
		return true
	}
	return header == g.authToken
}

func (g *Gateway) handleDesired(ctx context.Context, w http.ResponseWriter, r *http.Request, deviceName string) {
	desired, etag, err := g.computeDesired(ctx, deviceName)
	if err != nil {
		g.respondErr(ctx, w, http.StatusInternalServerError, "failed to compute desired state")
		g.log.Error(err, "compute desired", "device", deviceName)
		return
	}

	g.recordDesiredHeartbeatIfEligible(deviceName)

	w.Header().Set(desiredETagHeader, etag)

	if match := strings.TrimSpace(r.Header.Get("If-None-Match")); match != "" && match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(desired); err != nil {
		g.log.Error(err, "encode desired response", "device", deviceName)
	}
}

func (g *Gateway) handleReport(ctx context.Context, w http.ResponseWriter, r *http.Request, deviceName string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxReportBodyBytes)
	defer r.Body.Close()
	var req ReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		g.respondErr(ctx, w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	var reportedAt *time.Time
	if req.Timestamp != "" {
		parsed, err := time.Parse(time.RFC3339, req.Timestamp)
		if err != nil {
			g.respondErr(ctx, w, http.StatusBadRequest, "invalid timestamp")
			return
		}
		reportedAt = &parsed
	}
	if reportedAt == nil {
		now := time.Now().UTC()
		reportedAt = &now
	}

	now := time.Now()
	prevSeen, hb := g.lastSeenAndHeartbeat(deviceName)
	staleAfter := time.Duration(hb*g.staleMultiplier) * time.Second
	isStale := prevSeen.IsZero() || now.Sub(prevSeen) > staleAfter

	g.recordHeartbeat(deviceName, hb)
	g.recordReport(deviceName)

	if isStale {
		if err := g.markDeviceConnected(ctx, deviceName); err != nil {
			g.log.Error(err, "mark device connected", "device", deviceName)
			g.respondErr(ctx, w, http.StatusInternalServerError, "failed to mark device connected")
			return
		}
	}

	for i := range req.Observations {
		obs := req.Observations[i]
		if err := g.updateStatusForObservation(ctx, deviceName, obs, reportedAt); err != nil {
			if apierrors.IsBadRequest(err) {
				g.respondErr(ctx, w, http.StatusBadRequest, err.Error())
				return
			}
			if apierrors.IsNotFound(err) {
				g.log.V(1).Info("deviceprocess not found for observation", "device", deviceName, "name", obs.Name, "namespace", obs.Namespace)
				continue
			}
			g.log.Error(err, "update status from observation", "device", deviceName, "name", obs.Name, "namespace", obs.Namespace)
			g.respondErr(ctx, w, http.StatusInternalServerError, "failed to apply observation")
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ReportResponse{Ack: true})
}

func (g *Gateway) handleConnect(ctx context.Context, w http.ResponseWriter, r *http.Request, deviceName string) {
	hb := g.effectiveHeartbeat(deviceName)
	g.recordHeartbeat(deviceName, hb)
	g.recordReport(deviceName)
	if err := g.markDeviceConnected(ctx, deviceName); err != nil {
		g.log.Error(err, "mark device connected", "device", deviceName)
		g.respondErr(ctx, w, http.StatusInternalServerError, "failed to mark device connected")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ReportResponse{Ack: true})
}

func (g *Gateway) respondErr(_ context.Context, w http.ResponseWriter, status int, msg string) {
	g.log.V(1).Info("http error", "status", status, "message", msg)
	http.Error(w, msg, status)
}

func (g *Gateway) computeDesired(ctx context.Context, deviceName string) (*DesiredResponse, string, error) {
	processes, err := g.listDeviceProcesses(ctx, deviceName)
	if err != nil {
		return nil, "", err
	}

	items := make([]DesiredItem, 0, len(processes))
	for i := range processes {
		proc := processes[i]
		specHash := hashSpec(&proc.Spec)
		items = append(items, DesiredItem{
			UID:        string(proc.UID),
			Namespace:  proc.Namespace,
			Name:       proc.Name,
			Generation: proc.Generation,
			Spec:       proc.Spec,
			SpecHash:   specHash,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Namespace == items[j].Namespace {
			return items[i].Name < items[j].Name
		}
		return items[i].Namespace < items[j].Namespace
	})

	heartbeat := defaultHeartbeatSeconds
	g.mu.Lock()
	if hint, ok := g.heartbeatHints[deviceName]; ok && hint > 0 {
		heartbeat = hint
	}
	g.heartbeatHints[deviceName] = heartbeat
	g.mu.Unlock()

	desired := &DesiredResponse{
		DeviceName:               deviceName,
		HeartbeatIntervalSeconds: heartbeat,
		Items:                    items,
	}

	etag := hashDesired(items)
	return desired, etag, nil
}

func (g *Gateway) listDeviceProcesses(ctx context.Context, deviceName string) ([]apiv1alpha1.DeviceProcess, error) {
	var list apiv1alpha1.DeviceProcessList
	if err := g.client.List(ctx, &list, client.MatchingFields{"spec.deviceRef.name": deviceName}); err != nil {
		return nil, err
	}

	processes := make([]apiv1alpha1.DeviceProcess, 0)
	for i := range list.Items {
		proc := list.Items[i]
		if proc.Spec.DeviceRef.Name == deviceName {
			processes = append(processes, proc)
		}
	}
	return processes, nil
}

func (g *Gateway) updateStatusForObservation(ctx context.Context, deviceName string, obs Observation, reportedAt *time.Time) error {
	const maxAttempts = 3
	key := types.NamespacedName{Name: obs.Name, Namespace: obs.Namespace}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		var proc apiv1alpha1.DeviceProcess
		if err := g.client.Get(ctx, key, &proc); err != nil {
			return err
		}

		if proc.Spec.DeviceRef.Name != deviceName {
			return apierrors.NewBadRequest(fmt.Sprintf("device %s not authorized for %s/%s", deviceName, proc.Namespace, proc.Name))
		}

		before := proc.DeepCopy()

		if proc.Status.Phase == "" {
			proc.Status.Phase = apiv1alpha1.DeviceProcessPhasePending
		}

		// DeviceProcess resources have DaemonSet semantics: resource present => agent enforces Running.
		proc.Status.RuntimeSemantics = runtimeSemanticsDaemonSet

		connectedChanged := setAgentConnected(&proc.Status, true, connectedReason, connectedMessage)
		specObservedChanged := false
		if obs.ObservedSpecHash != "" && proc.Status.ObservedSpecHash != obs.ObservedSpecHash {
			proc.Status.ObservedSpecHash = obs.ObservedSpecHash
			msg := fmt.Sprintf("hash=%s", obs.ObservedSpecHash)
			if reportedAt != nil {
				msg = fmt.Sprintf("%s reportedAt=%s", msg, reportedAt.UTC().Format(time.RFC3339))
			}
			conditions.MarkTrue(&proc.Status.Conditions, apiv1alpha1.ConditionSpecObserved, "SpecObserved", msg)
			specObservedChanged = true
		}

		processStartedChanged := false
		if obs.ProcessStarted != nil {
			if *obs.ProcessStarted {
				conditions.MarkTrue(&proc.Status.Conditions, apiv1alpha1.ConditionProcessStarted, "ProcessStarted", "process started")
				if proc.Status.Phase == apiv1alpha1.DeviceProcessPhasePending || proc.Status.Phase == "" {
					proc.Status.Phase = apiv1alpha1.DeviceProcessPhaseRunning
				}
			} else {
				if obs.ErrorMessage != nil && strings.TrimSpace(*obs.ErrorMessage) != "" {
					conditions.MarkFalse(&proc.Status.Conditions, apiv1alpha1.ConditionProcessStarted, "ReconcileError", strings.TrimSpace(*obs.ErrorMessage))
				} else {
					conditions.MarkFalse(&proc.Status.Conditions, apiv1alpha1.ConditionProcessStarted, "ProcessNotStarted", "process not started")
				}
			}
			processStartedChanged = true
		}

		specWarningChanged := false
		if obs.WarningMessage != nil && strings.TrimSpace(*obs.WarningMessage) != "" {
			msg := strings.TrimSpace(*obs.WarningMessage)
			if existing := conditions.FindCondition(proc.Status.Conditions, apiv1alpha1.ConditionSpecWarning); existing == nil || existing.Status != metav1.ConditionTrue || existing.Message != msg {
				specWarningChanged = true
			}
			conditions.MarkTrue(&proc.Status.Conditions, apiv1alpha1.ConditionSpecWarning, "SpecWarning", msg)
		}

		healthChanged := false
		if obs.Healthy != nil {
			if *obs.Healthy {
				conditions.MarkTrue(&proc.Status.Conditions, apiv1alpha1.ConditionHealthy, "Healthy", "process healthy")
				if proc.Status.Phase == apiv1alpha1.DeviceProcessPhasePending || proc.Status.Phase == "" {
					proc.Status.Phase = apiv1alpha1.DeviceProcessPhaseRunning
				}
			} else {
				conditions.MarkFalse(&proc.Status.Conditions, apiv1alpha1.ConditionHealthy, "Unhealthy", "process reported unhealthy")
			}
			healthChanged = true
		}

		proc.Status.PID = obs.PID
		if strings.TrimSpace(obs.StartTime) == "" {
			proc.Status.StartTime = nil
		} else {
			startTime, err := time.Parse(time.RFC3339, obs.StartTime)
			if err != nil {
				return apierrors.NewBadRequest("invalid startTime")
			}
			t := metav1.NewTime(startTime)
			proc.Status.StartTime = &t
		}

		if reflectDeepEqualStatus(before.Status, proc.Status) {
			return nil
		}

		if err := g.client.Status().Patch(ctx, &proc, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return err
		}

		if connectedChanged {
			g.recorder.Event(&proc, corev1.EventTypeNormal, "AgentConnected", fmt.Sprintf("device %s connected", deviceName))
		}
		if specObservedChanged {
			msg := fmt.Sprintf("hash %s", obs.ObservedSpecHash)
			if reportedAt != nil {
				msg = fmt.Sprintf("%s at %s", msg, reportedAt.UTC().Format(time.RFC3339))
			}
			g.recorder.Event(&proc, corev1.EventTypeNormal, "SpecObserved", msg)
		}
		if specWarningChanged && obs.WarningMessage != nil && strings.TrimSpace(*obs.WarningMessage) != "" {
			g.recorder.Event(&proc, corev1.EventTypeWarning, "SpecWarning", strings.TrimSpace(*obs.WarningMessage))
		}
		if processStartedChanged && obs.ProcessStarted != nil && *obs.ProcessStarted {
			g.recorder.Event(&proc, corev1.EventTypeNormal, "ProcessStarted", "process started")
		}
		if healthChanged && obs.Healthy != nil {
			eventType := corev1.EventTypeNormal
			if !*obs.Healthy {
				eventType = corev1.EventTypeWarning
			}
			g.recorder.Event(&proc, eventType, "Healthy", "process health reported")
		}

		return nil
	}

	return apierrors.NewConflict(apiv1alpha1.SchemeGroupVersion.WithResource("deviceprocesses").GroupResource(), obs.Name, fmt.Errorf("status patch conflict after retries"))
}

func (g *Gateway) markDeviceConnected(ctx context.Context, deviceName string) error {
	procs, err := g.listDeviceProcesses(ctx, deviceName)
	if err != nil {
		return err
	}
	for i := range procs {
		proc := procs[i]
		before := proc.DeepCopy()
		changed := setAgentConnected(&proc.Status, true, connectedReason, connectedMessage)
		if !changed {
			continue
		}
		if err := g.client.Status().Patch(ctx, &proc, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
		g.recorder.Event(&proc, corev1.EventTypeNormal, "AgentConnected", "device connected")
	}
	g.recordReport(deviceName)
	return nil
}

func (g *Gateway) stalenessLoop(ctx context.Context) {
	interval := g.defaultInterval
	if interval <= 0 {
		interval = time.Duration(defaultHeartbeatSeconds) * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.markStaleDevices(ctx)
		}
	}
}

func (g *Gateway) markStaleDevices(ctx context.Context) {
	now := time.Now()
	g.mu.RLock()
	last := make(map[string]time.Time, len(g.lastSeen))
	interval := make(map[string]int, len(g.heartbeatHints))
	for k, v := range g.lastSeen {
		last[k] = v
	}
	for k, v := range g.heartbeatHints {
		interval[k] = v
	}
	g.mu.RUnlock()

	for device, seen := range last {
		hb := interval[device]
		if hb <= 0 {
			hb = int(g.defaultInterval.Seconds())
			if hb <= 0 {
				hb = defaultHeartbeatSeconds
			}
		}
		staleAfter := time.Duration(hb*g.staleMultiplier) * time.Second
		if now.Sub(seen) <= staleAfter {
			continue
		}
		if err := g.markDeviceDisconnected(ctx, device, now.Sub(seen)); err != nil {
			g.log.Error(err, "mark device disconnected", "device", device)
		}
	}
}

func (g *Gateway) markDeviceDisconnected(ctx context.Context, deviceName string, age time.Duration) error {
	procs, err := g.listDeviceProcesses(ctx, deviceName)
	if err != nil {
		return err
	}

	for i := range procs {
		proc := procs[i]
		before := proc.DeepCopy()
		changed := setAgentConnected(&proc.Status, false, "AgentDisconnected", "device stale (no recent reports)")
		if !changed {
			continue
		}
		if proc.Status.Phase == "" {
			proc.Status.Phase = apiv1alpha1.DeviceProcessPhasePending
		}
		if err := g.client.Status().Patch(ctx, &proc, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
		g.recorder.Event(&proc, corev1.EventTypeWarning, "AgentDisconnected", fmt.Sprintf("device %s stale", deviceName))
	}
	return nil
}

// recordDesiredHeartbeatIfEligible only counts a desired poll as a heartbeat if we
// have seen a recent report from the same device. This prevents devices from
// appearing healthy when they only read desired state without sending reports.
func (g *Gateway) recordDesiredHeartbeatIfEligible(deviceName string) {
	now := time.Now()
	g.mu.RLock()
	lastReport := g.lastReport[deviceName]
	heartbeat := g.heartbeatHints[deviceName]
	g.mu.RUnlock()

	if lastReport.IsZero() {
		return
	}

	heartbeat = g.normalizeHeartbeat(heartbeat)
	staleAfter := time.Duration(heartbeat*g.staleMultiplier) * time.Second
	if now.Sub(lastReport) > staleAfter {
		return
	}

	g.recordHeartbeat(deviceName, heartbeat)
}

func (g *Gateway) lastSeenAndHeartbeat(deviceName string) (time.Time, int) {
	g.mu.RLock()
	last := g.lastSeen[deviceName]
	hb := g.heartbeatHints[deviceName]
	g.mu.RUnlock()

	hb = g.normalizeHeartbeat(hb)
	return last, hb
}

func (g *Gateway) normalizeHeartbeat(hb int) int {
	if hb <= 0 {
		hb = int(g.defaultInterval.Seconds())
		if hb <= 0 {
			hb = defaultHeartbeatSeconds
		}
	}
	return hb
}

func (g *Gateway) effectiveHeartbeat(deviceName string) int {
	g.mu.RLock()
	hb := g.heartbeatHints[deviceName]
	g.mu.RUnlock()
	return g.normalizeHeartbeat(hb)
}

// recordReport tracks the time of the last report/connect for a device.
func (g *Gateway) recordReport(deviceName string) {
	g.mu.Lock()
	g.lastReport[deviceName] = time.Now()
	g.mu.Unlock()
}

func computeDeviceToken(secret, device string) string {
	h := hmac.New(func() hash.Hash { return sha256.New() }, []byte(secret))
	_, _ = h.Write([]byte(device))
	return hex.EncodeToString(h.Sum(nil))
}

func (g *Gateway) recordHeartbeat(deviceName string, heartbeatHint int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastSeen[deviceName] = time.Now()
	if heartbeatHint > 0 {
		g.heartbeatHints[deviceName] = heartbeatHint
	}
}

func setAgentConnected(status *apiv1alpha1.DeviceProcessStatus, connected bool, reason, message string) bool {
	desiredStatus := metav1.ConditionFalse
	if connected {
		desiredStatus = metav1.ConditionTrue
	}

	var beforeCopy *metav1.Condition
	if existing := conditions.FindCondition(status.Conditions, apiv1alpha1.ConditionAgentConnected); existing != nil {
		tmp := *existing
		beforeCopy = &tmp
	}

	conditions.SetCondition(&status.Conditions, metav1.Condition{Type: string(apiv1alpha1.ConditionAgentConnected), Status: desiredStatus, Reason: reason, Message: message})
	after := conditions.FindCondition(status.Conditions, apiv1alpha1.ConditionAgentConnected)

	if beforeCopy == nil || after == nil {
		return true
	}

	return beforeCopy.Status != after.Status || beforeCopy.Reason != after.Reason || beforeCopy.Message != after.Message
}

func hashSpec(spec *apiv1alpha1.DeviceProcessSpec) string {
	data, _ := json.Marshal(spec)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func hashDesired(items []DesiredItem) string {
	b := strings.Builder{}
	for i := range items {
		item := items[i]
		b.WriteString(item.Namespace)
		b.WriteByte('/')
		b.WriteString(item.Name)
		b.WriteByte('/')
		b.WriteString(strconv.FormatInt(item.Generation, 10))
		b.WriteByte('/')
		b.WriteString(item.SpecHash)
		b.WriteByte(';')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "\"" + hex.EncodeToString(sum[:]) + "\""
}

func reflectDeepEqualStatus(a, b apiv1alpha1.DeviceProcessStatus) bool {
	return a.Phase == b.Phase &&
		a.ObservedSpecHash == b.ObservedSpecHash &&
		a.RuntimeSemantics == b.RuntimeSemantics &&
		conditionsEqual(a.Conditions, b.Conditions) &&
		a.ArtifactVersion == b.ArtifactVersion &&
		a.PID == b.PID &&
		equalTimePtr(a.StartTime, b.StartTime) &&
		equalTimePtr(a.LastTransitionTime, b.LastTransitionTime) &&
		a.RestartCount == b.RestartCount &&
		a.LastTerminationReason == b.LastTerminationReason
}

func conditionsEqual(a, b []metav1.Condition) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].Status != b[i].Status || a[i].Reason != b[i].Reason || a[i].Message != b[i].Message {
			return false
		}
	}
	return true
}

func equalTimePtr(a, b *metav1.Time) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return a.Equal(b)
	}
}
