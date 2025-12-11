package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	rolloutBaseURL    = "http://manager:8080/rollout"
	registerURL       = "http://manager:8080/register"
	statusURL         = "http://manager:8080/status"
	heartbeatURL      = "http://manager:8080/heartbeat"
	rolloutStatusURL  = "http://manager:8080/rolloutStatus"
	pollInterval      = 2 * time.Second
	heartbeatInterval = 5 * time.Second
)

type rolloutAssignment struct {
	GenerationID int64    `json:"generationId"`
	Version      string   `json:"version"`
	Command      []string `json:"command"`
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

	var currentGenerationID int64
	currentVersion := ""
	log.Printf("agent %s starting; registering with manager", deviceID)
	if err := registerWithManager(ctx, client, deviceID, agentVersion, deviceType, labels, capabilities); err != nil {
		log.Fatalf("failed to register: %v", err)
	}
	go sendHeartbeats(ctx, hbClient, deviceID)
	log.Printf("agent %s registered; polling manager rollouts at %s/<deviceId>", deviceID, rolloutBaseURL)

	for {
		select {
		case <-ctx.Done():
			log.Printf("agent %s shutting down", deviceID)
			return
		case <-ticker.C:
			assign, targeted, err := fetchRollout(ctx, client, deviceID)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					log.Printf("poll canceled: %v", err)
					return
				}
				log.Printf("failed to fetch rollout: %v", err)
				continue
			}
			if !targeted {
				continue
			}

			if assign.GenerationID == 0 || assign.Version == "" {
				continue
			}

			if assign.GenerationID <= currentGenerationID {
				continue
			}

			log.Printf("agent %s applying generation %d version %s (was generation %d version %s)", deviceID, assign.GenerationID, assign.Version, currentGenerationID, currentVersion)
			currentGenerationID = assign.GenerationID
			currentVersion = assign.Version

			state, message := executeCommand(ctx, assign.Command)
			report := statusReport{
				DeviceID: deviceID,
				Version:  currentVersion,
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
			if err := postRolloutStatus(ctx, client, assign.GenerationID, deviceID, rolloutState, message); err != nil {
				log.Printf("failed to post rollout status: %v", err)
			}
		}
	}
}

func fetchRollout(ctx context.Context, client *http.Client, deviceID string) (rolloutAssignment, bool, error) {
	assign := rolloutAssignment{}
	url := fmt.Sprintf("%s/%s", rolloutBaseURL, deviceID)

	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return assign, false, err
		}

		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return assign, false, ctx.Err()
			}
			backoff := time.Duration(attempt) * 300 * time.Millisecond
			log.Printf("manager unreachable (attempt %d): %v; retrying in %s", attempt, err, backoff)
			if sleepWithContext(ctx, backoff) != nil {
				return assign, false, ctx.Err()
			}
			continue
		}

		func() {
			defer resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusOK:
				err = json.NewDecoder(resp.Body).Decode(&assign)
			case http.StatusNoContent:
				// Not targeted by current selector; treat as no desired update.
				err = nil
				assign = rolloutAssignment{}
			default:
				err = fmt.Errorf("unexpected status: %s", resp.Status)
			}
		}()

		if err != nil {
			if ctx.Err() != nil {
				return assign, false, ctx.Err()
			}
			backoff := time.Duration(attempt) * 300 * time.Millisecond
			log.Printf("failed to decode rollout (attempt %d): %v; retrying in %s", attempt, err, backoff)
			if sleepWithContext(ctx, backoff) != nil {
				return assign, false, ctx.Err()
			}
			continue
		}

		if resp.StatusCode == http.StatusNoContent {
			return assign, false, nil
		}

		return assign, true, nil
	}

	return assign, false, fmt.Errorf("unable to fetch rollout after retries")
}

func executeCommand(ctx context.Context, cmdParts []string) (string, string) {
	if len(cmdParts) == 0 {
		return "error", "no command provided"
	}

	cmd := exec.CommandContext(ctx, cmdParts[0], cmdParts[1:]...)
	output, err := cmd.CombinedOutput()
	cleaned := strings.TrimSpace(string(bytes.TrimSpace(output)))

	if err != nil {
		if cleaned == "" {
			cleaned = err.Error()
		}
		return "error", cleaned
	}

	if cleaned == "" {
		cleaned = "command completed"
	}

	return "running", cleaned
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

func postRolloutStatus(ctx context.Context, client *http.Client, generationID int64, deviceID, state, message string) error {
	payload, err := json.Marshal(map[string]interface{}{
		"deviceId":     deviceID,
		"generationId": generationID,
		"state":        state,
		"message":      message,
	})
	if err != nil {
		return err
	}

	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rolloutStatusURL, bytes.NewReader(payload))
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
