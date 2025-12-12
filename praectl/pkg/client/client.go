package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// PraetorClient is a lightweight wrapper around the Praetor manager HTTP API.
type PraetorClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewPraetorClient constructs a client using the provided base URL and http.Client.
func NewPraetorClient(baseURL string, httpClient *http.Client) *PraetorClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &PraetorClient{BaseURL: strings.TrimRight(baseURL, "/"), HTTPClient: httpClient}
}

// CreateRolloutRequest is the payload for creating a rollout.
type CreateRolloutRequest struct {
	Version         string            `json:"version"`
	MatchLabels     map[string]string `json:"matchLabels"`
	MaxFailureRatio float64           `json:"maxFailureRatio"`
}

// RolloutSelector mirrors the manager selector payload.
type RolloutSelector struct {
	MatchLabels map[string]string `json:"matchLabels"`
}

// Rollout represents a rollout generation returned by the manager.
type Rollout struct {
	ID              int64           `json:"id"`
	Version         string          `json:"version"`
	Selector        RolloutSelector `json:"selector"`
	CreatedAt       time.Time       `json:"createdAt"`
	State           string          `json:"state"`
	TotalTargets    int             `json:"totalTargets"`
	SuccessCount    int             `json:"successCount"`
	FailureCount    int             `json:"failureCount"`
	MaxFailureRatio float64         `json:"maxFailureRatio"`
}

// GenerationID returns the rollout identifier as a string for display purposes.
func (r Rollout) GenerationID() string {
	if r.ID == 0 {
		return ""
	}
	return strconv.FormatInt(r.ID, 10)
}

// MatchLabels exposes the selector map (guaranteed non-nil).
func (r Rollout) MatchLabels() map[string]string {
	if r.Selector.MatchLabels == nil {
		return map[string]string{}
	}
	return r.Selector.MatchLabels
}

// Pending derives remaining devices.
func (r Rollout) Pending() int {
	pending := r.TotalTargets - r.SuccessCount - r.FailureCount
	if pending < 0 {
		return 0
	}
	return pending
}

// IsTerminal reports whether the rollout has finished running.
func (r Rollout) IsTerminal() bool {
	switch strings.ToLower(r.State) {
	case "succeeded", "failed", "paused":
		return true
	default:
		return false
	}
}

// Device is an aggregated view combining registration metadata and latest status.
type Device struct {
	ID           string
	Type         string
	Online       bool
	Selected     bool
	AgentVersion string
	Version      string
	State        string
	Message      string
	Labels       map[string]string
	Capabilities []string
	RegisteredAt *time.Time
	LastSeen     *time.Time
}

type registeredDeviceResponse struct {
	DeviceID     string            `json:"deviceId"`
	AgentVersion string            `json:"agentVersion"`
	DeviceType   string            `json:"deviceType"`
	Labels       map[string]string `json:"labels"`
	Capabilities []string          `json:"capabilities"`
	RegisteredAt time.Time         `json:"registeredAt"`
	LastSeen     time.Time         `json:"lastSeen"`
	Online       bool              `json:"online"`
	Selected     bool              `json:"selected"`
}

type deviceStatusResponse struct {
	DeviceID     string            `json:"deviceId"`
	Version      string            `json:"version"`
	State        string            `json:"state"`
	Message      string            `json:"message"`
	DeviceType   string            `json:"deviceType"`
	Labels       map[string]string `json:"labels"`
	Capabilities []string          `json:"capabilities"`
	Online       bool              `json:"online"`
	Selected     bool              `json:"selected"`
}

// CreateRollout calls the manager's rollout creation endpoint.
func (c *PraetorClient) CreateRollout(ctx context.Context, payload CreateRolloutRequest) (*Rollout, error) {
	var rollout Rollout
	if err := c.do(ctx, http.MethodPost, "/rollout", payload, &rollout); err != nil {
		return nil, err
	}
	return &rollout, nil
}

// ListRollouts returns rollout generations.
func (c *PraetorClient) ListRollouts(ctx context.Context) ([]Rollout, error) {
	var rollouts []Rollout
	if err := c.do(ctx, http.MethodGet, "/rollout", nil, &rollouts); err != nil {
		return nil, err
	}
	return rollouts, nil
}

// GetRollout returns a single rollout generation by ID by scanning the ListRollouts result.
func (c *PraetorClient) GetRollout(ctx context.Context, generationID string) (*Rollout, error) {
	if generationID == "" {
		return nil, fmt.Errorf("generation id cannot be empty")
	}
	id, err := strconv.ParseInt(generationID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid generation id %q: %w", generationID, err)
	}

	rollouts, err := c.ListRollouts(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rollouts {
		if rollouts[i].ID == id {
			return &rollouts[i], nil
		}
	}
	return nil, fmt.Errorf("generation %s not found", generationID)
}

// GetDevices retrieves the fleet of managed devices combining metadata and status.
func (c *PraetorClient) GetDevices(ctx context.Context) ([]Device, error) {
	registered, err := c.listRegisteredDevices(ctx)
	if err != nil {
		return nil, err
	}
	statuses, err := c.listDeviceStatuses(ctx)
	if err != nil {
		return nil, err
	}

	statusMap := make(map[string]deviceStatusResponse, len(statuses))
	for _, st := range statuses {
		statusMap[st.DeviceID] = st
	}

	devices := make([]Device, 0, len(registered))
	for _, reg := range registered {
		st := statusMap[reg.DeviceID]
		devices = append(devices, Device{
			ID:           reg.DeviceID,
			Type:         reg.DeviceType,
			Online:       reg.Online,
			Selected:     reg.Selected,
			AgentVersion: reg.AgentVersion,
			Version:      st.Version,
			State:        st.State,
			Message:      st.Message,
			Labels:       chooseMap(st.Labels, reg.Labels),
			Capabilities: chooseSlice(st.Capabilities, reg.Capabilities),
			RegisteredAt: toTimePtr(reg.RegisteredAt),
			LastSeen:     toTimePtr(reg.LastSeen),
		})
	}

	return devices, nil
}

// GetDevice returns a single device by its identifier.
func (c *PraetorClient) GetDevice(ctx context.Context, id string) (*Device, error) {
	devices, err := c.GetDevices(ctx)
	if err != nil {
		return nil, err
	}
	for i := range devices {
		if devices[i].ID == id {
			return &devices[i], nil
		}
	}
	return nil, fmt.Errorf("device %s not found", id)
}

func (c *PraetorClient) listRegisteredDevices(ctx context.Context) ([]registeredDeviceResponse, error) {
	var devices []registeredDeviceResponse
	if err := c.do(ctx, http.MethodGet, "/devices/registered", nil, &devices); err != nil {
		return nil, err
	}
	return devices, nil
}

func (c *PraetorClient) listDeviceStatuses(ctx context.Context) ([]deviceStatusResponse, error) {
	var devices []deviceStatusResponse
	if err := c.do(ctx, http.MethodGet, "/devices", nil, &devices); err != nil {
		return nil, err
	}
	return devices, nil
}

func chooseMap(primary, fallback map[string]string) map[string]string {
	if len(primary) > 0 {
		return primary
	}
	return fallback
}

func chooseSlice(primary, fallback []string) []string {
	if len(primary) > 0 {
		return primary
	}
	return fallback
}

func toTimePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	tt := t
	return &tt
}

func (c *PraetorClient) do(ctx context.Context, method, path string, payload interface{}, out interface{}) error {
	url := c.BaseURL + path

	var body io.Reader
	if payload != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(payload); err != nil {
			return err
		}
		body = buf
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("praetor manager error: %s", strings.TrimSpace(string(data)))
	}

	if out == nil {
		return nil
	}

	return json.NewDecoder(resp.Body).Decode(out)
}
