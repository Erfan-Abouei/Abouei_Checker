// Package monitor keeps in-memory threshold streaks and schedules probes.
//
// Streaks are intentionally not persisted in Phase 3: they live only for the
// lifetime of the owning process. Durable incident state arrives in Phase 4.
package monitor

import (
	"sync"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/probe"
)

// Snapshot is the in-memory health and streak state for one backend. It is
// not persisted: Phase 3 keeps streaks only for the lifetime of the process.
type Snapshot struct {
	// BackendID is the stable configured backend identity.
	BackendID string
	// Name is the display name.
	Name string
	// ConsecutiveFailures counts successive non-healthy probes. A healthy
	// probe resets it to zero. Canceled probes leave it unchanged.
	ConsecutiveFailures int
	// ConsecutiveSuccesses counts successive healthy probes. Any non-healthy,
	// non-canceled probe resets it to zero.
	ConsecutiveSuccesses int
	// FailureThreshold is the configured consecutive-failure threshold.
	FailureThreshold int
	// ThresholdReached is true once ConsecutiveFailures reaches FailureThreshold.
	ThresholdReached bool
	// LastOutcome is the most recent non-canceled probe outcome, or empty when
	// no conclusive probe has been recorded yet.
	LastOutcome probe.Outcome
	// LastHTTPStatus is the most recent status code, or 0 when none exists.
	LastHTTPStatus int
}

// Tracker keeps deterministic, race-safe consecutive-failure streaks. The zero
// value is not usable; construct with NewTracker.
type Tracker struct {
	mu         sync.Mutex
	failures   map[string]int
	successes  map[string]int
	thresholds map[string]int
	names      map[string]string
	outcomes   map[string]probe.Outcome
	statuses   map[string]int
}

// NewTracker builds a tracker for the given backend thresholds. Thresholds
// must be positive; non-positive values default to 1 to keep evaluation
// deterministic even with unexpected input.
func NewTracker(thresholds map[string]int, names map[string]string) *Tracker {
	copiedThresholds := make(map[string]int, len(thresholds))
	for id, threshold := range thresholds {
		if threshold <= 0 {
			threshold = 1
		}
		copiedThresholds[id] = threshold
	}
	copiedNames := make(map[string]string, len(names))
	for id, name := range names {
		copiedNames[id] = name
	}
	return &Tracker{
		failures:   make(map[string]int),
		successes:  make(map[string]int),
		thresholds: copiedThresholds,
		names:      copiedNames,
		outcomes:   make(map[string]probe.Outcome),
		statuses:   make(map[string]int),
	}
}

// Record applies result to the streak state and returns the updated snapshot.
// Canceled results leave streak counters unchanged and never report a new
// outcome as healthy.
func (t *Tracker) Record(result probe.Result) Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()

	threshold := t.thresholds[result.BackendID]
	if threshold <= 0 {
		threshold = 1
	}
	if _, ok := t.names[result.BackendID]; !ok {
		t.names[result.BackendID] = result.Name
	}

	if result.Outcome == probe.OutcomeCanceled {
		return Snapshot{
			BackendID:            result.BackendID,
			Name:                 t.names[result.BackendID],
			ConsecutiveFailures:  t.failures[result.BackendID],
			ConsecutiveSuccesses: t.successes[result.BackendID],
			FailureThreshold:     threshold,
			ThresholdReached:     t.failures[result.BackendID] >= threshold,
			LastOutcome:          t.outcomes[result.BackendID],
			LastHTTPStatus:       t.statuses[result.BackendID],
		}
	}

	if result.Healthy() {
		t.failures[result.BackendID] = 0
		t.successes[result.BackendID]++
	} else {
		t.failures[result.BackendID]++
		t.successes[result.BackendID] = 0
	}
	t.outcomes[result.BackendID] = result.Outcome
	t.statuses[result.BackendID] = result.HTTPStatus

	return Snapshot{
		BackendID:            result.BackendID,
		Name:                 t.names[result.BackendID],
		ConsecutiveFailures:  t.failures[result.BackendID],
		ConsecutiveSuccesses: t.successes[result.BackendID],
		FailureThreshold:     threshold,
		ThresholdReached:     t.failures[result.BackendID] >= threshold,
		LastOutcome:          result.Outcome,
		LastHTTPStatus:       result.HTTPStatus,
	}
}

// Get returns the current snapshot for id.
func (t *Tracker) Get(id string) (Snapshot, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	threshold, ok := t.thresholds[id]
	if !ok {
		return Snapshot{}, false
	}
	if threshold <= 0 {
		threshold = 1
	}
	return Snapshot{
		BackendID:            id,
		Name:                 t.names[id],
		ConsecutiveFailures:  t.failures[id],
		ConsecutiveSuccesses: t.successes[id],
		FailureThreshold:     threshold,
		ThresholdReached:     t.failures[id] >= threshold,
		LastOutcome:          t.outcomes[id],
		LastHTTPStatus:       t.statuses[id],
	}, true
}
