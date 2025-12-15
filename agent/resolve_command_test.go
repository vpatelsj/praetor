package main

import (
	"path/filepath"
	"testing"
)

func TestResolveCommandRejectsTraversal(t *testing.T) {
	_, err := resolveCommand([]string{"../../evil"}, "/opt/rootfs")
	if err == nil {
		t.Fatalf("expected traversal rejection")
	}
}

func TestResolveCommandAllowsAbsolute(t *testing.T) {
	cmd := []string{"/bin/ls", "-l"}
	resolved, err := resolveCommand(cmd, "/opt/rootfs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved[0] != cmd[0] || resolved[1] != "-l" {
		t.Fatalf("absolute command should pass through, got %v", resolved)
	}
}

func TestResolveCommandResolvesRelativeInsideRootfs(t *testing.T) {
	root := "/opt/rootfs"
	resolved, err := resolveCommand([]string{"bin/app", "--flag"}, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(root, "bin/app")
	if resolved[0] != want || resolved[1] != "--flag" {
		t.Fatalf("unexpected resolved command %v", resolved)
	}
}
