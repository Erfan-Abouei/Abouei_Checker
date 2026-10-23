package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/app"
)

func testTheme(t *testing.T) Theme {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	return NewTheme()
}

func testModel(t *testing.T, names ...string) Model {
	t.Helper()
	order := append([]string(nil), names...)
	m := make(map[string]string, len(order))
	for _, id := range order {
		m[id] = id
	}
	start := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	return NewModel(testTheme(t), order, m, start)
}

func probeEvent(id, outcome string, incident string) app.MonitorEvent {
	healthy := outcome == "healthy"
	return app.MonitorEvent{
		BackendID:           id,
		Name:                id,
		Outcome:             outcome,
		Healthy:             healthy,
		HTTPStatus:          200,
		DurationMs:          42,
		ObservedAt:          time.Date(2026, 10, 23, 12, 0, 10, 0, time.UTC),
		ConsecutiveFailures: 1,
		FailureThreshold:    3,
		IncidentStatus:      incident,
		Cycle:               1,
	}
}

func TestApplyProbeTracksTransitions(t *testing.T) {
	m := testModel(t, "api")
	applyProbe(&m, probeEvent("api", "healthy", "none"))

	state := m.backends["api"]
	if !state.Seen || !state.Healthy || state.Outcome != "healthy" {
		t.Fatalf("state = %+v, want seen healthy", state)
	}
	if len(m.feed) != 0 {
		t.Fatalf("healthy first probe must not add feed entries, got %v", m.feed)
	}

	applyProbe(&m, probeEvent("api", "unhealthy", "none"))
	if m.backends["api"].Healthy {
		t.Error("backend should be unhealthy")
	}
	if len(m.feed) != 1 || m.feed[0].Kind != FeedFailure {
		t.Fatalf("want one failure entry, got %v", m.feed)
	}

	opened := probeEvent("api", "unhealthy", "open")
	opened.ConsecutiveFailures = 3
	applyProbe(&m, opened)
	if m.backends["api"].Incident != "open" {
		t.Error("incident should be open")
	}
	last := m.feed[len(m.feed)-1]
	if last.Kind != FeedIncident || !strings.Contains(last.Summary, "incident opened") {
		t.Errorf("want incident-opened entry, got %v", last)
	}

	applyProbe(&m, probeEvent("api", "healthy", "recovered"))
	last = m.feed[len(m.feed)-1]
	if last.Kind != FeedIncident || !strings.Contains(last.Summary, "recovered") {
		t.Errorf("want recovery entry, got %v", last)
	}
	if total, healthy, failing, incidents := m.counts(); total != 1 || healthy != 1 || failing != 0 || incidents != 0 {
		t.Errorf("counts = %d/%d/%d/%d", total, healthy, failing, incidents)
	}
}

func TestApplyProbeKeepsStableOrder(t *testing.T) {
	m := testModel(t, "b", "a")
	before := append([]string(nil), m.order...)
	for _, id := range []string{"a", "b", "b", "a"} {
		applyProbe(&m, probeEvent(id, "healthy", "none"))
	}
	for i := range before {
		if m.order[i] != before[i] {
			t.Fatalf("order changed: %v -> %v", before, m.order)
		}
	}
}

func TestApplySeedUsesPersistedState(t *testing.T) {
	m := testModel(t, "api", "web")
	observed := time.Date(2026, 10, 23, 11, 59, 0, 0, time.UTC)
	status := 503
	incidentID := "incident-1"
	report := app.StatusReport{
		GeneratedAt:   time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC),
		OpenIncidents: 1,
		Backends: []app.BackendStatus{
			{BackendID: "api", Name: "api", Status: app.StatusDown,
				LastObservedAt: &observed, LastStatusCode: &status,
				ConsecutiveFailures: 3, OpenIncidentID: &incidentID},
		},
	}
	applySeed(&m, report)

	api := m.backends["api"]
	if !api.Seen || api.Healthy || api.Incident != "open" || api.HTTPStatus != 503 {
		t.Errorf("seed state wrong: %+v", api)
	}
	if m.backends["web"].Seen {
		t.Error("unobserved backend must stay pending")
	}
	if len(m.feed) != 1 || m.feed[0].Kind != FeedNotice {
		t.Fatalf("want carry-over notice, got %v", m.feed)
	}
}

func TestDisplayStatusMapsDomainOnly(t *testing.T) {
	cases := map[string]string{
		"healthy": "healthy", "unhealthy": "down", "error": "down", "canceled": "unknown",
	}
	for outcome, want := range cases {
		b := &BackendState{Seen: true, Outcome: outcome}
		if got := b.DisplayStatus(); got != want {
			t.Errorf("outcome %q -> %q, want %q", outcome, got, want)
		}
	}
	if got := (&BackendState{}).DisplayStatus(); got != "pending" {
		t.Errorf("unseen -> %q, want pending", got)
	}
}
