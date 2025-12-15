package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/apollo/praetor/gateway"
)

func TestReportPayloadIncludesZeroPIDAndEmptyStartTime(t *testing.T) {
	req := gateway.ReportRequest{
		AgentVersion: "test",
		Timestamp:    "2025-12-15T00:00:00Z",
		Heartbeat:    true,
		Observations: []gateway.Observation{{
			Namespace:        "ns",
			Name:             "p",
			ObservedSpecHash: "h",
			PID:              0,
			StartTime:        "",
		}},
	}

	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(b)
	if !strings.Contains(js, `"pid":0`) {
		t.Fatalf("expected pid field in JSON, got %s", js)
	}
	if !strings.Contains(js, `"startTime":""`) {
		t.Fatalf("expected startTime field in JSON, got %s", js)
	}
}
