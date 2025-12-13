package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/apollo/praetor/gateway"
	"github.com/apollo/praetor/pkg/log"
	"github.com/apollo/praetor/pkg/version"
	"github.com/go-logr/logr"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	defaultHeartbeatSeconds = 15
	maxBackoff              = 30 * time.Second
)

type agent struct {
	deviceName        string
	gatewayURL        string
	deviceToken       string
	deviceTokenSecret string
	client            *http.Client
	logger            logr.Logger
	lastETag          string
	lastObserved      map[string]string
	heartbeat         time.Duration
	rnd               *rand.Rand
}

func main() {
	var deviceName string
	var gatewayURL string
	var deviceToken string
	var deviceTokenSecret string

	flag.StringVar(&deviceName, "device-name", getenv("APOLLO_DEVICE_NAME", ""), "Device identifier (env: APOLLO_DEVICE_NAME)")
	flag.StringVar(&gatewayURL, "gateway-url", getenv("APOLLO_GATEWAY_URL", ""), "Gateway base URL (env: APOLLO_GATEWAY_URL)")
	flag.StringVar(&deviceToken, "device-token", getenv("APOLLO_DEVICE_TOKEN", ""), "Shared device token (env: APOLLO_DEVICE_TOKEN)")
	flag.StringVar(&deviceTokenSecret, "device-token-secret", getenv("APOLLO_DEVICE_TOKEN_SECRET", ""), "HMAC secret for device-bound token (env: APOLLO_DEVICE_TOKEN_SECRET)")

	log.Setup()
	flag.Parse()

	logger := ctrllog.Log.WithName("agent")

	if deviceName == "" {
		logger.Error(fmt.Errorf("missing device name"), "set --device-name or APOLLO_DEVICE_NAME")
		os.Exit(1)
	}
	if gatewayURL == "" {
		logger.Error(fmt.Errorf("missing gateway URL"), "set --gateway-url or APOLLO_GATEWAY_URL")
		os.Exit(1)
	}

	ag := &agent{
		deviceName:        deviceName,
		gatewayURL:        strings.TrimSuffix(gatewayURL, "/"),
		deviceToken:       strings.TrimSpace(deviceToken),
		deviceTokenSecret: strings.TrimSpace(deviceTokenSecret),
		client:            &http.Client{Timeout: 10 * time.Second},
		logger:            logger,
		lastObserved:      make(map[string]string),
		heartbeat:         time.Duration(defaultHeartbeatSeconds) * time.Second,
		rnd:               rand.New(rand.NewSource(time.Now().UnixNano())),
	}

	logger.Info("agent starting", "device", deviceName, "gateway", gatewayURL, "version", version.Version, "commit", version.Commit)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ag.run(ctx); err != nil {
		logger.Error(err, "agent stopped")
		os.Exit(1)
	}
}

func (a *agent) run(ctx context.Context) error {
	if err := a.pollDesired(ctx); err != nil {
		return err
	}

	desiredTicker := time.NewTicker(5 * time.Second)
	heartbeatTicker := time.NewTicker(a.heartbeat)
	defer desiredTicker.Stop()
	defer heartbeatTicker.Stop()

	backoff := 2 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-desiredTicker.C:
			if err := a.pollDesired(ctx); err != nil {
				a.logger.Error(err, "poll desired failed")
				a.sleepWithJitter(ctx, backoff)
				backoff = nextBackoff(backoff)
				continue
			}
			backoff = 2 * time.Second
		case <-heartbeatTicker.C:
			if err := a.sendReport(ctx, nil); err != nil {
				a.logger.Error(err, "heartbeat report failed")
				a.sleepWithJitter(ctx, backoff)
				backoff = nextBackoff(backoff)
				continue
			}
			backoff = 2 * time.Second
		}

		// Adjust heartbeat ticker if desired interval changed.
		heartbeatTicker.Reset(a.heartbeat)
	}
}

func (a *agent) pollDesired(ctx context.Context) error {
	desired, notModified, etag, err := a.fetchDesired(ctx)
	if err != nil {
		return err
	}
	if etag != "" {
		a.lastETag = etag
	}

	if !notModified && desired != nil && desired.HeartbeatIntervalSeconds > 0 {
		a.heartbeat = time.Duration(desired.HeartbeatIntervalSeconds) * time.Second
	}

	obs := a.computeObservations(desired, notModified)
	if len(obs) == 0 {
		return nil
	}
	return a.sendReport(ctx, obs)
}

func (a *agent) fetchDesired(ctx context.Context) (*gateway.DesiredResponse, bool, string, error) {
	url := fmt.Sprintf("%s/v1/devices/%s/desired", a.gatewayURL, a.deviceName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, a.lastETag, err
	}
	if a.lastETag != "" {
		req.Header.Set("If-None-Match", a.lastETag)
	}
	if token := a.computeDeviceToken(); token != "" {
		req.Header.Set("X-Device-Token", token)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, false, a.lastETag, err
	}
	defer resp.Body.Close()

	etag := strings.TrimSpace(resp.Header.Get("ETag"))
	if etag == "" {
		etag = a.lastETag
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var desired gateway.DesiredResponse
		if err := json.NewDecoder(resp.Body).Decode(&desired); err != nil {
			return nil, false, etag, err
		}
		return &desired, false, etag, nil
	case http.StatusNotModified:
		return nil, true, etag, nil
	default:
		return nil, false, etag, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
}

func (a *agent) computeObservations(desired *gateway.DesiredResponse, notModified bool) []gateway.Observation {
	if notModified {
		return nil
	}
	if desired == nil {
		return nil
	}

	obs := make([]gateway.Observation, 0, len(desired.Items))
	for i := range desired.Items {
		item := desired.Items[i]
		lastHash := a.lastObserved[itemKey(item.Namespace, item.Name)]
		if lastHash == item.SpecHash {
			continue
		}
		cmd := strings.Join(append(item.Spec.Execution.Command, item.Spec.Execution.Args...), " ")
		a.logger.Info("desired command", "device", a.deviceName, "target", item.Name, "command", cmd)

		obs = append(obs, gateway.Observation{
			Namespace:        item.Namespace,
			Name:             item.Name,
			ObservedSpecHash: item.SpecHash,
			ProcessStarted:   boolPtr(true),
			Healthy:          boolPtr(true),
		})
	}
	return obs
}

func (a *agent) sendReport(ctx context.Context, observations []gateway.Observation) error {
	url := fmt.Sprintf("%s/v1/devices/%s/report", a.gatewayURL, a.deviceName)
	reqBody := gateway.ReportRequest{
		AgentVersion: version.Version,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		Heartbeat:    true,
		Observations: observations,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := a.computeDeviceToken(); token != "" {
		req.Header.Set("X-Device-Token", token)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("report failed with status %d", resp.StatusCode)
	}

	for i := range observations {
		obs := observations[i]
		a.lastObserved[itemKey(obs.Namespace, obs.Name)] = obs.ObservedSpecHash
	}
	return nil
}

func boolPtr(v bool) *bool {
	return &v
}

func itemKey(ns, name string) string {
	return ns + "/" + name
}

func (a *agent) computeDeviceToken() string {
	if a.deviceTokenSecret != "" {
		h := hmac.New(func() hash.Hash { return sha256.New() }, []byte(a.deviceTokenSecret))
		h.Write([]byte(a.deviceName))
		return hex.EncodeToString(h.Sum(nil))
	}
	return a.deviceToken
}

func (a *agent) sleepWithJitter(ctx context.Context, base time.Duration) {
	jitter := time.Duration(a.rnd.Int63n(int64(250 * time.Millisecond)))
	d := base + jitter
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

func nextBackoff(current time.Duration) time.Duration {
	n := current * 2
	if n > maxBackoff {
		return maxBackoff
	}
	return n
}

func getenv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
