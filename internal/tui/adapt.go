package tui

import (
	"fmt"
	"strings"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/app"
)

// applySeed folds the pre-launch Status snapshot into the model so incidents
// opened by previous runs are visible before the first session probe lands.
// Only persisted operational state is used; nothing is invented.
func applySeed(m *Model, report app.StatusReport) {
	byID := make(map[string]app.BackendStatus, len(report.Backends))
	for _, backend := range report.Backends {
		byID[backend.BackendID] = backend
	}
	for _, id := range m.order {
		row, ok := byID[id]
		if !ok || row.LastObservedAt == nil {
			continue
		}
		state := m.backends[id]
		if state == nil {
			continue
		}
		state.Seen = true
		state.ObservedAt = row.LastObservedAt.UTC()
		if row.LastStatusCode != nil {
			state.HTTPStatus = *row.LastStatusCode
		}
		state.Failures = row.ConsecutiveFailures
		switch row.Status {
		case app.StatusHealthy:
			state.Outcome = "healthy"
			state.Healthy = true
		case app.StatusDown:
			state.Outcome = "unhealthy"
			state.Healthy = false
			state.Incident = "open"
		default:
			state.Outcome = "error"
			state.Healthy = false
		}
	}
	if report.OpenIncidents > 0 {
		m.pushFeed(FeedEvent{
			At:      report.GeneratedAt,
			Kind:    FeedNotice,
			Summary: fmt.Sprintf("%d open incident(s) carried over from previous runs", report.OpenIncidents),
		})
	}
}

// applyProbe folds one live engine event into the model. Feed entries are
// emitted only for meaningful transitions (first failure, recovery,
// incident open/recovered), never one line per healthy probe.
func applyProbe(m *Model, event app.MonitorEvent) {
	state := m.backends[event.BackendID]
	if state == nil {
		// Defensive: configuration is fixed, but never drop a real event.
		m.order = append(m.order, event.BackendID)
		state = &BackendState{ID: event.BackendID, Name: event.Name}
		if state.Name == "" {
			state.Name = state.ID
		}
		m.backends[event.BackendID] = state
	}
	wasSeen := state.Seen
	wasHealthy := state.Healthy
	prevIncident := state.Incident

	state.Seen = true
	state.Outcome = event.Outcome
	state.Healthy = event.Healthy
	state.HTTPStatus = event.HTTPStatus
	state.LatencyMs = event.DurationMs
	state.ObservedAt = event.ObservedAt
	state.FailureNote = event.FailureReason
	state.Error = truncateRunes(event.Error, 240)
	state.Failures = event.ConsecutiveFailures
	state.Threshold = event.FailureThreshold
	state.Incident = event.IncidentStatus
	state.Cycle = event.Cycle

	at := event.ObservedAt
	if at.IsZero() {
		at = m.now
	}
	switch {
	case event.IncidentStatus == "open" && prevIncident != "open":
		m.pushFeed(FeedEvent{At: at, BackendID: event.BackendID, Kind: FeedIncident,
			Summary: fmt.Sprintf("%s incident opened (streak %d/%d)", event.BackendID, event.ConsecutiveFailures, event.FailureThreshold)})
	case event.IncidentStatus == "recovered":
		m.pushFeed(FeedEvent{At: at, BackendID: event.BackendID, Kind: FeedIncident,
			Summary: fmt.Sprintf("%s recovered", event.BackendID)})
	case !event.Healthy && event.Outcome != "canceled" && (!wasSeen || wasHealthy):
		note := strings.TrimSpace(event.Error)
		if note == "" {
			note = strings.TrimSpace(event.FailureReason)
		}
		if note == "" {
			note = event.Outcome
		}
		m.pushFeed(FeedEvent{At: at, BackendID: event.BackendID, Kind: FeedFailure,
			Summary: fmt.Sprintf("%s failing: %s", event.BackendID, truncateRunes(note, 120))})
	case event.Healthy && wasSeen && !wasHealthy:
		m.pushFeed(FeedEvent{At: at, BackendID: event.BackendID, Kind: FeedRecovery,
			Summary: fmt.Sprintf("%s healthy again", event.BackendID)})
	}
}

// activeIncidents returns states with an open incident in stable order.
func activeIncidents(m *Model) []*BackendState {
	var out []*BackendState
	for _, id := range m.order {
		b := m.backends[id]
		if b == nil || !b.Seen {
			continue
		}
		if b.Incident == "open" || b.Incident == "still_open" {
			out = append(out, b)
		}
	}
	return out
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}
