package systemd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeRunner struct {
	output   []byte
	err      error
	lastArgs []string
}

func (f *fakeRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	f.lastArgs = args
	return f.output, f.err
}

func TestEnsureUnitWithDetailsIdempotent(t *testing.T) {
	unitDir := t.TempDir()
	envDir := filepath.Join(t.TempDir(), "env")
	restorePaths := SetBasePathsForTesting(unitDir, envDir)
	defer restorePaths()

	unitName := "apollo-ns-name.service"
	unitContent := "[Unit]\nDescription=test\n\n[Service]\nType=simple\nExecStart=/bin/true\n\n[Install]\nWantedBy=multi-user.target\n"
	envPath := filepath.Join(envDir, "apollo-ns-name.env")
	envContent := "FOO=bar\n"

	unitChanged, envChanged, err := EnsureUnitWithDetails(context.Background(), unitName, unitContent, envPath, envContent)
	if err != nil {
		t.Fatalf("first ensure failed: %v", err)
	}
	if !unitChanged || !envChanged {
		t.Fatalf("expected changes on first write, got unitChanged=%v envChanged=%v", unitChanged, envChanged)
	}

	unitChanged, envChanged, err = EnsureUnitWithDetails(context.Background(), unitName, unitContent, envPath, envContent)
	if err != nil {
		t.Fatalf("second ensure failed: %v", err)
	}
	if unitChanged || envChanged {
		t.Fatalf("expected no changes on second write, got unitChanged=%v envChanged=%v", unitChanged, envChanged)
	}

	updatedEnv := "FOO=baz\n"
	unitChanged, envChanged, err = EnsureUnitWithDetails(context.Background(), unitName, unitContent, envPath, updatedEnv)
	if err != nil {
		t.Fatalf("env update failed: %v", err)
	}
	if envChanged == false {
		t.Fatalf("expected env change on update")
	}
	if unitChanged {
		t.Fatalf("unit should not be marked changed when only env updates")
	}

	content, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	if string(content) != updatedEnv {
		t.Fatalf("env content mismatch: %q", string(content))
	}
}

func TestShowParsesOutput(t *testing.T) {
	fake := &fakeRunner{output: []byte("MainPID=4321\nExecMainStartTimestamp=Tue 2024-02-13 14:22:11 UTC\nActiveState=active\nSubState=running\n")}
	restore := SetRunnerForTesting(fake)
	defer restore()

	pid, started, active, sub, err := Show(context.Background(), "apollo-unit.service")
	if err != nil {
		t.Fatalf("show failed: %v", err)
	}
	if pid != 4321 {
		t.Fatalf("expected pid 4321, got %d", pid)
	}
	if started.IsZero() {
		t.Fatalf("expected non-zero start time")
	}
	if active != "active" || sub != "running" {
		t.Fatalf("unexpected states active=%s sub=%s", active, sub)
	}
}

func TestPathsForSanitizes(t *testing.T) {
	unitDir := filepath.Join(t.TempDir(), "units")
	envDir := filepath.Join(t.TempDir(), "env")
	restorePaths := SetBasePathsForTesting(unitDir, envDir)
	defer restorePaths()

	paths := PathsFor("Namespace With Spaces", "name@123!!")
	if paths.UnitName == "" || paths.EnvPath == "" || paths.UnitPath == "" {
		t.Fatalf("paths should not be empty: %+v", paths)
	}
	if len(paths.UnitName) >= 90 {
		t.Fatalf("expected unit name to be reasonably short, got len=%d", len(paths.UnitName))
	}
	if filepath.Dir(paths.UnitPath) != unitDir {
		t.Fatalf("unit path dir mismatch: %s", paths.UnitPath)
	}
	if filepath.Dir(paths.EnvPath) != envDir {
		t.Fatalf("env path dir mismatch: %s", paths.EnvPath)
	}
	if filepath.Ext(paths.UnitName) != ".service" {
		t.Fatalf("unit name should end with .service, got %s", paths.UnitName)
	}
}

func TestParseTimestampAcceptsRFC3339(t *testing.T) {
	val := time.Now().UTC().Format(time.RFC3339)
	ts, err := parseTimestamp(val)
	if err != nil {
		t.Fatalf("parseTimestamp failed: %v", err)
	}
	if ts.Format(time.RFC3339) != val {
		t.Fatalf("round trip mismatch: %s vs %s", ts.Format(time.RFC3339), val)
	}
}
