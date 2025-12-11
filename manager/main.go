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

// RegisteredDevice captures information about an agent that has registered.
type RegisteredDevice struct {
	DeviceID     string    `json:"deviceId"`
	AgentVersion string    `json:"agentVersion"`
	DeviceType   string    `json:"deviceType"`
	RegisteredAt time.Time `json:"registeredAt"`
	LastSeen     time.Time `json:"lastSeen"`
}

type registeredDeviceView struct {
	DeviceID     string    `json:"deviceId"`
	AgentVersion string    `json:"agentVersion"`
	DeviceType   string    `json:"deviceType"`
	RegisteredAt time.Time `json:"registeredAt"`
	LastSeen     time.Time `json:"lastSeen"`
	Online       bool      `json:"online"`
}

// DeviceStatus is reported by agents after executing the desired command.
type DeviceStatus struct {
	DeviceID string `json:"deviceId"`
	Version  string `json:"version"`
	State    string `json:"state"`
	Message  string `json:"message"`
}

type deviceStatusView struct {
	DeviceID string `json:"deviceId"`
	Version  string `json:"version"`
	State    string `json:"state"`
	Message  string `json:"message"`
	Online   bool   `json:"online"`
}

// Server holds shared state guarded by a mutex to be safe for concurrent access.
type Server struct {
	mu                sync.Mutex
	desired           DesiredState
	registeredDevices map[string]RegisteredDevice
	deviceStatuses    map[string]DeviceStatus
}

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
	mux.HandleFunc("/desired/", srv.handleDesired) // GET /desired/<deviceId>
	mux.HandleFunc("/updateDesired", srv.handleUpdateDesired)
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

	w.Header().Set("Content-Type", "application/json")

	s.mu.Lock()
	desired := s.desired
	s.mu.Unlock()

	if err := json.NewEncoder(w).Encode(desired); err != nil {
		http.Error(w, "failed to encode state", http.StatusInternalServerError)
		return
	}
}

func (s *Server) handleUpdateDesired(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	var desired DesiredState
	if err := json.NewDecoder(r.Body).Decode(&desired); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if desired.Version == "" {
		http.Error(w, "version is required", http.StatusBadRequest)
		return
	}
	if len(desired.Command) == 0 {
		http.Error(w, "command is required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.desired = desired
	s.mu.Unlock()

	log.Printf("desired state updated to version=%s command=%v", desired.Version, desired.Command)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(desired); err != nil {
		http.Error(w, "failed to encode state", http.StatusInternalServerError)
		return
	}
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
			RegisteredAt: d.RegisteredAt,
			LastSeen:     d.LastSeen,
			Online:       isOnline(d.LastSeen),
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
		if reg, ok := s.registeredDevices[id]; ok {
			lastSeen = reg.LastSeen
		}
		statuses = append(statuses, deviceStatusView{
			DeviceID: st.DeviceID,
			Version:  st.Version,
			State:    st.State,
			Message:  st.Message,
			Online:   isOnline(lastSeen),
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

	s.mu.Lock()
	if reg, ok := s.registeredDevices[status.DeviceID]; ok {
		reg.LastSeen = time.Now()
		s.registeredDevices[status.DeviceID] = reg
	}
	s.deviceStatuses[status.DeviceID] = status
	s.mu.Unlock()

	state := "OFFLINE"
	if reg, ok := s.registeredDevices[status.DeviceID]; ok && isOnline(reg.LastSeen) {
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
