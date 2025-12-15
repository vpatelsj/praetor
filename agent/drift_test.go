package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apollo/praetor/agent/systemd"
	apiv1alpha1 "github.com/apollo/praetor/api/azure.com/v1alpha1"
	"github.com/apollo/praetor/gateway"
	"github.com/go-logr/logr"
)

type seqRunner struct {
	calls     [][]string
	showCount int
}

func (r *seqRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{}, args...))
	if len(args) == 0 {
		return nil, nil
	}
	switch args[0] {
	case "show":
		if r.showCount == 0 {
			r.showCount++
			return []byte("MainPID=0\nExecMainStartTimestamp=n/a\nActiveState=inactive\nSubState=dead\n"), nil
		}
		return []byte("MainPID=999\nExecMainStartTimestamp=Tue 2024-02-13 14:22:11 UTC\nActiveState=active\nSubState=running\n"), nil
	default:
		return []byte(""), nil
	}
}

func TestReconcileStartsStoppedService(t *testing.T) {
	ctx := context.Background()
	unitDir := filepath.Join(t.TempDir(), "units")
	envDir := filepath.Join(t.TempDir(), "env")
	restorePaths := systemd.SetBasePathsForTesting(unitDir, envDir)
	defer restorePaths()

	runner := &seqRunner{}
	restoreRunner := systemd.SetRunnerForTesting(runner)
	defer restoreRunner()

	item := gateway.DesiredItem{
		Namespace: "ns",
		Name:      "proc",
		SpecHash:  "h1",
		Spec: apiv1alpha1.DeviceProcessSpec{
			Execution: apiv1alpha1.DeviceProcessExecution{
				Backend: apiv1alpha1.DeviceProcessBackendSystemd,
				Command: []string{"/usr/bin/app"},
				Args:    []string{"--flag"},
			},
			Artifact:      apiv1alpha1.DeviceProcessArtifact{Type: apiv1alpha1.ArtifactTypeFile, URL: "/usr/bin/app"},
			RestartPolicy: apiv1alpha1.DeviceProcessRestartPolicyNever,
		},
	}

	paths := systemd.PathsFor(item.Namespace, item.Name)
	unitContent, envContent, err := renderUnitFiles(item, paths.EnvPath)
	if err != nil {
		t.Fatalf("renderUnitFiles: %v", err)
	}
	if !strings.Contains(unitContent, "Restart=no\n") {
		t.Fatalf("expected unit to render Restart=no, got:\n%s", unitContent)
	}
	if err := os.MkdirAll(filepath.Dir(paths.UnitPath), 0o755); err != nil {
		t.Fatalf("mkdir unit dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.EnvPath), 0o755); err != nil {
		t.Fatalf("mkdir env dir: %v", err)
	}
	if err := os.WriteFile(paths.UnitPath, []byte(unitContent), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	if err := os.WriteFile(paths.EnvPath, []byte(envContent), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}

	ag := &agent{
		logger:       logr.Discard(),
		managed:      map[string]managedItem{},
		statePath:    filepath.Join(t.TempDir(), "state.json"),
		lastObserved: map[string]string{},
	}

	key := itemKey(item.Namespace, item.Name)
	ag.managed[key] = managedItem{
		UnitName:           paths.UnitName,
		LastActionAt:       time.Now().Add(-10 * time.Second).UTC().Format(time.RFC3339),
		LastActionSpecHash: item.SpecHash,
	}

	desired := &gateway.DesiredResponse{Items: []gateway.DesiredItem{item}}
	_, err = ag.reconcile(ctx, desired)
	if err != nil {
		t.Fatalf("reconcile error: %v", err)
	}

	started := false
	for _, c := range runner.calls {
		if len(c) >= 2 && c[0] == "start" && c[1] == paths.UnitName {
			started = true
			break
		}
	}
	if !started {
		var sb strings.Builder
		for _, c := range runner.calls {
			sb.WriteString(strings.Join(c, " "))
			sb.WriteByte('\n')
		}
		t.Fatalf("expected systemctl start to be called; calls:\n%s", sb.String())
	}
}
