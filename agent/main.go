package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	managerBase       = "http://manager:8080"
	registerURL       = managerBase + "/register"
	statusURL         = managerBase + "/status"
	heartbeatURL      = managerBase + "/heartbeat"
	pollInterval      = 2 * time.Second
	heartbeatInterval = 5 * time.Second
)

type rolloutResource struct {
	Name       string `json:"name"`
	DeviceType string `json:"deviceType"`
	Spec       struct {
		Version     string            `json:"version"`
		Selector    map[string]string `json:"selector"`
		MaxFailures float64           `json:"maxFailures"`
	} `json:"spec"`
	Status struct {
		Generation     int64             `json:"generation"`
		UpdatedDevices map[string]bool   `json:"updatedDevices"`
		FailedDevices  map[string]string `json:"failedDevices"`
		State          string            `json:"state"`
	} `json:"status"`
}

type statusReport struct {
	DeviceID string `json:"deviceId"`
	Version  string `json:"version"`
	State    string `json:"state"`
	Message  string `json:"message"`
}

func main() {
	deviceID := os.Getenv("DEVICE_ID")
	if deviceID == "" {
		log.Fatal("DEVICE_ID is required")
	}
	agentVersion := getenvDefault("AGENT_VERSION", "1.0.0")
	deviceType := getenvDefault("DEVICE_TYPE", "Simulator")
	labels := map[string]string{
		"rack": "demo",
		"role": defaultRole(deviceType),
	}
	capabilities := defaultCapabilities(deviceType)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &http.Client{Timeout: 5 * time.Second}
	hbClient := &http.Client{Timeout: 1 * time.Second}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	applied := map[string]int64{}
	log.Printf("agent %s starting; registering with manager", deviceID)
	if err := registerWithManager(ctx, client, deviceID, agentVersion, deviceType, labels, capabilities); err != nil {
		log.Fatalf("failed to register: %v", err)
	}
	go sendHeartbeats(ctx, hbClient, deviceID)
	log.Printf("agent %s registered; watching rollouts at %s", deviceID, rolloutsURL(deviceType))

	for {
		select {
		case <-ctx.Done():
			log.Printf("agent %s shutting down", deviceID)
			return
		case <-ticker.C:
			rollout, targeted, err := fetchPendingRollout(ctx, client, deviceID, deviceType, labels, applied)
			if err != nil {
				log.Printf("failed to fetch rollout: %v", err)
				continue
			}
			if !targeted {
				continue
			}

			if rollout.Status.Generation == 0 || rollout.Spec.Version == "" {
				continue
			}

			log.Printf("agent %s applying rollout %s generation %d version %s", deviceID, rollout.Name, rollout.Status.Generation, rollout.Spec.Version)

			state, message := applyRollout(ctx, rollout)
			report := statusReport{
				DeviceID: deviceID,
				Version:  rollout.Spec.Version,
				State:    state,
				Message:  message,
			}

			if err := postStatus(ctx, client, report); err != nil {
				log.Printf("failed to post status: %v", err)
			}

			rolloutState := "Succeeded"
			if state == "error" {
				rolloutState = "Failed"
			}
			if err := postRolloutStatus(ctx, client, deviceType, rollout, deviceID, rolloutState, message); err != nil {
				log.Printf("failed to post rollout status: %v", err)
			}

			applied[rollout.Name] = rollout.Status.Generation
		}
	}
}

func fetchPendingRollout(ctx context.Context, client *http.Client, deviceID, deviceType string, labels map[string]string, applied map[string]int64) (*rolloutResource, bool, error) {
	url := rolloutsURL(deviceType)
	var rollout rolloutResource

	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, false, err
		}

		resp, err := client.Do(req)
		if err != nil {
			backoff := time.Duration(attempt) * 300 * time.Millisecond
			log.Printf("manager unreachable (attempt %d): %v; retrying in %s", attempt, err, backoff)
			if sleepWithContext(ctx, backoff) != nil {
				return nil, false, ctx.Err()
			}
			continue
		}

		func() {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("unexpected status: %s", resp.Status)
				return
			}

			var rollouts []rolloutResource
			if decodeErr := json.NewDecoder(resp.Body).Decode(&rollouts); decodeErr != nil {
				err = decodeErr
				return
			}

			for _, ro := range rollouts {
				if ro.Status.State != "Planned" && ro.Status.State != "Running" {
					continue
				}
				if !matchesSelector(labels, ro.Spec.Selector) {
					continue
				}
				if ro.Status.UpdatedDevices != nil && ro.Status.UpdatedDevices[deviceID] {
					continue
				}
				if ro.Status.FailedDevices != nil {
					if _, failed := ro.Status.FailedDevices[deviceID]; failed {
						continue
					}
				}
				if appliedGen, ok := applied[ro.Name]; ok && ro.Status.Generation <= appliedGen {
					continue
				}
				rollout = ro
				err = nil
				return
			}
			err = nil
		}()

		if err != nil {
			backoff := time.Duration(attempt) * 300 * time.Millisecond
			log.Printf("failed to decode rollout (attempt %d): %v; retrying in %s", attempt, err, backoff)
			if sleepWithContext(ctx, backoff) != nil {
				return nil, false, ctx.Err()
			}
			continue
		}

		if rollout.Name == "" {
			return nil, false, nil
		}

		return &rollout, true, nil
	}

	return nil, false, fmt.Errorf("unable to fetch rollout after retries")
}

func rolloutsURL(deviceType string) string {
	return fmt.Sprintf("%s/api/v1/devicetypes/%s/rollouts", managerBase, strings.ToLower(deviceType))
}

func rolloutStatusURL(deviceType, name string) string {
	return fmt.Sprintf("%s/api/v1/devicetypes/%s/rollouts/%s/status", managerBase, strings.ToLower(deviceType), url.PathEscape(name))
}

func applyRollout(_ context.Context, rollout *rolloutResource) (string, string) {
	return "running", fmt.Sprintf("applied version %s", rollout.Spec.Version)
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

func postStatus(ctx context.Context, client *http.Client, report statusReport) error {
	payload, err := json.Marshal(report)
	if err != nil {
		return err
	}

	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, statusURL, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
		}

		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		backoff := time.Duration(attempt) * 300 * time.Millisecond
		if err != nil {
			log.Printf("post status failed (attempt %d): %v; retrying in %s", attempt, err, backoff)
		} else {
			log.Printf("post status returned %s (attempt %d); retrying in %s", resp.Status, attempt, backoff)
		}

		if sleepWithContext(ctx, backoff) != nil {
			return ctx.Err()
		}
	}

	return fmt.Errorf("unable to post status after retries")
}

func postRolloutStatus(ctx context.Context, client *http.Client, deviceType string, rollout *rolloutResource, deviceID, state, message string) error {
	payload, err := json.Marshal(map[string]interface{}{
		"deviceId":   deviceID,
		"generation": rollout.Status.Generation,
		"state":      state,
		"message":    message,
	})
	if err != nil {
		return err
	}

	url := rolloutStatusURL(deviceType, rollout.Name)
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err == nil && resp != nil {
			resp.Body.Close()
		}

		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		backoff := time.Duration(attempt) * 300 * time.Millisecond
		if err != nil {
			log.Printf("post rollout status failed (attempt %d): %v; retrying in %s", attempt, err, backoff)
		} else {
			log.Printf("post rollout status returned %s (attempt %d); retrying in %s", resp.Status, attempt, backoff)
		}

		if sleepWithContext(ctx, backoff) != nil {
			return ctx.Err()
		}
	}

	return fmt.Errorf("unable to post rollout status after retries")
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func registerWithManager(ctx context.Context, client *http.Client, deviceID, agentVersion, deviceType string, labels map[string]string, capabilities []string) error {
	payload, err := json.Marshal(map[string]interface{}{
		"deviceId":     deviceID,
		"agentVersion": agentVersion,
		"deviceType":   deviceType,
		"labels":       labels,
		"capabilities": capabilities,
	})
	if err != nil {
		return err
	}

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, registerURL, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err == nil && resp != nil {
			resp.Body.Close()
		}

		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		backoff := time.Duration(1<<attempt) * 500 * time.Millisecond
		if backoff > 5*time.Second {
			backoff = 5 * time.Second
		}
		if err != nil {
			log.Printf("registration failed (attempt %d): %v; retrying in %s", attempt+1, err, backoff)
		} else {
			log.Printf("registration returned %s (attempt %d); retrying in %s", resp.Status, attempt+1, backoff)
		}

		if sleepWithContext(ctx, backoff) != nil {
			return ctx.Err()
		}
	}
}

func sendHeartbeats(ctx context.Context, client *http.Client, deviceID string) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := postHeartbeat(ctx, client, deviceID); err != nil {
				log.Printf("heartbeat failed: %v", err)
			}
		}
	}
}

func postHeartbeat(ctx context.Context, client *http.Client, deviceID string) error {
	payload, err := json.Marshal(map[string]string{"deviceId": deviceID})
	if err != nil {
		return err
	}

	hbCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(hbCtx, http.MethodPost, heartbeatURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("heartbeat returned %s", resp.Status)
}

func getenvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func defaultCapabilities(deviceType string) []string {
	switch deviceType {
	case "Server", "DPU":
		return []string{"systemd", "raw-binary"}
	case "NetworkSwitch", "SOC":
		return []string{"systemd", "raw-binary"}
	case "BMC":
		return []string{"initd", "raw-binary"}
	case "Simulator":
		return []string{"systemd", "raw-binary"}
	default:
		return []string{"systemd", "raw-binary"}
	}
}

func defaultRole(deviceType string) string {
	switch deviceType {
	case "Simulator":
		return "sim"
	case "NetworkSwitch":
		return "switch"
	case "Server":
		return "server"
	case "DPU":
		return "dpu"
	case "SOC":
		return "soc"
	case "BMC":
		return "bmc"
	default:
		return strings.ToLower(deviceType)
	}
}
