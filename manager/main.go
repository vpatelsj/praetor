package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
)

// DesiredState represents the command the manager wants agents to run.
type DesiredState struct {
	Version string   `json:"version"`
	Command []string `json:"command"`
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
	mu      sync.Mutex
	desired DesiredState
}

func main() {
	srv := &Server{
		desired: DesiredState{
			Version: "v1",
			Command: []string{"echo", "Hello from Praetor v1!"},
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/desired", srv.handleDesired)
	mux.HandleFunc("/status", srv.handleStatus)

	log.Println("Praetor manager listening on :8080")
	if err := http.ListenAndServe(":8080", mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func (s *Server) handleDesired(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")

		s.mu.Lock()
		desired := s.desired
		s.mu.Unlock()

		if err := json.NewEncoder(w).Encode(desired); err != nil {
			http.Error(w, "failed to encode state", http.StatusInternalServerError)
			return
		}
	case http.MethodPost:
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
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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

	log.Printf("status from %s version=%s state=%s message=%s", status.DeviceID, status.Version, status.State, status.Message)
	w.WriteHeader(http.StatusAccepted)
}
