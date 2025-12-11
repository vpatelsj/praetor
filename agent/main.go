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
	desiredURL   = "http://manager:8080/desired"
	statusURL    = "http://manager:8080/status"
	pollInterval = 2 * time.Second
)

type desiredState struct {
	Version string   `json:"version"`
	Command []string `json:"command"`
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &http.Client{Timeout: 5 * time.Second}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	currentVersion := ""
	log.Printf("agent %s starting; polling manager at %s", deviceID, desiredURL)

	for {
		select {
		case <-ctx.Done():
			log.Printf("agent %s shutting down", deviceID)
			return
		case <-ticker.C:
			desired, err := fetchDesired(ctx, client)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					log.Printf("poll canceled: %v", err)
					return
				}
				log.Printf("failed to fetch desired state: %v", err)
				continue
			}

			if desired.Version == "" {
				log.Printf("desired state missing version; skipping")
				continue
			}

			if desired.Version == currentVersion {
				continue
			}

			log.Printf("agent %s updating to version %s (was %s)", deviceID, desired.Version, currentVersion)
			currentVersion = desired.Version

			state, message := executeCommand(ctx, desired.Command)
			report := statusReport{
				DeviceID: deviceID,
				Version:  currentVersion,
				State:    state,
				Message:  message,
			}

			if err := postStatus(ctx, client, report); err != nil {
				log.Printf("failed to post status: %v", err)
			}
		}
	}
}

func fetchDesired(ctx context.Context, client *http.Client) (desiredState, error) {
	var ds desiredState

	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, desiredURL, nil)
		if err != nil {
			return ds, err
		}

		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ds, ctx.Err()
			}
			backoff := time.Duration(attempt) * 300 * time.Millisecond
			log.Printf("manager unreachable (attempt %d): %v; retrying in %s", attempt, err, backoff)
			if sleepWithContext(ctx, backoff) != nil {
				return ds, ctx.Err()
			}
			continue
		}

		func() {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("unexpected status: %s", resp.Status)
				return
			}
			err = json.NewDecoder(resp.Body).Decode(&ds)
		}()

		if err != nil {
			if ctx.Err() != nil {
				return ds, ctx.Err()
			}
			backoff := time.Duration(attempt) * 300 * time.Millisecond
			log.Printf("failed to decode desired state (attempt %d): %v; retrying in %s", attempt, err, backoff)
			if sleepWithContext(ctx, backoff) != nil {
				return ds, ctx.Err()
			}
			continue
		}

		return ds, nil
	}

	return ds, fmt.Errorf("unable to fetch desired state after retries")
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
