package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const offlineThreshold = 15 * time.Second

// DesiredState represents the command the manager wants agents to run.
type DesiredState struct {
	Version string   `json:"version"`
	Command []string `json:"command"`
}

// Selector defines label matching for targeting desired state.
type Selector struct {
	MatchLabels map[string]string `json:"matchLabels"`
}

type DeviceType string

const (
	DeviceTypeServer        DeviceType = "Server"
	DeviceTypeNetworkSwitch DeviceType = "NetworkSwitch"
	DeviceTypeDPU           DeviceType = "DPU"
	DeviceTypeSOC           DeviceType = "SOC"
	DeviceTypeBMC           DeviceType = "BMC"
	DeviceTypeSimulator     DeviceType = "Simulator"
)

var allowedCapabilities = map[DeviceType]map[string]bool{
	DeviceTypeServer: {
		"systemd":    true,
		"container":  true,
		"raw-binary": true,
	},
	DeviceTypeNetworkSwitch: {
		"systemd":    true,
		"raw-binary": true,
	},
	DeviceTypeDPU: {
		"systemd":    true,
		"container":  true,
		"raw-binary": true,
	},
	DeviceTypeSOC: {
		"systemd":    true,
		"raw-binary": true,
	},
	DeviceTypeBMC: {
		"initd":      true,
		"raw-binary": true,
	},
	DeviceTypeSimulator: {
		"systemd":    true,
		"container":  true,
		"raw-binary": true,
		"initd":      true,
	},
}

// RegisteredDevice captures information about an agent that has registered.
type RegisteredDevice struct {
	DeviceID     string            `json:"deviceId"`
	AgentVersion string            `json:"agentVersion"`
	DeviceType   DeviceType        `json:"deviceType"`
	Labels       map[string]string `json:"labels"`
	Capabilities []string          `json:"capabilities"`
	RegisteredAt time.Time         `json:"registeredAt"`
	LastSeen     time.Time         `json:"lastSeen"`
}

type Generation struct {
	ID              int64             `json:"id"`
	Version         string            `json:"version"`
	Selector        Selector          `json:"selector"`
	CreatedAt       time.Time         `json:"createdAt"`
	State           string            `json:"state"` // Planned, Running, Paused, Succeeded, Failed
	UpdatedDevices  map[string]bool   `json:"updatedDevices"`
	FailedDevices   map[string]string `json:"failedDevices"`
	TotalTargets    int               `json:"totalTargets"`
	SuccessCount    int               `json:"successCount"`
	FailureCount    int               `json:"failureCount"`
	MaxFailureRatio float64           `json:"maxFailureRatio"`
}

type registeredDeviceView struct {
	DeviceID     string            `json:"deviceId"`
	AgentVersion string            `json:"agentVersion"`
	DeviceType   DeviceType        `json:"deviceType"`
	Labels       map[string]string `json:"labels"`
	Capabilities []string          `json:"capabilities"`
	RegisteredAt time.Time         `json:"registeredAt"`
	LastSeen     time.Time         `json:"lastSeen"`
	Online       bool              `json:"online"`
	Selected     bool              `json:"selected"`
}

// DeviceStatus is reported by agents after executing the desired command.
type DeviceStatus struct {
	DeviceID string `json:"deviceId"`
	Version  string `json:"version"`
	State    string `json:"state"`
	Message  string `json:"message"`
}

type deviceStatusView struct {
	DeviceID     string            `json:"deviceId"`
	Version      string            `json:"version"`
	State        string            `json:"state"`
	Message      string            `json:"message"`
	DeviceType   DeviceType        `json:"deviceType"`
	Labels       map[string]string `json:"labels"`
	Capabilities []string          `json:"capabilities"`
	Online       bool              `json:"online"`
	Selected     bool              `json:"selected"`
}

type rolloutRequest struct {
	Version         string            `json:"version"`
	MatchLabels     map[string]string `json:"matchLabels"`
	MaxFailureRatio float64           `json:"maxFailureRatio"`
}

type rolloutStatusRequest struct {
	DeviceID     string `json:"deviceId"`
	GenerationID int64  `json:"generationId"`
	State        string `json:"state"`
	Message      string `json:"message"`
}

// Server holds shared state guarded by a mutex to be safe for concurrent access.
type Server struct {
	mu                sync.Mutex
	desired           DesiredState
	registeredDevices map[string]RegisteredDevice
	deviceStatuses    map[string]DeviceStatus
}

var activeSelector = Selector{
	MatchLabels: map[string]string{},
}
var generations = map[int64]*Generation{}
var activeGeneration *Generation
var nextGenerationID int64 = 1

func main() {
	srv := &Server{
		desired: DesiredState{
			Version: "v1",
			Command: []string{"echo", "Hello from Praetor v1!"},
		},
		registeredDevices: make(map[string]RegisteredDevice),
		deviceStatuses:    make(map[string]DeviceStatus),
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
	device, ok := s.registeredDevices[deviceID]
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
	selector := Selector{MatchLabels: req.MatchLabels}
	targets := make([]string, 0)
	for id, dev := range s.registeredDevices {
		if deviceMatchesSelector(dev, selector) {
			targets = append(targets, id)
		}
	}

	gen := &Generation{
		ID:              nextGenerationID,
		Version:         req.Version,
		Selector:        selector,
		CreatedAt:       time.Now(),
		State:           "Running",
		UpdatedDevices:  make(map[string]bool),
		FailedDevices:   make(map[string]string),
		TotalTargets:    len(targets),
		MaxFailureRatio: req.MaxFailureRatio,
	}

	nextGenerationID++
	activeSelector = selector
	activeGeneration = gen
	generations[gen.ID] = gen
	s.desired.Version = req.Version
	s.mu.Unlock()

	log.Printf("[ROLLOUT] generation=%d version=%s selector=%+v targets=%d", gen.ID, gen.Version, selector.MatchLabels, gen.TotalTargets)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(gen); err != nil {
		http.Error(w, "failed to encode rollout", http.StatusInternalServerError)
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
	gen := activeGeneration
	device, ok := s.registeredDevices[deviceID]
	command := s.desired.Command
	s.mu.Unlock()

	if gen == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !ok {
		http.Error(w, "device not registered", http.StatusNotFound)
		return
	}
	if !deviceMatchesSelector(device, gen.Selector) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	resp := struct {
		GenerationID int64    `json:"generationId"`
		Version      string   `json:"version"`
		Command      []string `json:"command"`
	}{
		GenerationID: gen.ID,
		Version:      gen.Version,
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
	gen, ok := generations[req.GenerationID]
	device, deviceKnown := s.registeredDevices[req.DeviceID]
	s.mu.Unlock()

	if !ok {
		http.Error(w, "generation not found", http.StatusNotFound)
		return
	}
	if !deviceKnown || !deviceMatchesSelector(device, gen.Selector) {
		http.Error(w, "device not part of generation", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if gen.UpdatedDevices[req.DeviceID] {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if _, exists := gen.FailedDevices[req.DeviceID]; exists {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.State {
	case "Succeeded":
		gen.UpdatedDevices[req.DeviceID] = true
		gen.SuccessCount++
	case "Failed":
		gen.FailedDevices[req.DeviceID] = req.Message
		gen.FailureCount++
	}

	var failureRatio float64
	if gen.TotalTargets > 0 {
		failureRatio = float64(gen.FailureCount) / float64(gen.TotalTargets)
	}

	if failureRatio >= gen.MaxFailureRatio && gen.State == "Running" {
		gen.State = "Paused"
	}
	if gen.SuccessCount == gen.TotalTargets {
		gen.State = "Succeeded"
	}

	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleUpdateSelector(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	var sel Selector
	if err := json.NewDecoder(r.Body).Decode(&sel); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if sel.MatchLabels == nil {
		sel.MatchLabels = map[string]string{}
	}

	s.mu.Lock()
	activeSelector = sel
	s.mu.Unlock()

	log.Printf("[SELECTOR] updated to %+v", sel.MatchLabels)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	var req RegisteredDevice
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.DeviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}
	if !isValidDeviceType(req.DeviceType) {
		http.Error(w, "invalid deviceType", http.StatusBadRequest)
		return
	}
	if req.Labels == nil {
		req.Labels = map[string]string{}
	}
	if req.Capabilities == nil {
		req.Capabilities = []string{}
	}
	for _, cap := range req.Capabilities {
		if !isCapabilityAllowed(req.DeviceType, cap) {
			http.Error(w, "capability not allowed for deviceType", http.StatusBadRequest)
			return
		}
	}

	now := time.Now()
	s.mu.Lock()
	existing, ok := s.registeredDevices[req.DeviceID]
	if !ok {
		req.RegisteredAt = now
	} else {
		req.RegisteredAt = existing.RegisteredAt
	}
	req.LastSeen = now
	s.registeredDevices[req.DeviceID] = RegisteredDevice{
		DeviceID:     req.DeviceID,
		AgentVersion: req.AgentVersion,
		DeviceType:   req.DeviceType,
		Labels:       req.Labels,
		Capabilities: req.Capabilities,
		RegisteredAt: req.RegisteredAt,
		LastSeen:     req.LastSeen,
	}
	s.mu.Unlock()

	log.Printf("[REGISTER] device=%s agent=%s type=%s", req.DeviceID, req.AgentVersion, req.DeviceType)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(req); err != nil {
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
	reg, ok := s.registeredDevices[payload.DeviceID]
	if ok {
		reg.LastSeen = now
		s.registeredDevices[payload.DeviceID] = reg
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
	devices := make([]registeredDeviceView, 0, len(s.registeredDevices))
	for _, d := range s.registeredDevices {
		devices = append(devices, registeredDeviceView{
			DeviceID:     d.DeviceID,
			AgentVersion: d.AgentVersion,
			DeviceType:   d.DeviceType,
			Labels:       d.Labels,
			Capabilities: d.Capabilities,
			RegisteredAt: d.RegisteredAt,
			LastSeen:     d.LastSeen,
			Online:       isOnline(d.LastSeen),
			Selected:     deviceMatchesSelector(d, activeSelector),
		})
	}
	s.mu.Unlock()

	if err := json.NewEncoder(w).Encode(devices); err != nil {
		http.Error(w, "failed to encode devices", http.StatusInternalServerError)
		return
	}
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	s.mu.Lock()
	statuses := make([]deviceStatusView, 0, len(s.deviceStatuses))
	for id, st := range s.deviceStatuses {
		lastSeen := time.Time{}
		reg, ok := s.registeredDevices[id]
		if ok {
			lastSeen = reg.LastSeen
		}
		statuses = append(statuses, deviceStatusView{
			DeviceID:     st.DeviceID,
			Version:      st.Version,
			State:        st.State,
			Message:      st.Message,
			DeviceType:   reg.DeviceType,
			Labels:       reg.Labels,
			Capabilities: reg.Capabilities,
			Online:       isOnline(lastSeen),
			Selected:     ok && deviceMatchesSelector(reg, activeSelector),
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
	if reg, ok := s.registeredDevices[status.DeviceID]; ok {
		reg.LastSeen = time.Now()
		s.registeredDevices[status.DeviceID] = reg
		statusOnline = isOnline(reg.LastSeen)
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

func isOnline(lastSeen time.Time) bool {
	if lastSeen.IsZero() {
		return false
	}
	return time.Since(lastSeen) <= offlineThreshold
}

func isValidDeviceType(dt DeviceType) bool {
	switch dt {
	case DeviceTypeServer, DeviceTypeNetworkSwitch, DeviceTypeDPU, DeviceTypeSOC, DeviceTypeBMC, DeviceTypeSimulator:
		return true
	default:
		return false
	}
}

func isCapabilityAllowed(dt DeviceType, cap string) bool {
	return allowedCapabilities[dt][cap]
}

func deviceMatchesSelector(device RegisteredDevice, sel Selector) bool {
	if len(sel.MatchLabels) == 0 {
		return true
	}
	for k, v := range sel.MatchLabels {
		if device.Labels[k] != v {
			return false
		}
	}
	return true
}
