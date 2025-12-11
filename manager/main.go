package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

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

// DeviceStatus is reported by agents after executing the desired command.
type DeviceStatus struct {
	DeviceID string `json:"deviceId"`
	Version  string `json:"version"`
	State    string `json:"state"`
	Message  string `json:"message"`
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

func (s *Server) handleRegisteredDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	s.mu.Lock()
	devices := make([]RegisteredDevice, 0, len(s.registeredDevices))
	for _, d := range s.registeredDevices {
		devices = append(devices, d)
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
	statuses := make([]DeviceStatus, 0, len(s.deviceStatuses))
	for _, st := range s.deviceStatuses {
		statuses = append(statuses, st)
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

	log.Printf("status from %s version=%s state=%s message=%s", status.DeviceID, status.Version, status.State, status.Message)
	w.WriteHeader(http.StatusAccepted)
}
