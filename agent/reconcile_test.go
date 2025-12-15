package main

import (
	"testing"

	apiv1alpha1 "github.com/apollo/praetor/api/azure.com/v1alpha1"
	"github.com/apollo/praetor/gateway"
)

func TestRenderUnitFiles(t *testing.T) {
	item := gateway.DesiredItem{
		Namespace: "edge-ns",
		Name:      "processor",
		Spec: apiv1alpha1.DeviceProcessSpec{
			Execution: apiv1alpha1.DeviceProcessExecution{
				Backend:    apiv1alpha1.DeviceProcessBackendSystemd,
				Command:    []string{"/usr/bin/app"},
				Args:       []string{"--mode", "fast start"},
				Env:        []apiv1alpha1.DeviceProcessEnvVar{{Name: "B", Value: "2"}, {Name: "A", Value: "1"}},
				WorkingDir: "/opt/work",
				User:       "apollo",
			},
			RestartPolicy: apiv1alpha1.DeviceProcessRestartPolicyOnFailure,
		},
	}

	unit, env, err := renderUnitFiles(item, "/etc/apollo/env/apollo-edge-ns-processor.env")
	if err != nil {
		t.Fatalf("renderUnitFiles returned error: %v", err)
	}

	expectedUnit := "[Unit]\n" +
		"Description=Apollo DeviceProcess edge-ns/processor\n" +
		"After=network.target\n\n" +
		"[Service]\n" +
		"Type=simple\n" +
		"ExecStart=/usr/bin/app --mode \"fast start\"\n" +
		"WorkingDirectory=/opt/work\n" +
		"EnvironmentFile=-/etc/apollo/env/apollo-edge-ns-processor.env\n" +
		"Restart=on-failure\n" +
		"User=apollo\n\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"

	if unit != expectedUnit {
		t.Fatalf("unexpected unit content:\n%s\nexpected:\n%s", unit, expectedUnit)
	}

	expectedEnv := "A=1\nB=2\n"
	expectedEnv = "A=\"1\"\nB=\"2\"\n"
	if env != expectedEnv {
		t.Fatalf("unexpected env content %q, expected %q", env, expectedEnv)
	}
}
