package main

import (
	"testing"

	apiv1alpha1 "github.com/apollo/praetor/api/azure.com/v1alpha1"
)

func TestRenderEnvFile_QuotesAndSorts(t *testing.T) {
	out, err := RenderEnvFile([]apiv1alpha1.DeviceProcessEnvVar{
		{Name: "B", Value: "has spaces"},
		{Name: "A", Value: "1"},
	})
	if err != nil {
		t.Fatalf("RenderEnvFile error: %v", err)
	}
	want := "A=\"1\"\nB=\"has spaces\"\n"
	if out != want {
		t.Fatalf("unexpected output %q, want %q", out, want)
	}
}

func TestRenderEnvFile_PreservesHashesEqualsUnicode(t *testing.T) {
	out, err := RenderEnvFile([]apiv1alpha1.DeviceProcessEnvVar{{Name: "A", Value: "x#y=z café"}})
	if err != nil {
		t.Fatalf("RenderEnvFile error: %v", err)
	}
	want := "A=\"x#y=z café\"\n"
	if out != want {
		t.Fatalf("unexpected output %q, want %q", out, want)
	}
}

func TestRenderEnvFile_EscapesQuotesAndBackslashes(t *testing.T) {
	out, err := RenderEnvFile([]apiv1alpha1.DeviceProcessEnvVar{{Name: "A", Value: "a\"b\\c"}})
	if err != nil {
		t.Fatalf("RenderEnvFile error: %v", err)
	}
	want := "A=\"a\\\"b\\\\c\"\n"
	if out != want {
		t.Fatalf("unexpected output %q, want %q", out, want)
	}
}

func TestRenderEnvFile_RejectsInvalidKey(t *testing.T) {
	_, err := RenderEnvFile([]apiv1alpha1.DeviceProcessEnvVar{{Name: "1BAD", Value: "x"}})
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestRenderEnvFile_RejectsNewlineInjection(t *testing.T) {
	_, err := RenderEnvFile([]apiv1alpha1.DeviceProcessEnvVar{{Name: "A", Value: "x\nB=evil"}})
	if err == nil {
		t.Fatalf("expected error")
	}
}
