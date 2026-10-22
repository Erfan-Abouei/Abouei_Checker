package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/app"
)

func TestJoinIDs(t *testing.T) {
	if got := joinIDs(nil); got != "" {
		t.Errorf("joinIDs(nil) = %q, want empty", got)
	}
	if got := joinIDs([]string{"a", "b"}); got != "a, b" {
		t.Errorf("joinIDs = %q, want %q", got, "a, b")
	}
}

func TestFormatMonitorLine(t *testing.T) {
	healthy := app.MonitorEvent{
		BackendID: "api", Outcome: "healthy", Healthy: true,
		HTTPStatus: 200, DurationMs: 12, ConsecutiveFailures: 0, FailureThreshold: 3,
	}
	if got := formatMonitorLine(healthy); !strings.Contains(got, "api: healthy") || !strings.Contains(got, "200") {
		t.Errorf("healthy line = %q", got)
	}
	unhealthy := app.MonitorEvent{
		BackendID: "api", Outcome: "unhealthy", Healthy: false,
		HTTPStatus: 503, DurationMs: 30, FailureReason: "status_mismatch",
		Error: "unexpected status code 503", ConsecutiveFailures: 1, FailureThreshold: 3,
	}
	if got := formatMonitorLine(unhealthy); !strings.Contains(got, "streak 1/3") || !strings.Contains(got, "503") {
		t.Errorf("unhealthy line = %q", got)
	}
	canceled := app.MonitorEvent{
		BackendID: "api", Outcome: "canceled", Healthy: false,
		Error: "probe canceled", ConsecutiveFailures: 1, FailureThreshold: 3,
	}
	if got := formatMonitorLine(canceled); !strings.Contains(got, "canceled") {
		t.Errorf("canceled line = %q", got)
	}
	noStatus := app.MonitorEvent{
		BackendID: "api", Outcome: "healthy", Healthy: true, DurationMs: 5,
	}
	if got := formatMonitorLine(noStatus); !strings.Contains(got, "api: healthy") {
		t.Errorf("no-status line = %q", got)
	}
}

func TestMonitorProbeEnvelope(t *testing.T) {
	event := app.MonitorEvent{
		BackendID: "api", Name: "API", Outcome: "unhealthy", Healthy: false,
		HTTPStatus: 503, DurationMs: 9, ObservedAt: time.Now().UTC(),
		FailureReason: "status_mismatch", Error: "unexpected status code 503",
		ConsecutiveFailures: 2, FailureThreshold: 3, ThresholdReached: false, Cycle: 4,
	}
	payload := monitorProbeEnvelope(event)
	if payload.Command != "monitor" || payload.Status != "unhealthy" {
		t.Errorf("envelope = %+v", payload)
	}
	data, ok := payload.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type = %T", payload.Data)
	}
	if data["kind"] != "probe" || data["cycle"] != 4 {
		t.Errorf("data = %v", data)
	}
	if _, present := data["failure_reason"]; !present {
		t.Error("unhealthy probe must carry failure_reason")
	}

	healthy := event
	healthy.Outcome = "healthy"
	healthy.Healthy = true
	healthy.FailureReason = ""
	healthy.Error = ""
	healthyPayload := monitorProbeEnvelope(healthy)
	if healthyPayload.Status != "success" {
		t.Errorf("healthy envelope status = %q", healthyPayload.Status)
	}
	healthyData := healthyPayload.Data.(map[string]any)
	if _, present := healthyData["failure_reason"]; present {
		t.Error("healthy probe must omit failure_reason")
	}
}
