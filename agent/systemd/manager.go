package systemd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultUnitDir   = "/etc/systemd/system"
	defaultEnvDir    = "/etc/apollo/env"
	maxUnitBaseLen   = 80
	tempFileTemplate = ".tmp-apollo-*"
)

var (
	unitDir              = defaultUnitDir
	envDir               = defaultEnvDir
	defaultRunner Runner = &execRunner{}

	reInvalid = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)
)

// Runner executes commands. Pluggable for tests.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (r *execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = os.Environ()
	return cmd.CombinedOutput()
}

// Paths holds derived unit/env locations for a DeviceProcess.
type Paths struct {
	UnitName string
	UnitPath string
	EnvPath  string
}

// PathsFor returns deterministic, sanitized paths for a namespaced name.
func PathsFor(namespace, name string) Paths {
	base := sanitizedBase(namespace, name)
	unitName := base + ".service"
	return Paths{
		UnitName: unitName,
		UnitPath: filepath.Join(unitDir, unitName),
		EnvPath:  filepath.Join(envDir, base+".env"),
	}
}

// EnsureUnit writes the unit and env files idempotently. Returns true when either file changed.
func EnsureUnit(ctx context.Context, unitName, unitContent string, envPath string, envContent string) (bool, error) {
	unitChanged, envChanged, err := EnsureUnitWithDetails(ctx, unitName, unitContent, envPath, envContent)
	return unitChanged || envChanged, err
}

// EnsureUnitWithDetails writes files and reports which file changed.
func EnsureUnitWithDetails(ctx context.Context, unitName, unitContent string, envPath string, envContent string) (bool, bool, error) {
	_ = ctx // context kept for API symmetry; file writes are local.

	unitPath := filepath.Join(unitDir, unitName)
	changedUnit, err := writeIfChanged(unitPath, []byte(unitContent), 0o644)
	if err != nil {
		return false, false, err
	}

	envDirPath := filepath.Dir(envPath)
	if err := os.MkdirAll(envDirPath, 0o755); err != nil {
		return changedUnit, false, err
	}
	changedEnv, err := writeIfChanged(envPath, []byte(envContent), 0o600)
	if err != nil {
		return changedUnit, changedEnv, err
	}

	return changedUnit, changedEnv, nil
}

// RemoveUnit deletes unit and env files if present. Returns true when any file was removed.
func RemoveUnit(ctx context.Context, unitName, unitPath string, envPath string) (bool, error) {
	unitRemoved, envRemoved, err := RemoveUnitWithDetails(ctx, unitName, unitPath, envPath)
	return unitRemoved || envRemoved, err
}

// RemoveUnitWithDetails deletes files and reports which ones were removed.
func RemoveUnitWithDetails(ctx context.Context, unitName, unitPath string, envPath string) (bool, bool, error) {
	_ = ctx
	_ = unitName

	unitRemoved, err := removeIfExists(unitPath)
	if err != nil {
		return false, false, err
	}
	envRemoved, err := removeIfExists(envPath)
	if err != nil {
		return unitRemoved, false, err
	}
	return unitRemoved, envRemoved, nil
}

// EnableAndStart enables the unit and starts it.
func EnableAndStart(ctx context.Context, unitName string) error {
	out, err := runSystemctl(ctx, "enable", "--now", unitName)
	if err != nil {
		return fmt.Errorf("systemctl enable --now %s: %w: %s", unitName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Start starts the unit (without enabling it).
func Start(ctx context.Context, unitName string) error {
	out, err := runSystemctl(ctx, "start", unitName)
	if err != nil {
		return fmt.Errorf("systemctl start %s: %w: %s", unitName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Restart restarts the unit.
func Restart(ctx context.Context, unitName string) error {
	out, err := runSystemctl(ctx, "restart", unitName)
	if err != nil {
		return fmt.Errorf("systemctl restart %s: %w: %s", unitName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StopAndDisable stops the unit and disables it.
func StopAndDisable(ctx context.Context, unitName string) error {
	out, err := runSystemctl(ctx, "disable", "--now", unitName)
	if err != nil {
		return fmt.Errorf("systemctl disable --now %s: %w: %s", unitName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DaemonReload reloads systemd units.
func DaemonReload(ctx context.Context) error {
	out, err := runSystemctl(ctx, "daemon-reload")
	if err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// IsUnitNotFoundError returns true when a systemctl operation failed because the unit does not exist.
func IsUnitNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "could not be found") ||
		strings.Contains(s, "not-found") ||
		strings.Contains(s, "loaded: not-found") ||
		strings.Contains(s, "does not exist") ||
		strings.Contains(s, "unit file") && strings.Contains(s, "does not exist")
}

// Show returns runtime info for a unit.
func Show(ctx context.Context, unitName string) (int64, time.Time, string, string, error) {
	out, err := runSystemctl(ctx, "show", unitName, "-p", "MainPID", "-p", "ExecMainStartTimestamp", "-p", "ActiveState", "-p", "SubState")
	if err != nil {
		return 0, time.Time{}, "", "", fmt.Errorf("systemctl show %s: %w: %s", unitName, err, strings.TrimSpace(string(out)))
	}

	lines := strings.Split(string(out), "\n")
	get := func(key string) string {
		for _, line := range lines {
			if strings.HasPrefix(line, key+"=") {
				return strings.TrimPrefix(line, key+"=")
			}
		}
		return ""
	}

	pidStr := strings.TrimSpace(get("MainPID"))
	pid, _ := strconv.ParseInt(pidStr, 10, 64)

	startStr := strings.TrimSpace(get("ExecMainStartTimestamp"))
	startTime, _ := parseTimestamp(startStr)

	return pid, startTime, get("ActiveState"), get("SubState"), nil
}

// SetRunnerForTesting swaps the systemctl runner and returns a restore func.
func SetRunnerForTesting(r Runner) func() {
	prev := defaultRunner
	defaultRunner = r
	return func() { defaultRunner = prev }
}

// SetBasePathsForTesting overrides path roots and returns a restore func.
func SetBasePathsForTesting(uDir, eDir string) func() {
	prevUnit := unitDir
	prevEnv := envDir
	unitDir = uDir
	envDir = eDir
	return func() {
		unitDir = prevUnit
		envDir = prevEnv
	}
}

func runSystemctl(ctx context.Context, args ...string) ([]byte, error) {
	return defaultRunner.Run(ctx, "systemctl", args...)
}

func sanitizedBase(namespace, name string) string {
	sanitize := func(s string) string {
		s = strings.TrimSpace(strings.ToLower(s))
		s = reInvalid.ReplaceAllString(s, "-")
		s = strings.Trim(s, "-")
		if s == "" {
			return "device"
		}
		return s
	}

	base := fmt.Sprintf("apollo-%s-%s", sanitize(namespace), sanitize(name))
	if len(base) <= maxUnitBaseLen {
		return base
	}

	sum := sha256.Sum256([]byte(base))
	suffix := hex.EncodeToString(sum[:])[:8]
	keep := maxUnitBaseLen - len(suffix) - 1
	if keep < 1 {
		keep = 1
	}
	return fmt.Sprintf("%s-%s", base[:keep], suffix)
}

func parseTimestamp(val string) (time.Time, error) {
	val = strings.TrimSpace(val)
	if val == "" || strings.EqualFold(val, "n/a") {
		return time.Time{}, nil
	}

	layouts := []string{
		"Mon 2006-01-02 15:04:05 MST",
		time.RFC3339,
	}
	for _, layout := range layouts {
		if ts, err := time.Parse(layout, val); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp: %s", val)
}

func writeIfChanged(path string, content []byte, perm os.FileMode) (bool, error) {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, content) {
		return false, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), tempFileTemplate)
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())

	if perm != 0 {
		if err := tmp.Chmod(perm); err != nil {
			tmp.Close()
			return false, err
		}
	}

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		return false, err
	}
	return true, nil
}

func removeIfExists(path string) (bool, error) {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
