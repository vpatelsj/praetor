package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"manager/controllers"
	"manager/pkg/model"
	"manager/pkg/types"
)

const offlineThreshold = 15 * time.Second

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

// Server holds shared state guarded by a mutex to be safe for concurrent access.
type Server struct {
	mu              sync.Mutex
	devicesByType   map[types.DeviceType]map[string]*model.Device
	deviceTypeIndex map[string]types.DeviceType
	deviceStatuses  map[string]model.DeviceStatus
}

var rolloutsByType = map[types.DeviceType]map[string]*model.Rollout{}

type rolloutController interface {
	ReconcileRollouts()
}

func main() {
	srv := &Server{
		devicesByType:   make(map[types.DeviceType]map[string]*model.Device),
		deviceTypeIndex: make(map[string]types.DeviceType),
		deviceStatuses:  make(map[string]model.DeviceStatus),
	}

	deviceTypes := []types.DeviceType{
		types.DeviceTypeSwitch,
		types.DeviceTypeDPU,
		types.DeviceTypeSOC,
		types.DeviceTypeBMC,
		types.DeviceTypeServer,
		types.DeviceTypeSim,
	}
	for _, dt := range deviceTypes {
		if rolloutsByType[dt] == nil {
			rolloutsByType[dt] = make(map[string]*model.Rollout)
		}
		if srv.devicesByType[dt] == nil {
			srv.devicesByType[dt] = make(map[string]*model.Device)
		}
	}

	switchController := controllers.NewSwitchController(&srv.mu, rolloutsByType[types.DeviceTypeSwitch], srv.devicesByType[types.DeviceTypeSwitch])
	dpuController := controllers.NewDPUController(&srv.mu, rolloutsByType[types.DeviceTypeDPU], srv.devicesByType[types.DeviceTypeDPU])
	socController := controllers.NewSOCController(&srv.mu, rolloutsByType[types.DeviceTypeSOC], srv.devicesByType[types.DeviceTypeSOC])
	bmcController := controllers.NewBMCController(&srv.mu, rolloutsByType[types.DeviceTypeBMC], srv.devicesByType[types.DeviceTypeBMC])
	serverController := controllers.NewServerController(&srv.mu, rolloutsByType[types.DeviceTypeServer], srv.devicesByType[types.DeviceTypeServer])
	simController := controllers.NewSimulatorController(&srv.mu, rolloutsByType[types.DeviceTypeSim], srv.devicesByType[types.DeviceTypeSim])

	startControllerLoop(switchController)
	startControllerLoop(dpuController)
	startControllerLoop(socController)
	startControllerLoop(bmcController)
	startControllerLoop(serverController)
	startControllerLoop(simController)

	mux := http.NewServeMux()
	mux.HandleFunc("/register", srv.handleRegister)
	mux.HandleFunc("/heartbeat", srv.handleHeartbeat)
	mux.HandleFunc("/devices/registered", srv.handleRegisteredDevices)
	mux.HandleFunc("/devices", srv.handleDevices)
	mux.HandleFunc("/api/v1/devices", srv.handleDevices)
	mux.HandleFunc("/api/v1/devicetypes/", srv.handleAPIDeviceTypes)
	mux.HandleFunc("/status", srv.handleStatus)

	log.Println("Praetor manager listening on :8080")
	if err := http.ListenAndServe(":8080", mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func startControllerLoop(ctrl rolloutController) {
	ticker := time.NewTicker(2 * time.Second)
	go func() {
		defer ticker.Stop()
		for range ticker.C {
			ctrl.ReconcileRollouts()
		}
	}()
}

func cloneRollout(src *model.Rollout) *model.Rollout {
	if src == nil {
		return nil
	}
	return &model.Rollout{
		Name:       src.Name,
		DeviceType: src.DeviceType,
		CreatedAt:  src.CreatedAt,
		Spec: model.RolloutSpec{
			Version:     src.Spec.Version,
			Selector:    copySelector(src.Spec.Selector),
			MaxFailures: src.Spec.MaxFailures,
		},
		Status: model.RolloutStatus{
			Generation:         src.Status.Generation,
			ObservedGeneration: src.Status.ObservedGeneration,
			UpdatedDevices:     copyBoolMap(src.Status.UpdatedDevices),
			FailedDevices:      copyStringMap(src.Status.FailedDevices),
			TotalTargets:       src.Status.TotalTargets,
			SuccessCount:       src.Status.SuccessCount,
			FailureCount:       src.Status.FailureCount,
			State:              src.Status.State,
		},
	}
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
		s.devicesByType[dt] = make(map[string]*model.Device)
	}
	device, ok := s.devicesByType[dt][req.DeviceID]
	if !ok {
		device = &model.Device{
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
	registered := make([]model.Device, 0)
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
			Selected:   s.deviceSelectedLocked(dev),
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

	var status model.DeviceStatus
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

func (s *Server) handleAPIDeviceTypes(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/devicetypes/") {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/devicetypes/")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) < 1 {
		http.NotFound(w, r)
		return
	}
	deviceTypeStr := parts[0]
	if deviceTypeStr == "" {
		http.Error(w, "device type required", http.StatusBadRequest)
		return
	}

	deviceType, err := types.ParseDeviceType(deviceTypeStr)
	if err != nil {
		http.Error(w, "invalid device type", http.StatusBadRequest)
		return
	}

	remaining := parts[1:]
	if len(remaining) == 0 || remaining[0] == "" {
		http.NotFound(w, r)
		return
	}

	switch remaining[0] {
	case "rollouts":
		s.handleDeviceTypeRollouts(w, r, deviceType, remaining[1:])
	case "devices":
		if len(remaining) != 1 {
			http.NotFound(w, r)
			return
		}
		s.handleDeviceTypeDevices(w, r, deviceType)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleDeviceTypeRollouts(w http.ResponseWriter, r *http.Request, deviceType types.DeviceType, remaining []string) {
	if len(remaining) == 0 || remaining[0] == "" {
		switch r.Method {
		case http.MethodGet:
			s.respondWithRollouts(w, deviceType)
		case http.MethodPost:
			s.createRolloutForType(w, r, deviceType)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	name := remaining[0]
	if len(remaining) == 1 {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.respondWithRollout(w, deviceType, name)
		return
	}

	if len(remaining) == 2 && remaining[1] == "status" && r.Method == http.MethodPost {
		s.updateRolloutStatusForType(w, r, deviceType, name)
		return
	}

	http.NotFound(w, r)
}

func (s *Server) handleDeviceTypeDevices(w http.ResponseWriter, r *http.Request, deviceType types.DeviceType) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	s.mu.Lock()
	devices := s.devicesByType[deviceType]
	result := make([]model.Device, 0, len(devices))
	for _, dev := range devices {
		if dev == nil {
			continue
		}
		copy := *dev
		copy.Online = isOnline(dev.LastSeen)
		result = append(result, copy)
	}
	s.mu.Unlock()

	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, "failed to encode devices", http.StatusInternalServerError)
		return
	}
}

func (s *Server) respondWithRollouts(w http.ResponseWriter, deviceType types.DeviceType) {
	w.Header().Set("Content-Type", "application/json")
	s.mu.Lock()
	typed := rolloutsByType[deviceType]
	list := make([]*model.Rollout, 0, len(typed))
	for _, rollout := range typed {
		list = append(list, cloneRollout(rollout))
	}
	s.mu.Unlock()

	if err := json.NewEncoder(w).Encode(list); err != nil {
		http.Error(w, "failed to encode rollouts", http.StatusInternalServerError)
		return
	}
}

func (s *Server) respondWithRollout(w http.ResponseWriter, deviceType types.DeviceType, name string) {
	w.Header().Set("Content-Type", "application/json")
	s.mu.Lock()
	rollout, ok := rolloutsByType[deviceType][name]
	var clone *model.Rollout
	if ok {
		clone = cloneRollout(rollout)
	}
	s.mu.Unlock()

	if !ok {
		http.Error(w, "rollout not found", http.StatusNotFound)
		return
	}
	if err := json.NewEncoder(w).Encode(clone); err != nil {
		http.Error(w, "failed to encode rollout", http.StatusInternalServerError)
		return
	}
}

func (s *Server) createRolloutForType(w http.ResponseWriter, r *http.Request, deviceType types.DeviceType) {
	type createRolloutRequest struct {
		Name        string            `json:"name"`
		Version     string            `json:"version"`
		Selector    map[string]string `json:"selector"`
		MaxFailures float64           `json:"maxFailures"`
	}
	var req createRolloutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Version == "" {
		http.Error(w, "name and version are required", http.StatusBadRequest)
		return
	}
	if req.Selector == nil {
		req.Selector = map[string]string{}
	}

	w.Header().Set("Content-Type", "application/json")
	now := time.Now()
	s.mu.Lock()
	if rolloutsByType[deviceType] == nil {
		rolloutsByType[deviceType] = make(map[string]*model.Rollout)
	}
	if _, exists := rolloutsByType[deviceType][req.Name]; exists {
		s.mu.Unlock()
		http.Error(w, "rollout already exists", http.StatusConflict)
		return
	}
	rollout := &model.Rollout{
		Name:       req.Name,
		DeviceType: deviceType,
		CreatedAt:  now,
		Spec: model.RolloutSpec{
			Version:     req.Version,
			Selector:    copySelector(req.Selector),
			MaxFailures: req.MaxFailures,
		},
		Status: model.RolloutStatus{
			Generation:         1,
			ObservedGeneration: 0,
			UpdatedDevices:     make(map[string]bool),
			FailedDevices:      make(map[string]string),
			State:              "Planned",
		},
	}
	rolloutsByType[deviceType][req.Name] = rollout
	clone := cloneRollout(rollout)
	s.mu.Unlock()

	if err := json.NewEncoder(w).Encode(clone); err != nil {
		http.Error(w, "failed to encode rollout", http.StatusInternalServerError)
		return
	}
}

func (s *Server) updateRolloutStatusForType(w http.ResponseWriter, r *http.Request, deviceType types.DeviceType, name string) {
	type rolloutStatusUpdate struct {
		DeviceID   string `json:"deviceId"`
		Generation int64  `json:"generation"`
		State      string `json:"state"`
		Message    string `json:"message"`
	}

	var req rolloutStatusUpdate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.DeviceID == "" || req.State == "" {
		http.Error(w, "deviceId and state are required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	rollout, ok := rolloutsByType[deviceType][name]
	if !ok {
		http.Error(w, "rollout not found", http.StatusNotFound)
		return
	}
	if rollout.Status.UpdatedDevices == nil {
		rollout.Status.UpdatedDevices = make(map[string]bool)
	}
	if rollout.Status.FailedDevices == nil {
		rollout.Status.FailedDevices = make(map[string]string)
	}

	switch req.State {
	case "Succeeded":
		rollout.Status.UpdatedDevices[req.DeviceID] = true
	case "Failed":
		rollout.Status.FailedDevices[req.DeviceID] = req.Message
	default:
		http.Error(w, "state must be Succeeded or Failed", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) allDevicesLocked() []*model.Device {
	devices := make([]*model.Device, 0)
	for _, typed := range s.devicesByType {
		for _, dev := range typed {
			devices = append(devices, dev)
		}
	}
	return devices
}

func (s *Server) getDeviceLocked(id string) (*model.Device, bool) {
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

func (s *Server) findRolloutByGenerationLocked(id int64) (*model.Rollout, bool) {
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

func (s *Server) deviceSelectedLocked(dev *model.Device) bool {
	typed := rolloutsByType[dev.DeviceType]
	if typed == nil {
		return false
	}
	for _, rollout := range typed {
		if rollout == nil {
			continue
		}
		if rollout.Status.State != "Planned" && rollout.Status.State != "Running" {
			continue
		}
		if !deviceMatchesSelector(dev, rollout.Spec.Selector) {
			continue
		}
		if rollout.Status.UpdatedDevices != nil && rollout.Status.UpdatedDevices[dev.ID] {
			continue
		}
		if rollout.Status.FailedDevices != nil {
			if _, failed := rollout.Status.FailedDevices[dev.ID]; failed {
				continue
			}
		}
		return true
	}
	return false
}

func deviceMatchesSelector(device *model.Device, sel map[string]string) bool {
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
