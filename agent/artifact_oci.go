package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-logr/logr"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sys/unix"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
)

const (
	defaultOCIArtifactRoot   = "/var/lib/apollo/artifacts/oci"
	readyMarkerName          = "READY"
	defaultMaxExtractEntries = 10000
	defaultMaxExtractBytes   = int64(512 << 20) // 512MiB
)

var (
	digestPattern = regexp.MustCompile(`^sha256:[A-Fa-f0-9]{64}$`)

	maxExtractEntries = defaultMaxExtractEntries
	maxExtractBytes   = defaultMaxExtractBytes

	// Injection points for tests.
	orasCopy = func(ctx context.Context, src oras.Target, srcRef string, dst oras.Target, dstRef string, opts oras.CopyOptions) (ocispec.Descriptor, error) {
		return oras.Copy(ctx, src, srcRef, dst, dstRef, opts)
	}
	newRemoteRepository = func(ref string) (*remote.Repository, error) {
		return remote.NewRepository(ref)
	}
	nowFunc = func() time.Time { return time.Now().UTC() }
)

type ociResult struct {
	rootfsPath      string
	digest          string
	downloaded      bool
	verified        bool
	attempts        int32
	lastAttemptTime string
	lastError       string
	downloadReason  string
	downloadMessage string
	verifyReason    string
	verifyMessage   string
}

type ociFetcherImpl struct {
	root   string
	logger logr.Logger
}

func newOCIFetcher(logger logr.Logger, root string) ociFetcher {
	r := strings.TrimSpace(root)
	if r == "" {
		r = defaultOCIArtifactRoot
	}
	return &ociFetcherImpl{root: r, logger: logger}
}

func (f *ociFetcherImpl) Ensure(ctx context.Context, ref string) (ociResult, error) {
	res := ociResult{downloadReason: "ArtifactDownloadFailed", verifyReason: "ArtifactVerifyFailed"}
	parsedRef, err := registry.ParseReference(strings.TrimSpace(ref))
	if err != nil {
		return res, fmt.Errorf("invalid oci ref: %w", err)
	}
	if parsedRef.Reference == "" || !digestPattern.MatchString(parsedRef.Reference) {
		return res, fmt.Errorf("oci ref must be pinned by digest (got %q)", parsedRef.Reference)
	}

	digestHex := strings.TrimPrefix(parsedRef.Reference, "sha256:")
	baseDir := filepath.Join(f.root, digestHex)
	lockPath := filepath.Join(baseDir, ".lock")
	readyPath := filepath.Join(baseDir, readyMarkerName)
	rootfsPath := filepath.Join(baseDir, "rootfs")
	metaPath := filepath.Join(baseDir, "meta.json")

	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return res, err
	}

	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return res, err
	}
	defer lockFile.Close()
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX); err != nil {
		return res, err
	}
	defer unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)

	if fileExists(readyPath) && dirExists(rootfsPath) {
		res.rootfsPath = rootfsPath
		res.digest = parsedRef.Reference
		res.downloaded = true
		res.verified = true
		res.downloadReason = "ArtifactDownloaded"
		res.downloadMessage = "artifact cached"
		res.verifyReason = "ArtifactVerified"
		res.verifyMessage = "artifact cached"
		res.lastError = ""
		return res, nil
	}

	store, err := oci.New(baseDir)
	if err != nil {
		return res, err
	}

	repoRef := fmt.Sprintf("%s/%s", parsedRef.Registry, parsedRef.Repository)
	repository, err := newRemoteRepository(repoRef)
	if err != nil {
		return res, err
	}
	repository.PlainHTTP = allowPlainHTTP(parsedRef.Registry)

	attempts := int32(0)
	var desc ocispec.Descriptor
	for attempt := 0; attempt < 3; attempt++ {
		attempts++
		res.lastAttemptTime = nowFunc().Format(time.RFC3339)
		desc, err = orasCopy(ctx, repository, parsedRef.Reference, store, parsedRef.Reference, oras.DefaultCopyOptions)
		if err == nil {
			break
		}
		if !isRetryable(err) {
			break
		}
		backoff := backoffDuration(attempt)
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(backoff):
		}
	}

	res.attempts = attempts
	if err != nil {
		res.lastError = errorString(err)
		return res, err
	}
	res.downloaded = true
	res.downloadReason = "ArtifactDownloaded"
	res.downloadMessage = "artifact downloaded"
	res.digest = parsedRef.Reference

	if desc.Digest.String() != parsedRef.Reference {
		res.lastError = fmt.Sprintf("unexpected manifest digest %s (want %s)", desc.Digest.String(), parsedRef.Reference)
		res.verifyReason = "DigestMismatch"
		res.verifyMessage = res.lastError
		return res, fmt.Errorf(res.lastError)
	}

	manifestBytes, err := content.FetchAll(ctx, store, desc)
	if err != nil {
		res.lastError = errorString(err)
		return res, err
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		res.lastError = errorString(err)
		return res, err
	}
	if len(manifest.Layers) == 0 {
		res.lastError = "manifest has no layers"
		res.verifyMessage = res.lastError
		return res, fmt.Errorf(res.lastError)
	}
	if len(manifest.Layers) != 1 {
		res.lastError = "MVP requires single-layer tar artifact"
		res.verifyReason = "UnsupportedArtifact"
		res.verifyMessage = res.lastError
		return res, fmt.Errorf(res.lastError)
	}
	layer := manifest.Layers[0]

	layerReader, err := store.Fetch(ctx, layer)
	if err != nil {
		res.lastError = errorString(err)
		return res, err
	}
	defer layerReader.Close()

	tmpRoot := filepath.Join(baseDir, fmt.Sprintf("rootfs.tmp.%d", nowFunc().UnixNano()))
	if err := os.MkdirAll(tmpRoot, 0o755); err != nil {
		res.lastError = errorString(err)
		return res, err
	}
	size, err := extractLayer(layerReader, layer.MediaType, tmpRoot)
	if err != nil {
		os.RemoveAll(tmpRoot)
		res.lastError = errorString(err)
		res.verifyMessage = res.lastError
		if ee, ok := err.(extractError); ok {
			if ee.reason != "" {
				res.verifyReason = ee.reason
			}
			if ee.msg != "" {
				res.verifyMessage = ee.msg
			}
		}
		return res, err
	}

	if err := os.RemoveAll(rootfsPath); err != nil {
		os.RemoveAll(tmpRoot)
		res.lastError = errorString(err)
		return res, err
	}
	if err := os.Rename(tmpRoot, rootfsPath); err != nil {
		os.RemoveAll(tmpRoot)
		res.lastError = errorString(err)
		return res, err
	}

	meta := struct {
		Ref       string `json:"ref"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
		FetchedAt string `json:"fetchedAt"`
	}{
		Ref:       ref,
		Digest:    parsedRef.Reference,
		Size:      size,
		FetchedAt: nowFunc().Format(time.RFC3339),
	}
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(metaPath, metaBytes, 0o644); err != nil {
		res.lastError = errorString(err)
		return res, err
	}
	if err := os.WriteFile(readyPath, []byte("ok\n"), 0o644); err != nil {
		res.lastError = errorString(err)
		return res, err
	}

	res.rootfsPath = rootfsPath
	res.digest = parsedRef.Reference
	res.downloaded = true
	res.verified = true
	res.verifyReason = "ArtifactVerified"
	res.verifyMessage = "artifact verified"
	res.lastError = ""
	return res, nil
}

type extractError struct {
	reason string
	msg    string
}

func (e extractError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return e.reason
}

func extractLayer(r io.Reader, mediaType, dest string) (int64, error) {
	var reader io.Reader = r
	if strings.Contains(strings.ToLower(mediaType), "gzip") {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return 0, err
		}
		defer gz.Close()
		reader = gz
	}

	tr := tar.NewReader(reader)
	var total int64
	entries := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return total, err
		}

		entries++
		if entries > maxExtractEntries {
			return total, extractError{reason: "ExtractLimitExceeded", msg: fmt.Sprintf("extraction aborted: too many entries (%d > %d)", entries, maxExtractEntries)}
		}

		name := filepath.Clean(hdr.Name)
		if name == "." || name == "" {
			continue
		}
		if filepath.IsAbs(name) || strings.HasPrefix(name, "..") || strings.Contains(name, "../") {
			return total, extractError{reason: "InvalidPath", msg: fmt.Sprintf("rejecting unsafe path %q", hdr.Name)}
		}

		target := filepath.Join(dest, name)
		if !strings.HasPrefix(target, dest+string(os.PathSeparator)) {
			return total, extractError{reason: "InvalidPath", msg: fmt.Sprintf("rejecting path outside rootfs: %q", hdr.Name)}
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, hdr.FileInfo().Mode().Perm()); err != nil {
				return total, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return total, err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, hdr.FileInfo().Mode().Perm())
			if err != nil {
				return total, err
			}
			n, err := io.Copy(f, tr)
			f.Close()
			total += n
			if total > maxExtractBytes {
				return total, extractError{reason: "ExtractLimitExceeded", msg: fmt.Sprintf("extraction aborted: size %d exceeds limit %d", total, maxExtractBytes)}
			}
			if err != nil {
				return total, err
			}
		default:
			return total, extractError{reason: "UnsupportedEntryType", msg: fmt.Sprintf("unsupported entry type %d for %q", hdr.Typeflag, hdr.Name)}
		}
	}
	return total, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var nerr net.Error
	if errors.As(err, &nerr) && (nerr.Timeout() || nerr.Temporary()) {
		return true
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "429") || strings.Contains(s, "too many requests") {
		return true
	}
	if strings.Contains(s, " 5") || strings.Contains(s, "internal server error") {
		return true
	}
	return false
}

func backoffDuration(attempt int) time.Duration {
	base := time.Second * time.Duration(1<<attempt)
	if base > maxBackoff {
		base = maxBackoff
	}
	jitter := time.Duration(randInt63n(base.Nanoseconds() / 2))
	return base + jitter
}

func randInt63n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return time.Now().UnixNano() % n
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if len(msg) > 300 {
		return msg[:300]
	}
	return msg
}

func allowPlainHTTP(reg string) bool {
	plainAll := strings.EqualFold(strings.TrimSpace(os.Getenv("APOLLO_OCI_PLAIN_HTTP")), "1") ||
		strings.EqualFold(strings.TrimSpace(os.Getenv("APOLLO_OCI_PLAIN_HTTP")), "true")

	hostOnly := reg
	if h, _, err := net.SplitHostPort(reg); err == nil {
		hostOnly = h
	}
	hostOnly = strings.ToLower(hostOnly)

	if plainAll {
		return true
	}

	allowlist := strings.FieldsFunc(strings.TrimSpace(os.Getenv("APOLLO_OCI_PLAIN_HTTP_HOSTS")), func(r rune) bool { return r == ',' })
	for _, h := range allowlist {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if h == hostOnly {
			return true
		}
	}

	return false
}
