package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/apollo/praetor/agent/systemd"
	apiv1alpha1 "github.com/apollo/praetor/api/azure.com/v1alpha1"
	"github.com/apollo/praetor/gateway"
)

type fakeOCI struct {
	results []ociResult
	errs    []error
	calls   int
}

func (f *fakeOCI) Ensure(_ context.Context, _ string) (ociResult, error) {
	idx := f.calls
	f.calls++
	if idx < len(f.results) {
		return f.results[idx], f.errs[idx]
	}
	return ociResult{}, errors.New("unexpected oci call")
}

type noopRunner struct{}

func (n *noopRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return nil, nil
}

func TestReconcileArtifactFailureCarriesReasons(t *testing.T) {
	restoreRunner := systemd.SetRunnerForTesting(&noopRunner{})
	defer restoreRunner()
	restorePaths := systemd.SetBasePathsForTesting(t.TempDir(), filepath.Join(t.TempDir(), "env"))
	defer restorePaths()

	res := ociResult{
		digest:          "sha256:" + strings.Repeat("a", 64),
		downloaded:      true,
		verified:        false,
		attempts:        1,
		lastAttemptTime: time.Date(2025, 12, 15, 1, 2, 3, 0, time.UTC).Format(time.RFC3339),
		lastError:       "pull failed",
		downloadReason:  "FetchFailed",
		downloadMessage: "pull failed",
		verifyReason:    "DigestMismatch",
		verifyMessage:   "digest mismatch",
	}

	a := &agent{
		logger:    logr.Discard(),
		managed:   make(map[string]managedItem),
		statePath: filepath.Join(t.TempDir(), "state.json"),
		oci:       &fakeOCI{results: []ociResult{res}, errs: []error{errors.New("pull failed")}},
		client:    &http.Client{Timeout: 2 * time.Second},
	}

	desired := &gateway.DesiredResponse{Items: []gateway.DesiredItem{{
		Namespace: "ns",
		Name:      "proc",
		SpecHash:  "hash",
		Spec: apiv1alpha1.DeviceProcessSpec{
			Artifact: apiv1alpha1.DeviceProcessArtifact{Type: apiv1alpha1.ArtifactTypeOCI, URL: "ghcr.io/app@sha256:" + strings.Repeat("a", 64)},
			Execution: apiv1alpha1.DeviceProcessExecution{
				Backend: apiv1alpha1.DeviceProcessBackendSystemd,
				Command: []string{"bin/app"},
			},
		},
	}}}

	obs, err := a.reconcile(context.Background(), desired)
	if err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("expected one observation, got %d", len(obs))
	}
	o := obs[0]
	if got := derefBool(o.ArtifactDownloaded); !got {
		t.Fatalf("expected ArtifactDownloaded true")
	}
	if got := derefBool(o.ArtifactVerified); got {
		t.Fatalf("expected ArtifactVerified false")
	}
	if o.ArtifactDownloadReason != res.downloadReason {
		t.Fatalf("download reason mismatch: %q", o.ArtifactDownloadReason)
	}
	if o.ArtifactDownloadMessage != res.downloadMessage {
		t.Fatalf("download message mismatch: %q", o.ArtifactDownloadMessage)
	}
	if o.ArtifactVerifyReason != res.verifyReason {
		t.Fatalf("verify reason mismatch: %q", o.ArtifactVerifyReason)
	}
	if o.ArtifactVerifyMessage != res.verifyMessage {
		t.Fatalf("verify message mismatch: %q", o.ArtifactVerifyMessage)
	}

	req := gateway.ReportRequest{AgentVersion: "test", Timestamp: time.Now().UTC().Format(time.RFC3339), Heartbeat: true, Observations: obs}
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(payload)
	if !strings.Contains(body, res.downloadReason) || !strings.Contains(body, res.verifyReason) {
		t.Fatalf("expected reasons in payload, got %s", body)
	}
	if !strings.Contains(body, res.downloadMessage) || !strings.Contains(body, res.verifyMessage) {
		t.Fatalf("expected messages in payload, got %s", body)
	}
}

func TestReconcileNonOCIResetsArtifactFields(t *testing.T) {
	restoreRunner := systemd.SetRunnerForTesting(&noopRunner{})
	defer restoreRunner()
	restorePaths := systemd.SetBasePathsForTesting(t.TempDir(), filepath.Join(t.TempDir(), "env"))
	defer restorePaths()

	rootfs := t.TempDir()
	a := &agent{
		logger:    logr.Discard(),
		managed:   make(map[string]managedItem),
		statePath: filepath.Join(t.TempDir(), "state.json"),
		oci: &fakeOCI{results: []ociResult{{
			rootfsPath:     rootfs,
			digest:         "sha256:" + strings.Repeat("b", 64),
			downloaded:     true,
			verified:       true,
			downloadReason: "ArtifactDownloaded",
			verifyReason:   "ArtifactVerified",
		}}, errs: []error{nil}},
		client: &http.Client{Timeout: 2 * time.Second},
	}

	desired := &gateway.DesiredResponse{Items: []gateway.DesiredItem{}}
	// First reconcile with OCI to populate fields/state.
	desired.Items = []gateway.DesiredItem{dispatchItem(apiv1alpha1.ArtifactTypeOCI, []string{"bin/app"})}
	if _, err := a.reconcile(context.Background(), desired); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	// Switch to non-OCI and ensure fields are cleared/not applicable.
	desired.Items = []gateway.DesiredItem{dispatchItem(apiv1alpha1.ArtifactTypeLocal, []string{"/usr/bin/app"})}
	obs, err := a.reconcile(context.Background(), desired)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("expected one observation")
	}
	o := obs[0]
	if o.ArtifactDigest != "" || o.ArtifactLastError != "" {
		t.Fatalf("expected cleared artifact fields, got digest=%q lastError=%q", o.ArtifactDigest, o.ArtifactLastError)
	}
	if got := derefBool(o.ArtifactDownloaded); got {
		t.Fatalf("expected ArtifactDownloaded false")
	}
	if got := derefBool(o.ArtifactVerified); got {
		t.Fatalf("expected ArtifactVerified false")
	}
	if o.ArtifactDownloadReason != "NotApplicable" || o.ArtifactVerifyReason != "NotApplicable" {
		t.Fatalf("expected NotApplicable reasons, got %q/%q", o.ArtifactDownloadReason, o.ArtifactVerifyReason)
	}
	if o.ArtifactDownloadAttempts != 0 || o.LastArtifactAttemptTime != "" {
		t.Fatalf("expected attempts/time cleared, got %d/%s", o.ArtifactDownloadAttempts, o.LastArtifactAttemptTime)
	}
}

func dispatchItem(artifactType apiv1alpha1.DeviceProcessArtifactType, cmd []string) gateway.DesiredItem {
	return gateway.DesiredItem{
		Namespace: "ns",
		Name:      "proc",
		SpecHash:  "hash",
		Spec: apiv1alpha1.DeviceProcessSpec{
			Artifact: apiv1alpha1.DeviceProcessArtifact{Type: artifactType, URL: "example"},
			Execution: apiv1alpha1.DeviceProcessExecution{
				Backend: apiv1alpha1.DeviceProcessBackendSystemd,
				Command: cmd,
			},
		},
	}
}

func derefBool(ptr *bool) bool {
	if ptr == nil {
		return false
	}
	return *ptr
}
