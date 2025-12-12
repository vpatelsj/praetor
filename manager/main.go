package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"manager/pkg/types"
)

const offlineThreshold = 15 * time.Second

// DesiredState represents the command the manager wants agents to run.
type DesiredState struct {
	Version string   `json:"version"`
	Command []string `json:"command"`
}

// Device captures the minimal metadata Praetor tracks per registered device.
type Device struct {
	ID         string            `json:"deviceId"`
	DeviceType types.DeviceType  `json:"deviceType"`
	Labels     map[string]string `json:"labels"`
	LastSeen   time.Time         `json:"lastSeen"`
	Online     bool              `json:"online"`
}

// DeviceStatus is reported by agents after executing the desired command.
type DeviceStatus struct {
	DeviceID string `json:"deviceId"`
	Version  string `json:"version"`
	State    string `json:"state"`
	Message  string `json:"message"`
}

type deviceStatusView struct {
	DeviceID   string            `json:"deviceId"`
	Version    string            `json:"version"`
	State      string            `json:"state"`
	Message    string            `json:"message"`
	DeviceType types.DeviceType  `json:"deviceType"`
	Labels     map[string]string `json:"labels"`
	Online     bool              `json:"online"`
	Selected   bool              `json:"selected"`
}

type rolloutRequest struct {
	Version         string            `json:"version"`
	Command         []string          `json:"command"`
	MatchLabels     map[string]string `json:"matchLabels"`
	MaxFailureRatio float64           `json:"maxFailureRatio"`
}

type rolloutStatusRequest struct {
	DeviceID     string `json:"deviceId"`
	GenerationID int64  `json:"generationId"`
	State        string `json:"state"`
	Message      string `json:"message"`
}

// RolloutSpec defines the desired state for a rollout.
type RolloutSpec struct {
	Version     string            `json:"version"`
	Selector    map[string]string `json:"selector"`
	MaxFailures float64           `json:"maxFailures"`
}

// RolloutStatus captures progress of a rollout.
type RolloutStatus struct {
	Generation         int64             `json:"generation"`
	ObservedGeneration int64             `json:"observedGeneration"`
	UpdatedDevices     map[string]bool   `json:"updatedDevices"`
	FailedDevices      map[string]string `json:"failedDevices"`
	TotalTargets       int               `json:"totalTargets"`
	SuccessCount       int               `json:"successCount"`
	FailureCount       int               `json:"failureCount"`
	State              string            `json:"state"`
}

// Rollout is a namespaced rollout resource scoped to a DeviceType.
type Rollout struct {
	Name       string           `json:"name"`
	DeviceType types.DeviceType `json:"deviceType"`
	CreatedAt  time.Time        `json:"createdAt"`
	Spec       RolloutSpec      `json:"spec"`
	Status     RolloutStatus    `json:"status"`
}

type legacyGeneration struct {
	ID              int64             `json:"id"`
	Version         string            `json:"version"`
	Selector        map[string]string `json:"selector"`
	CreatedAt       time.Time         `json:"createdAt"`
	State           string            `json:"state"`
	UpdatedDevices  map[string]bool   `json:"updatedDevices"`
	FailedDevices   map[string]string `json:"failedDevices"`
	TotalTargets    int               `json:"totalTargets"`
	SuccessCount    int               `json:"successCount"`
	FailureCount    int               `json:"failureCount"`
	MaxFailureRatio float64           `json:"maxFailureRatio"`
}

// Server holds shared state guarded by a mutex to be safe for concurrent access.
type Server struct {
	mu              sync.Mutex
	desired         DesiredState
	devicesByType   map[types.DeviceType]map[string]*Device
	deviceTypeIndex map[string]types.DeviceType
	deviceStatuses  map[string]DeviceStatus
}

var activeSelector = map[string]string{}
var rolloutsByType = map[types.DeviceType]map[string]*Rollout{}
var activeRollout *Rollout
var nextGenerationID int64 = 1

func main() {
	srv := &Server{
		desired: DesiredState{
			Version: "v1",
			Command: []string{"echo", "Hello from Praetor v1!"},
		},
		devicesByType:   make(map[types.DeviceType]map[string]*Device),
		deviceTypeIndex: make(map[string]types.DeviceType),
		deviceStatuses:  make(map[string]DeviceStatus),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/desired/", srv.handleDesired) // GET /desired/<deviceId> (legacy)
	mux.HandleFunc("/rollout", srv.handleRollout)
	mux.HandleFunc("/rollout/", srv.handleRolloutTarget)
	mux.HandleFunc("/rolloutStatus", srv.handleRolloutStatus)
	mux.HandleFunc("/register", srv.handleRegister)
	mux.HandleFunc("/heartbeat", srv.handleHeartbeat)
	mux.HandleFunc("/devices/registered", srv.handleRegisteredDevices)
	mux.HandleFunc("/devices", srv.handleDevices)
	mux.HandleFunc("/status", srv.handleStatus)

	log.Println("Praetor manager listening on :8080")
	if err := http.ListenAndServe(":8080", mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func (s *Server) handleDesired(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	deviceID := strings.TrimPrefix(r.URL.Path, "/desired/")
	if deviceID == "" || deviceID == r.URL.Path {
		http.Error(w, "deviceId is required in path", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	desired := s.desired
	device, ok := s.getDeviceLocked(deviceID)
	s.mu.Unlock()

	if !ok {
		http.Error(w, "device not registered", http.StatusNotFound)
		return
	}

	if !deviceMatchesSelector(device, activeSelector) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(desired); err != nil {
		http.Error(w, "failed to encode state", http.StatusInternalServerError)
		return
	}
}

func (s *Server) handleRollout(w http.ResponseWriter, r *http.Request) {
	// Only handle GET for exact /rollout path (list all rollouts)
	if r.Method == http.MethodGet && r.URL.Path == "/rollout" {
		s.handleListRollouts(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	var req rolloutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Version == "" {
		http.Error(w, "version is required", http.StatusBadRequest)
		return
	}
	if req.MatchLabels == nil {
		req.MatchLabels = map[string]string{}
	}
	s.mu.Lock()
	if len(req.Command) == 0 {
		req.Command = s.desired.Command
	}
	selector := copySelector(req.MatchLabels)
	devices := s.allDevicesLocked()
	targets := make([]string, 0, len(devices))
	for _, dev := range devices {
		if deviceMatchesSelector(dev, selector) {
			targets = append(targets, dev.ID)
		}
	}

	name := "legacy-generation-" + strconv.FormatInt(nextGenerationID, 10)
	now := time.Now()
	rollout := &Rollout{
		Name:       name,
		DeviceType: types.DeviceTypeSwitch,
		CreatedAt:  now,
		Spec: RolloutSpec{
			Version:     req.Version,
			Selector:    selector,
			MaxFailures: req.MaxFailureRatio,
		},
		Status: RolloutStatus{
			Generation:         nextGenerationID,
			ObservedGeneration: nextGenerationID,
			UpdatedDevices:     make(map[string]bool),
			FailedDevices:      make(map[string]string),
			TotalTargets:       len(targets),
			State:              "Running",
		},
	}

	nextGenerationID++
	activeSelector = selector
	activeRollout = rollout
	s.desired.Version = req.Version
	s.desired.Command = req.Command
	if rolloutsByType[rollout.DeviceType] == nil {
		rolloutsByType[rollout.DeviceType] = make(map[string]*Rollout)
	}
	rolloutsByType[rollout.DeviceType][rollout.Name] = rollout
	s.mu.Unlock()

	log.Printf("[ROLLOUT] generation=%d version=%s selector=%+v targets=%d", rollout.Status.Generation, rollout.Spec.Version, selector, rollout.Status.TotalTargets)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(legacyGenerationFromRollout(rollout)); err != nil {
		http.Error(w, "failed to encode rollout", http.StatusInternalServerError)
		return
	}
}

func legacyGenerationFromRollout(rollout *Rollout) legacyGeneration {
	return legacyGeneration{
		ID:              rollout.Status.Generation,
		Version:         rollout.Spec.Version,
		Selector:        copySelector(rollout.Spec.Selector),
		CreatedAt:       rollout.CreatedAt,
		State:           rollout.Status.State,
		UpdatedDevices:  copyBoolMap(rollout.Status.UpdatedDevices),
		FailedDevices:   copyStringMap(rollout.Status.FailedDevices),
		TotalTargets:    rollout.Status.TotalTargets,
		SuccessCount:    rollout.Status.SuccessCount,
		FailureCount:    rollout.Status.FailureCount,
		MaxFailureRatio: rollout.Spec.MaxFailures,
	}
}

func (s *Server) handleListRollouts(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	rollouts := make([]legacyGeneration, 0)
	for _, typed := range rolloutsByType {
		for _, rollout := range typed {
			rollouts = append(rollouts, legacyGenerationFromRollout(rollout))
		}
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(rollouts); err != nil {
		http.Error(w, "failed to encode rollouts", http.StatusInternalServerError)
		return
	}
}

func (s *Server) handleRolloutTarget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	deviceID := strings.TrimPrefix(r.URL.Path, "/rollout/")
	if deviceID == "" || deviceID == r.URL.Path {
		http.Error(w, "deviceId is required in path", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	rollout := activeRollout
	device, ok := s.getDeviceLocked(deviceID)
	command := s.desired.Command
	s.mu.Unlock()

	if rollout == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !ok {
		http.Error(w, "device not registered", http.StatusNotFound)
		return
	}
	if !deviceMatchesSelector(device, rollout.Spec.Selector) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	resp := struct {
		GenerationID int64    `json:"generationId"`
		Version      string   `json:"version"`
		Command      []string `json:"command"`
	}{
		GenerationID: rollout.Status.Generation,
		Version:      rollout.Spec.Version,
		Command:      command,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, "failed to encode rollout target", http.StatusInternalServerError)
		return
	}
}

func (s *Server) handleRolloutStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	var req rolloutStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.DeviceID == "" || req.GenerationID == 0 || (req.State != "Succeeded" && req.State != "Failed") {
		http.Error(w, "deviceId, generationId, and valid state are required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	rollout, ok := s.findRolloutByGenerationLocked(req.GenerationID)
	device, deviceKnown := s.getDeviceLocked(req.DeviceID)
	s.mu.Unlock()

	if !ok {
		http.Error(w, "generation not found", http.StatusNotFound)
		return
	}
	if !deviceKnown || !deviceMatchesSelector(device, rollout.Spec.Selector) {
		http.Error(w, "device not part of generation", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if rollout.Status.UpdatedDevices[req.DeviceID] {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if _, exists := rollout.Status.FailedDevices[req.DeviceID]; exists {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.State {
	case "Succeeded":
		rollout.Status.UpdatedDevices[req.DeviceID] = true
		rollout.Status.SuccessCount++
	case "Failed":
		rollout.Status.FailedDevices[req.DeviceID] = req.Message
		rollout.Status.FailureCount++
	}

	var failureRatio float64
	if rollout.Status.TotalTargets > 0 {
		failureRatio = float64(rollout.Status.FailureCount) / float64(rollout.Status.TotalTargets)
	}

	if failureRatio >= rollout.Spec.MaxFailures && rollout.Status.State == "Running" {
		rollout.Status.State = "Paused"
	}
	if rollout.Status.SuccessCount == rollout.Status.TotalTargets {
		rollout.Status.State = "Succeeded"
	}

	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleUpdateSelector(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	var payload struct {
		MatchLabels map[string]string `json:"matchLabels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if payload.MatchLabels == nil {
		payload.MatchLabels = map[string]string{}
	}

	s.mu.Lock()
	activeSelector = copySelector(payload.MatchLabels)
	s.mu.Unlock()

	log.Printf("[SELECTOR] updated to %+v", payload.MatchLabels)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	var req struct {
		DeviceID   string            `json:"deviceId"`
		DeviceType string            `json:"deviceType"`
		Labels     map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.DeviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}

	dt, err := types.ParseDeviceType(req.DeviceType)
	if err != nil {
		http.Error(w, "invalid deviceType", http.StatusBadRequest)
		return
	}
	if req.Labels == nil {
		req.Labels = map[string]string{}
	}

	now := time.Now()
	s.mu.Lock()
	if s.devicesByType[dt] == nil {
		s.devicesByType[dt] = make(map[string]*Device)
	}
	device, ok := s.devicesByType[dt][req.DeviceID]
	if !ok {
		device = &Device{
			ID:         req.DeviceID,
			DeviceType: dt,
		}
		s.devicesByType[dt][req.DeviceID] = device
	}
	device.Labels = req.Labels
	device.LastSeen = now
	device.Online = true
	s.deviceTypeIndex[req.DeviceID] = dt
	s.mu.Unlock()

	log.Printf("[REGISTER] device=%s type=%s", req.DeviceID, dt)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(device); err != nil {
		http.Error(w, "failed to encode registration", http.StatusInternalServerError)
		return
	}
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	var payload struct {
		DeviceID string `json:"deviceId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if payload.DeviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}

	now := time.Now()
	s.mu.Lock()
	dev, ok := s.getDeviceLocked(payload.DeviceID)
	if ok {
		dev.LastSeen = now
		dev.Online = true
	}
	s.mu.Unlock()

	if !ok {
		http.Error(w, "device not registered", http.StatusNotFound)
		return
	}

	state := "OFFLINE"
	if isOnline(now) {
		state = "ONLINE"
	}
	log.Printf("[HEARTBEAT] device=%s lastSeen=%s %s", payload.DeviceID, now.Format(time.RFC3339), state)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleRegisteredDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	s.mu.Lock()
	registered := make([]Device, 0)
	for _, dev := range s.allDevicesLocked() {
		copy := *dev
		copy.Online = isOnline(dev.LastSeen)
		registered = append(registered, copy)
	}
	s.mu.Unlock()

	if err := json.NewEncoder(w).Encode(registered); err != nil {
		http.Error(w, "failed to encode devices", http.StatusInternalServerError)
		return
	}
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	typeFilter := r.URL.Query().Get("type")
	var filter types.DeviceType
	var err error
	var hasFilter bool
	if typeFilter != "" {
		filter, err = types.ParseDeviceType(typeFilter)
		if err != nil {
			http.Error(w, "invalid device type filter", http.StatusBadRequest)
			return
		}
		hasFilter = true
	}
	w.Header().Set("Content-Type", "application/json")

	s.mu.Lock()
	statuses := make([]deviceStatusView, 0, len(s.deviceStatuses))
	for id, st := range s.deviceStatuses {
		dev, ok := s.getDeviceLocked(id)
		if !ok {
			continue
		}
		if hasFilter && dev.DeviceType != filter {
			continue
		}
		statuses = append(statuses, deviceStatusView{
			DeviceID:   st.DeviceID,
			Version:    st.Version,
			State:      st.State,
			Message:    st.Message,
			DeviceType: dev.DeviceType,
			Labels:     dev.Labels,
			Online:     isOnline(dev.LastSeen),
			Selected:   deviceMatchesSelector(dev, activeSelector),
		})
	}
	s.mu.Unlock()

	if err := json.NewEncoder(w).Encode(statuses); err != nil {
		http.Error(w, "failed to encode devices", http.StatusInternalServerError)
		return
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	var status DeviceStatus
	if err := json.NewDecoder(r.Body).Decode(&status); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if status.DeviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}

	statusOnline := false
	s.mu.Lock()
	if dev, ok := s.getDeviceLocked(status.DeviceID); ok {
		dev.LastSeen = time.Now()
		dev.Online = true
		statusOnline = isOnline(dev.LastSeen)
	}
	s.deviceStatuses[status.DeviceID] = status
	s.mu.Unlock()

	state := "OFFLINE"
	if statusOnline {
		state = "ONLINE"
	}
	log.Printf("status from %s version=%s state=%s message=%s %s", status.DeviceID, status.Version, status.State, status.Message, state)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) allDevicesLocked() []*Device {
	devices := make([]*Device, 0)
	for _, typed := range s.devicesByType {
		for _, dev := range typed {
			devices = append(devices, dev)
		}
	}
	return devices
}

func (s *Server) getDeviceLocked(id string) (*Device, bool) {
	deviceType, ok := s.deviceTypeIndex[id]
	if !ok {
		return nil, false
	}
	devices := s.devicesByType[deviceType]
	if devices == nil {
		return nil, false
	}
	dev, ok := devices[id]
	return dev, ok
}

func (s *Server) findRolloutByGenerationLocked(id int64) (*Rollout, bool) {
	for _, typed := range rolloutsByType {
		for _, rollout := range typed {
			if rollout.Status.Generation == id {
				return rollout, true
			}
		}
	}
	return nil, false
}

func copySelector(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return copyStringMap(m)
}

func copyStringMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

func copyBoolMap(m map[string]bool) map[string]bool {
	if m == nil {
		return map[string]bool{}
	}
	cp := make(map[string]bool, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

func isOnline(lastSeen time.Time) bool {
	if lastSeen.IsZero() {
		return false
	}
	return time.Since(lastSeen) <= offlineThreshold
}

func deviceMatchesSelector(device *Device, sel map[string]string) bool {
	if len(sel) == 0 {
		return true
	}
	for k, v := range sel {
		if device.Labels[k] != v {
			return false
		}
	}
	return true
}
