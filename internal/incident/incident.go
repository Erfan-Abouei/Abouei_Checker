// Package incident implements the threshold-driven incident state machine
// defined in docs/incident-lifecycle.md.
//
// It is pure domain logic: it evaluates probe results against persisted
// streaks and policy and decides transitions, but it never touches SQLite,
// HTTP, or the CLI. Transactional application happens in internal/store and
// delivery in internal/notify.
package incident

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/probe"
)

// TransitionKind classifies what one probe result does to incident state.
type TransitionKind string

const (
	// TransitionNone means no incident opened, closed, or updated.
	TransitionNone TransitionKind = "none"
	// TransitionOpened means the failure threshold was reached and a new
	// incident opens.
	TransitionOpened TransitionKind = "opened"
	// TransitionStillOpen means the probe failed while an incident was already
	// open; metadata is updated but no new Down event is created.
	TransitionStillOpen TransitionKind = "still_open"
	// TransitionRecovered means the recovery threshold was reached and the
	// open incident closes.
	TransitionRecovered TransitionKind = "recovered"
)

// Event types for the versioned webhook payload contract.
const (
	EventOpened    = "incident.opened"
	EventReminder  = "incident.reminder"
	EventRecovered = "incident.recovered"
)

// Streaks carries the persisted consecutive counters for one backend.
type Streaks struct {
	// Failures counts consecutive failed probes.
	Failures int
	// Successes counts consecutive healthy probes while an incident is open.
	Successes int
	// FirstFailureAt is the start of the current failure streak, or zero when
	// there is no active streak.
	FirstFailureAt time.Time
}

// Outcome is the evaluated result of one probe: updated streaks plus any
// incident transition and the notification event it requires (if policy
// enables one).
type Outcome struct {
	// Streaks are the updated counters the caller must persist.
	Streaks Streaks
	// Transition classifies the incident change.
	Transition TransitionKind
	// IncidentID is the new incident on open, or the still-open/closed
	// incident ID. Empty when no incident is involved.
	IncidentID string
	// EventType is the webhook event to enqueue, or empty when policy
	// disables the notification for this transition.
	EventType string
}

// Evaluate applies the threshold rules to one probe result. openIncidentID is
// the currently open incident for the backend, or empty when none is open.
// Canceled results leave streaks unchanged and produce no transition; callers
// normally skip persistence for them entirely.
func Evaluate(
	result probe.Result,
	backend config.Backend,
	streaks Streaks,
	openIncidentID string,
	now time.Time,
) Outcome {
	if result.ObservedAt.IsZero() {
		result.ObservedAt = now
		if result.ObservedAt.IsZero() {
			result.ObservedAt = time.Now().UTC()
		}
	}
	if result.Outcome == probe.OutcomeCanceled {
		return Outcome{Streaks: streaks, Transition: TransitionNone}
	}
	if result.Healthy() {
		return evaluateHealthy(backend, streaks, openIncidentID)
	}
	return evaluateFailed(result, backend, streaks, openIncidentID)
}

func evaluateFailed(
	result probe.Result,
	backend config.Backend,
	streaks Streaks,
	openIncidentID string,
) Outcome {
	updated := Streaks{
		Failures:  streaks.Failures + 1,
		Successes: 0,
	}
	if streaks.Failures == 0 {
		updated.FirstFailureAt = result.ObservedAt
	} else {
		updated.FirstFailureAt = streaks.FirstFailureAt
	}

	if openIncidentID != "" {
		return Outcome{
			Streaks:    updated,
			Transition: TransitionStillOpen,
			IncidentID: openIncidentID,
			EventType:  "",
		}
	}
	if updated.Failures < effectiveThreshold(backend.FailureThreshold) {
		return Outcome{Streaks: updated, Transition: TransitionNone}
	}
	outcome := Outcome{
		Streaks:    updated,
		Transition: TransitionOpened,
		IncidentID: NewID(),
		EventType:  "",
	}
	if backend.Notifications.Enabled && backend.Notifications.NotifyOnDown {
		outcome.EventType = EventOpened
	}
	return outcome
}

func evaluateHealthy(
	backend config.Backend,
	streaks Streaks,
	openIncidentID string,
) Outcome {
	if openIncidentID == "" {
		return Outcome{
			Streaks:    Streaks{},
			Transition: TransitionNone,
		}
	}
	updated := Streaks{Successes: streaks.Successes + 1}
	if updated.Successes < effectiveThreshold(backend.RecoveryThreshold) {
		return Outcome{
			Streaks:    updated,
			Transition: TransitionNone,
			IncidentID: openIncidentID,
		}
	}
	outcome := Outcome{
		Streaks:    Streaks{Successes: updated.Successes},
		Transition: TransitionRecovered,
		IncidentID: openIncidentID,
		EventType:  "",
	}
	if backend.Notifications.Enabled && backend.Notifications.NotifyOnRecovery {
		outcome.EventType = EventRecovered
	}
	return outcome
}

func effectiveThreshold(threshold int) int {
	if threshold <= 0 {
		return 1
	}
	return threshold
}

// ReminderDecision carries the inputs for one reminder evaluation.
type ReminderDecision struct {
	// OpenIncidentID is empty when no incident is open (no reminder then).
	OpenIncidentID string
	// Enabled reports whether reminder policy is active for the backend.
	Enabled bool
	// NextReminderAt is the scheduled time; a zero value means due now.
	NextReminderAt time.Time
	// HasNextReminderAt reports whether a schedule exists.
	HasNextReminderAt bool
	// BlockingEvents counts earlier incident events in a non-terminal state
	// (pending, delivering, failed, or dead). Reminders wait for terminal
	// delivery before a new one is enqueued.
	BlockingEvents int
	// ReminderCount is how many reminders were already sent for the incident.
	ReminderCount int
	// ReminderMax caps reminders; zero means unlimited.
	ReminderMax int
	// LastSequence is the greatest event sequence used for the incident, or 1
	// when no event row exists (sequence 1 stays reserved for incident.opened).
	LastSequence int
}

// ShouldRemind applies the reminder policy: enabled, open incident, due
// schedule, no blocking earlier events, and cap not reached. It returns the
// sequence the reminder would use.
func ShouldRemind(decision ReminderDecision, now time.Time) (enqueue bool, sequence int) {
	if decision.OpenIncidentID == "" || !decision.Enabled {
		return false, 0
	}
	if decision.HasNextReminderAt && now.Before(decision.NextReminderAt) {
		return false, 0
	}
	if decision.BlockingEvents > 0 {
		return false, 0
	}
	if decision.ReminderMax > 0 && decision.ReminderCount >= decision.ReminderMax {
		return false, 0
	}
	last := decision.LastSequence
	if last < 1 {
		last = 1
	}
	return true, last + 1
}

// NewID generates a UUIDv4 string with crypto/rand for incident and event
// identity. It panics only if the system random source fails, which callers
// treat as fatal because identity must never be duplicated or empty.
func NewID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(fmt.Sprintf("incident: cannot generate UUID: %v", err))
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}
