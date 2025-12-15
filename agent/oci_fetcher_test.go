package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/apollo/praetor/agent/systemd"
	apiv1alpha1 "github.com/apollo/praetor/api/azure.com/v1alpha1"
	"github.com/apollo/praetor/gateway"
)

type temporaryErr struct{ msg string }

func (e temporaryErr) Error() string   { return e.msg }
func (e temporaryErr) Timeout() bool   { return true }
func (e temporaryErr) Temporary() bool { return true }

func withOCIOverrides(t *testing.T, copyFn func(context.Context, oras.Target, string, oras.Target, string, oras.CopyOptions) (ocispec.Descriptor, error)) func() {
	origCopy := orasCopy
	origRepo := newRemoteRepository
	origNow := nowFunc
	orasCopy = copyFn
	newRemoteRepository = func(ref string) (*remote.Repository, error) {
		return &remote.Repository{}, nil
	}
	fixedNow := time.Date(2025, 12, 15, 1, 2, 3, 0, time.UTC)
	nowFunc = func() time.Time { return fixedNow }
	return func() {
		orasCopy = origCopy
		newRemoteRepository = origRepo
		nowFunc = origNow
	}
}

func pushSingleLayer(store *oci.Store, dstRef string, tarBytes []byte, mediaType string) (ocispec.Descriptor, error) {
	layerDesc := ocispec.Descriptor{
		MediaType: mediaType,
		Digest:    digest.FromBytes(tarBytes),
		Size:      int64(len(tarBytes)),
	}
	if err := store.Push(context.Background(), layerDesc, bytes.NewReader(tarBytes)); err != nil {
		return ocispec.Descriptor{}, err
	}
	manifest := ocispec.Manifest{Layers: []ocispec.Descriptor{layerDesc}}
	manifestBytes, _ := json.Marshal(manifest)
	manifestDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromBytes(manifestBytes),
		Size:      int64(len(manifestBytes)),
	}
	if err := store.Push(context.Background(), manifestDesc, bytes.NewReader(manifestBytes)); err != nil {
		return ocispec.Descriptor{}, err
	}
	if err := store.Tag(context.Background(), manifestDesc, dstRef); err != nil {
		return ocispec.Descriptor{}, err
	}
	return manifestDesc, nil
}

func makeTar(entries map[string]string) []byte {
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	for name, content := range entries {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(content))
	}
	_ = tw.Close()
	return buf.Bytes()
}

func TestEnsureOCICacheHitSkipsPull(t *testing.T) {
	dir := t.TempDir()
	digestStr := "sha256:" + strings.Repeat("0", 64)
	base := filepath.Join(dir, strings.TrimPrefix(digestStr, "sha256:"))
	rootfs := filepath.Join(base, "rootfs")
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		t.Fatalf("mkdir rootfs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, readyMarkerName), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write ready: %v", err)
	}

	called := 0
	restore := withOCIOverrides(t, func(ctx context.Context, src oras.Target, srcRef string, dst oras.Target, dstRef string, opts oras.CopyOptions) (ocispec.Descriptor, error) {
		called++
		return ocispec.Descriptor{}, errors.New("should not be called")
	})
	defer restore()

	f := newOCIFetcher(logr.Discard(), dir)
	res, err := f.Ensure(context.Background(), "ghcr.io/example/app@"+digestStr)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if called != 0 {
		t.Fatalf("orasCopy should not be called on cache hit")
	}
	if !res.downloaded || !res.verified {
		t.Fatalf("expected downloaded+verified true")
	}
	if res.attempts != 0 {
		t.Fatalf("expected attempts 0, got %d", res.attempts)
	}
}

func TestEnsureOCIBadRefRejected(t *testing.T) {
	f := newOCIFetcher(logr.Discard(), t.TempDir())
	_, err := f.Ensure(context.Background(), "ghcr.io/example/app:latest")
	if err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("expected pin by digest error, got %v", err)
	}
}

func TestEnsureOCIRejectsTraversal(t *testing.T) {
	digestStr := "sha256:" + strings.Repeat("1", 64)
	restore := withOCIOverrides(t, func(ctx context.Context, src oras.Target, srcRef string, dst oras.Target, dstRef string, opts oras.CopyOptions) (ocispec.Descriptor, error) {
		store := dst.(*oci.Store)
		tarBytes := makeTar(map[string]string{"../escape.sh": "echo bad"})
		return pushSingleLayer(store, dstRef, tarBytes, ocispec.MediaTypeImageLayer)
	})
	defer restore()

	f := newOCIFetcher(logr.Discard(), t.TempDir())
	_, err := f.Ensure(context.Background(), "ghcr.io/example/app@"+digestStr)
	if err == nil {
		t.Fatalf("expected traversal error")
	}
}

func TestEnsureOCIRetriesThenSucceeds(t *testing.T) {
	digestStr := "sha256:" + strings.Repeat("2", 64)
	calls := 0
	restore := withOCIOverrides(t, func(ctx context.Context, src oras.Target, srcRef string, dst oras.Target, dstRef string, opts oras.CopyOptions) (ocispec.Descriptor, error) {
		calls++
		if calls < 3 {
			return ocispec.Descriptor{}, temporaryErr{msg: "temp"}
		}
		store := dst.(*oci.Store)
		tarBytes := makeTar(map[string]string{"bin/app": "echo ok"})
		return pushSingleLayer(store, dstRef, tarBytes, ocispec.MediaTypeImageLayer)
	})
	defer restore()

	f := newOCIFetcher(logr.Discard(), t.TempDir())
	res, err := f.Ensure(context.Background(), "ghcr.io/example/app@"+digestStr)
	if err != nil {
		t.Fatalf("ensure failed: %v", err)
	}
	if res.attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", res.attempts)
	}
	if res.lastAttemptTime == "" {
		t.Fatalf("expected lastAttemptTime set")
	}
}

func TestReconcileDoesNotStartOnOCIFailure(t *testing.T) {
	fr := &recordingRunner{}
	restoreRunner := systemd.SetRunnerForTesting(fr)
	defer restoreRunner()
	restorePaths := systemd.SetBasePathsForTesting(t.TempDir(), filepath.Join(t.TempDir(), "env"))
	defer restorePaths()

	a := &agent{
		logger:    logr.Discard(),
		managed:   make(map[string]managedItem),
		statePath: filepath.Join(t.TempDir(), "state.json"),
		oci:       &failingOCI{},
		client:    &http.Client{Timeout: 2 * time.Second},
	}

	desired := &gateway.DesiredResponse{Items: []gateway.DesiredItem{{
		Namespace: "ns",
		Name:      "proc",
		SpecHash:  "hash",
		Spec: apiv1alpha1.DeviceProcessSpec{
			Artifact: apiv1alpha1.DeviceProcessArtifact{Type: apiv1alpha1.ArtifactTypeOCI, URL: "ghcr.io/app:tag"},
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
		t.Fatalf("expected one observation")
	}
	if obs[0].ProcessStarted != nil && *obs[0].ProcessStarted {
		t.Fatalf("process should not start on OCI failure")
	}
	for _, args := range fr.calls {
		if len(args) > 0 && args[0] == "enable" {
			t.Fatalf("should not call enable on failure")
		}
	}
}

type failingOCI struct{}

func (f *failingOCI) Ensure(_ context.Context, _ string) (ociResult, error) {
	return ociResult{lastError: "bad ref", downloaded: false, verified: false}, errors.New("bad ref")
}

type recordingRunner struct{ calls [][]string }

func (r *recordingRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	return nil, nil
}
