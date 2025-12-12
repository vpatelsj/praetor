package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PraetorClient talks to the Praetor manager HTTP API.
type PraetorClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewPraetorClient returns a client initialized with the base URL.
func NewPraetorClient(baseURL string, httpClient *http.Client) *PraetorClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &PraetorClient{BaseURL: strings.TrimRight(baseURL, "/"), HTTPClient: httpClient}
}

// Rollout represents a type-scoped rollout resource.
type Rollout struct {
	Name       string        `json:"name"`
	DeviceType string        `json:"deviceType"`
	Spec       RolloutSpec   `json:"spec"`
	Status     RolloutStatus `json:"status"`
}

// RolloutSpec mirrors the manager API spec payload.
type RolloutSpec struct {
	Version     string            `json:"version"`
	Selector    map[string]string `json:"selector"`
	MaxFailures float64           `json:"maxFailures"`
	Command     []string          `json:"command"`
}

// RolloutStatus summarizes rollout progress.
type RolloutStatus struct {
	Generation   int64  `json:"generation"`
	State        string `json:"state"`
	Updated      int    `json:"successCount"`
	Failed       int    `json:"failureCount"`
	TotalTargets int    `json:"totalTargets"`
}

// Device represents a single device description.
type Device struct {
	ID         string            `json:"deviceId"`
	DeviceType string            `json:"deviceType"`
	Labels     map[string]string `json:"labels"`
	Online     bool              `json:"online"`
	LastSeen   time.Time         `json:"lastSeen"`
}

// CreateRolloutRequest carries create parameters.
type CreateRolloutRequest struct {
	Version     string            `json:"version"`
	Selector    map[string]string `json:"selector"`
	MaxFailures float64           `json:"maxFailures"`
	Command     []string          `json:"command"`
}

// CreateRollout creates a rollout for the given device type.
func (c *PraetorClient) CreateRollout(ctx context.Context, deviceType, name string, req CreateRolloutRequest) (*Rollout, error) {
	if name == "" {
		return nil, fmt.Errorf("rollout name is required")
	}
	payload := map[string]interface{}{
		"name":        name,
		"version":     req.Version,
		"command":     req.Command,
		"selector":    req.Selector,
		"maxFailures": req.MaxFailures,
	}
	path := fmt.Sprintf("/api/v1/devicetypes/%s/rollouts", deviceType)
	var rollout Rollout
	if err := c.do(ctx, http.MethodPost, path, payload, &rollout); err != nil {
		return nil, err
	}
	return &rollout, nil
}

// ListRollouts lists rollouts for a device type.
func (c *PraetorClient) ListRollouts(ctx context.Context, deviceType string) ([]Rollout, error) {
	path := fmt.Sprintf("/api/v1/devicetypes/%s/rollouts", deviceType)
	var rollouts []Rollout
	if err := c.do(ctx, http.MethodGet, path, nil, &rollouts); err != nil {
		return nil, err
	}
	return rollouts, nil
}

// GetRollout fetches a rollout by name.
func (c *PraetorClient) GetRollout(ctx context.Context, deviceType, name string) (*Rollout, error) {
	path := fmt.Sprintf("/api/v1/devicetypes/%s/rollouts/%s", deviceType, url.PathEscape(name))
	var rollout Rollout
	if err := c.do(ctx, http.MethodGet, path, nil, &rollout); err != nil {
		return nil, err
	}
	return &rollout, nil
}

// GetDevicesByType returns devices for a specific type.
func (c *PraetorClient) GetDevicesByType(ctx context.Context, deviceType string) ([]Device, error) {
	path := fmt.Sprintf("/api/v1/devicetypes/%s/devices", deviceType)
	var devices []Device
	if err := c.do(ctx, http.MethodGet, path, nil, &devices); err != nil {
		return nil, err
	}
	return devices, nil
}

// GetDeviceByType fetches a device by ID from the given device type list.
func (c *PraetorClient) GetDeviceByType(ctx context.Context, deviceType, id string) (*Device, error) {
	devices, err := c.GetDevicesByType(ctx, deviceType)
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
