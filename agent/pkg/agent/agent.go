package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultPollInterval      = 5 * time.Second
	defaultHeartbeatInterval = 5 * time.Second
)

// Agent encapsulates the shared logic for Praetor agents.
type Agent struct {
	deviceID   string
	deviceType string
	labels     map[string]string
	managerURL *url.URL

	httpClient      *http.Client
	pollInterval    time.Duration
	heartbeatTicker time.Duration
	localGenerations map[string]int64

	logger *log.Logger
}

// New creates a new Agent instance.
func New(deviceID, deviceType, managerAddr string, logger *log.Logger) (*Agent, error) {
	if deviceID == "" {
		return nil, fmt.Errorf("deviceID is required")
	}
	if deviceType == "" {
		return nil, fmt.Errorf("deviceType is required")
	}
	if managerAddr == "" {
		managerAddr = "http://localhost:8080"
	}
	parsed, err := url.Parse(managerAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid manager address: %w", err)
	}
	if logger == nil {
		logger = log.Default()
	}

	return &Agent{
		deviceID:        deviceID,
		deviceType:      strings.ToLower(deviceType),
		labels: map[string]string{
			"role": strings.ToLower(deviceType),
		},
		managerURL:      parsed,
		httpClient:      &http.Client{Timeout: 10 * time.Second},
		pollInterval:    defaultPollInterval,
		heartbeatTicker: defaultHeartbeatInterval,
		localGenerations: make(map[string]int64),
		logger:          logger,
	}, nil
}

// Start runs the registration, heartbeat, and rollout polling loops.
func (a *Agent) Start(ctx context.Context) error {
	if err := a.register(ctx); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	a.logger.Printf("registered device %s (%s) with manager %s", a.deviceID, a.deviceType, a.managerURL)

	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go a.heartbeatLoop(hbCtx)

	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := a.pollRollouts(ctx); err != nil {
				a.logger.Printf("rollout poll failed: %v", err)
			}
		}
	}
}

func (a *Agent) register(ctx context.Context) error {
	payload := map[string]interface{}{
		"deviceId":   a.deviceID,
		"deviceType": a.deviceType,
		"labels":      a.labels,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.managerURL.ResolveReference(&url.URL{Path: "/register"}).String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("registration failed: %s", resp.Status)
	}
	return nil
}

func (a *Agent) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(a.heartbeatTicker)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.sendHeartbeat(ctx); err != nil {
				a.logger.Printf("heartbeat error: %v", err)
			}
		}
	}
}

func (a *Agent) sendHeartbeat(ctx context.Context) error {
	payload := map[string]string{"deviceId": a.deviceID}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	hbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(hbCtx, http.MethodPost, a.managerURL.ResolveReference(&url.URL{Path: "/heartbeat"}).String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("heartbeat failed: %s", resp.Status)
	}
	return nil
}

func (a *Agent) pollRollouts(ctx context.Context) error {
	path := fmt.Sprintf("/api/v1/devicetypes/%s/rollouts", a.deviceType)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.managerURL.ResolveReference(&url.URL{Path: path}).String(), nil)
	if err != nil {
		return err
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("rollout list failed: %s", resp.Status)
	}

	var rollouts []Rollout
	if err := json.NewDecoder(resp.Body).Decode(&rollouts); err != nil {
		return fmt.Errorf("decode rollouts: %w", err)
	}

	for _, r := range rollouts {
		if !strings.EqualFold(r.Status.State, "running") {
			continue
		}
		if !matchesSelector(a.labels, r.Spec.Selector) {
			continue
		}
		if r.Status.Generation <= a.localGenerations[r.Name] {
			continue
		}
		a.logger.Printf("executing rollout name=%s generation=%d version=%s", r.Name, r.Status.Generation, r.Spec.Version)
		state, message := a.executeRollout(ctx, r)
		if err := a.reportRolloutStatus(ctx, r.Name, r.Status.Generation, state, message); err != nil {
			a.logger.Printf("report status failed for rollout %s: %v", r.Name, err)
		}
		if state == "Succeeded" {
			a.localGenerations[r.Name] = r.Status.Generation
		}
	}
	return nil
}

func (a *Agent) executeRollout(ctx context.Context, r Rollout) (string, string) {
	cmdParts := r.Command
	if len(cmdParts) == 0 {
		cmdParts = r.Spec.Command
	}
	if len(cmdParts) == 0 {
		cmdParts = []string{"echo", fmt.Sprintf("Applying version %s", r.Spec.Version)}
	}

	cmd := exec.CommandContext(ctx, cmdParts[0], cmdParts[1:]...)
	output, err := cmd.CombinedOutput()
	message := strings.TrimSpace(string(output))
	if message == "" && err != nil {
		message = err.Error()
	}
	if err != nil {
		return "Failed", message
	}
	if message == "" {
		message = "command completed"
	}
	return "Succeeded", message
}

func (a *Agent) reportRolloutStatus(ctx context.Context, rolloutName string, generation int64, state, message string) error {
	payload := map[string]interface{}{
		"deviceId":   a.deviceID,
		"generation": generation,
		"state":      state,
		"message":    message,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/api/v1/devicetypes/%s/rollouts/%s/status", a.deviceType, rolloutName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.managerURL.ResolveReference(&url.URL{Path: path}).String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("rollout status update failed: %s", resp.Status)
	}
	return nil
}

// Rollout mirrors the manager's rollout JSON structure.
type Rollout struct {
	Name       string        `json:"name"`
	DeviceType string        `json:"deviceType"`
	Command    []string      `json:"command"`
	Spec       RolloutSpec   `json:"spec"`
	Status     RolloutStatus `json:"status"`
}

type RolloutSpec struct {
	Version     string            `json:"version"`
	Command     []string          `json:"command"`
	Selector    map[string]string `json:"selector"`
	MaxFailures float64           `json:"maxFailures"`
}

type RolloutStatus struct {
	Generation   int64  `json:"generation"`
	State        string `json:"state"`
	SuccessCount int    `json:"successCount"`
	FailureCount int    `json:"failureCount"`
	TotalTargets int    `json:"totalTargets"`
}

func matchesSelector(labels, selector map[string]string) bool {
	if len(selector) == 0 {
		return true
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}
