package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/incident"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/notify"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/probe"
)

// IncidentStatus values reported to the CLI per probe result.
const (
	IncidentNone      = "none"
	IncidentOpen      = "open"
	IncidentStillOpen = "still_open"
	IncidentRecovered = "recovered"
)

// ProcessOutcome summarizes one transactional probe application.
type ProcessOutcome struct {
	// Skipped reports canceled probes, which touch no state.
	Skipped bool
	// ObservationID is the persisted observation row.
	ObservationID int64
	// Failures and Successes are the persisted streaks after the probe.
	Failures  int
	Successes int
	// FailureThreshold is the configured consecutive-failure threshold.
	FailureThreshold int
	// FirstFailureAt is the active streak start, or zero when cleared.
	FirstFailureAt time.Time
	// Transition classifies the incident change.
	Transition incident.TransitionKind
	// IncidentID is the opened, still-open, or closed incident, if any.
	IncidentID string
	// IncidentStatus is the CLI-facing status: none, open, still_open, or
	// recovered.
	IncidentStatus string
	// EventID, EventType, and EventSequence describe the enqueued transition
	// event, when policy enabled one.
	EventID       string
	EventType     string
	EventSequence int
	// ReminderEventID is set when a due reminder was enqueued in the same
	// transaction.
	ReminderEventID string
}

// IncidentRow is a stored incident snapshot.
type IncidentRow struct {
	ID             string
	BackendID      string
	State          string
	OpenedAt       time.Time
	FirstFailureAt time.Time
	LastFailureAt  *time.Time
	ResolvedAt     *time.Time
	FailureCount   int
	RecoveryCount  int
	NextReminderAt *time.Time
	ReminderCount  int
}

// EventRow is a stored notification event.
type EventRow struct {
	ID           string
	IncidentID   string
	BackendID    string
	EventType    string
	Sequence     int
	Status       string
	AttemptCount int
	Payload      []byte
}

// ProcessResult applies one probe result transactionally: observation insert,
// streak upsert, incident transition, outbox insert, and due-reminder
// evaluation commit atomically. Canceled results are skipped without touching
// state. now fixes all timestamps in the transaction for testability.
func (s *Store) ProcessResult(
	ctx context.Context,
	backend config.Backend,
	result probe.Result,
	now time.Time,
) (ProcessOutcome, error) {
	if result.Outcome == probe.OutcomeCanceled {
		return ProcessOutcome{Skipped: true, IncidentStatus: IncidentNone}, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	var outcome ProcessOutcome
	err := withTransientRetry(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return &OpenError{Reason: "internal", Path: s.path, Message: "cannot persist probe result", cause: err}
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()

		result, err := s.processInTx(ctx, tx, backend, result, now)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return &OpenError{Reason: "internal", Path: s.path, Message: "cannot persist probe result", cause: err}
		}
		committed = true
		outcome = result
		return nil
	})
	if err != nil {
		return ProcessOutcome{}, err
	}
	return outcome, nil
}

func (s *Store) processInTx(
	ctx context.Context,
	tx *sql.Tx,
	backend config.Backend,
	result probe.Result,
	now time.Time,
) (ProcessOutcome, error) {
	failed := !result.Healthy()
	storedOutcome := "healthy"
	if failed {
		storedOutcome = "failed"
	}

	observationID, err := insertObservation(ctx, tx, backend.ID, result, storedOutcome)
	if err != nil {
		return ProcessOutcome{}, err
	}

	state, err := loadBackendStateTx(ctx, tx, backend.ID)
	if err != nil {
		return ProcessOutcome{}, err
	}
	open, err := loadOpenIncidentTx(ctx, tx, backend.ID)
	if err != nil {
		return ProcessOutcome{}, err
	}
	var openID string
	if open != nil {
		openID = open.ID
	}

	decision := incident.Evaluate(result, backend, incident.Streaks{
		Failures:       state.Failures,
		Successes:      state.Successes,
		FirstFailureAt: state.FirstFailureAt,
	}, openID, now)

	if err := upsertBackendState(ctx, tx, backend.ID, decision.Streaks, result, now); err != nil {
		return ProcessOutcome{}, err
	}

	outcome := ProcessOutcome{
		ObservationID:    observationID,
		Failures:         decision.Streaks.Failures,
		Successes:        decision.Streaks.Successes,
		FailureThreshold: backend.FailureThreshold,
		FirstFailureAt:   decision.Streaks.FirstFailureAt,
		Transition:       decision.Transition,
		IncidentStatus:   IncidentNone,
	}

	switch decision.Transition {
	case incident.TransitionOpened:
		applied, err := s.applyOpened(ctx, tx, backend, result, decision, now)
		if err != nil {
			return ProcessOutcome{}, err
		}
		outcome.IncidentID = applied.incidentID
		outcome.IncidentStatus = IncidentOpen
		outcome.EventID = applied.eventID
		outcome.EventType = applied.eventType
		outcome.EventSequence = applied.sequence
	case incident.TransitionStillOpen:
		if open == nil {
			// Lost race with a concurrent opener is handled in applyOpened;
			// reaching here means the incident vanished mid-transaction.
			return ProcessOutcome{}, &OpenError{Reason: "internal", Path: s.path, Message: "cannot persist probe result"}
		}
		if err := updateOpenMetadata(ctx, tx, open.ID, now); err != nil {
			return ProcessOutcome{}, err
		}
		outcome.IncidentID = open.ID
		outcome.IncidentStatus = IncidentStillOpen
	case incident.TransitionRecovered:
		if open == nil {
			return ProcessOutcome{}, &OpenError{Reason: "internal", Path: s.path, Message: "cannot persist probe result"}
		}
		applied, err := s.applyRecovered(ctx, tx, backend, result, open, decision, now)
		if err != nil {
			return ProcessOutcome{}, err
		}
		outcome.IncidentID = open.ID
		outcome.IncidentStatus = IncidentRecovered
		outcome.EventID = applied.eventID
		outcome.EventType = applied.eventType
		outcome.EventSequence = applied.sequence
	case incident.TransitionNone:
		if open != nil {
			outcome.IncidentID = open.ID
			outcome.IncidentStatus = IncidentStillOpen
		}
	}

	// Due-reminder evaluation runs in the same transaction whenever an
	// incident is open afterwards.
	if outcome.IncidentID != "" && outcome.IncidentStatus != IncidentRecovered {
		reminderID, enqueued, err := s.maybeEnqueueReminder(ctx, tx, backend, result, outcome.IncidentID, now)
		if err != nil {
			return ProcessOutcome{}, err
		}
		if enqueued {
			outcome.ReminderEventID = reminderID
		}
	}
	return outcome, nil
}

type appliedTransition struct {
	incidentID string
	eventID    string
	eventType  string
	sequence   int
}

func (s *Store) applyOpened(
	ctx context.Context,
	tx *sql.Tx,
	backend config.Backend,
	result probe.Result,
	decision incident.Outcome,
	now time.Time,
) (appliedTransition, error) {
	firstFailure := decision.Streaks.FirstFailureAt
	if firstFailure.IsZero() {
		firstFailure = result.ObservedAt
		if firstFailure.IsZero() {
			firstFailure = now
		}
	}
	var nextReminder *time.Time
	if backend.Notifications.Enabled && backend.Notifications.ReminderInterval > 0 {
		scheduled := now.Add(backend.Notifications.ReminderInterval)
		nextReminder = &scheduled
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO incidents
  (id, backend_id, state, opened_at_ms, first_failure_at_ms, last_failure_at_ms,
   resolved_at_ms, failure_count, recovery_count, next_reminder_at_ms,
   reminder_count, created_at_ms, updated_at_ms)
VALUES (?, ?, 'open', ?, ?, ?, NULL, ?, 0, ?, 0, ?, ?)`,
		decision.IncidentID, backend.ID,
		nowMillis(now), nowMillis(firstFailure), nowMillis(now),
		decision.Streaks.Failures, nullMillis(nextReminder),
		nowMillis(now), nowMillis(now),
	)
	if err != nil {
		if !isConstraintError(err) {
			return appliedTransition{}, &OpenError{Reason: "internal", Path: s.path, Message: "cannot persist probe result", cause: err}
		}
		// A concurrent transaction opened the incident first: keep it open
		// and update its metadata instead of duplicating Down.
		open, loadErr := loadOpenIncidentTx(ctx, tx, backend.ID)
		if loadErr != nil || open == nil {
			return appliedTransition{}, &OpenError{Reason: "internal", Path: s.path, Message: "cannot persist probe result", cause: err}
		}
		if updateErr := updateOpenMetadata(ctx, tx, open.ID, now); updateErr != nil {
			return appliedTransition{}, updateErr
		}
		return appliedTransition{incidentID: open.ID}, nil
	}

	applied := appliedTransition{incidentID: decision.IncidentID}
	if decision.EventType == "" {
		return applied, nil
	}
	eventID := incident.NewID()
	payload, err := notify.BuildPayload(openedPayload(decision.IncidentID, eventID, backend, result, decision.Streaks.Failures, firstFailure, now))
	if err != nil {
		return appliedTransition{}, &OpenError{Reason: "internal", Path: s.path, Message: "cannot persist probe result", cause: err}
	}
	if err := insertEvent(ctx, tx, eventID, decision.IncidentID, backend.ID, decision.EventType, 1, payload, now); err != nil {
		return appliedTransition{}, err
	}
	applied.eventID = eventID
	applied.eventType = decision.EventType
	applied.sequence = 1
	return applied, nil
}

func (s *Store) applyRecovered(
	ctx context.Context,
	tx *sql.Tx,
	backend config.Backend,
	result probe.Result,
	open *IncidentRow,
	decision incident.Outcome,
	now time.Time,
) (appliedTransition, error) {
	applied := appliedTransition{incidentID: open.ID}
	if _, err := tx.ExecContext(ctx, `
UPDATE incidents
SET state='closed', resolved_at_ms=?, recovery_count=?, updated_at_ms=?
WHERE id=? AND state='open'`,
		nowMillis(now), decision.Streaks.Successes, nowMillis(now), open.ID,
	); err != nil {
		return appliedTransition{}, &OpenError{Reason: "internal", Path: s.path, Message: "cannot persist probe result", cause: err}
	}
	// Cancel reminders that were queued but never delivered. A reminder
	// already in flight may still reach the receiver with its original
	// sequence, which an idempotent consumer ignores.
	if _, err := tx.ExecContext(ctx, `
UPDATE notification_events
SET status='canceled', updated_at_ms=?
WHERE incident_id=? AND event_type='incident.reminder' AND status IN ('pending','failed')`,
		nowMillis(now), open.ID,
	); err != nil {
		return appliedTransition{}, &OpenError{Reason: "internal", Path: s.path, Message: "cannot persist probe result", cause: err}
	}
	if decision.EventType == "" {
		return applied, nil
	}
	sequence, err := nextSequence(ctx, tx, open.ID)
	if err != nil {
		return appliedTransition{}, err
	}
	eventID := incident.NewID()
	payload, err := notify.BuildPayload(recoveredPayload(open, eventID, backend, result, decision.Streaks.Successes, sequence, now))
	if err != nil {
		return appliedTransition{}, &OpenError{Reason: "internal", Path: s.path, Message: "cannot persist probe result", cause: err}
	}
	if err := insertEvent(ctx, tx, eventID, open.ID, backend.ID, decision.EventType, sequence, payload, now); err != nil {
		return appliedTransition{}, err
	}
	applied.eventID = eventID
	applied.eventType = decision.EventType
	applied.sequence = sequence
	return applied, nil
}

// maybeEnqueueReminder enqueues at most one due reminder for an open
// incident. Missed slots are skipped: the next schedule starts from the
// enqueue time.
func (s *Store) maybeEnqueueReminder(
	ctx context.Context,
	tx *sql.Tx,
	backend config.Backend,
	result probe.Result,
	incidentID string,
	now time.Time,
) (string, bool, error) {
	if !backend.Notifications.Enabled || backend.Notifications.ReminderInterval <= 0 {
		return "", false, nil
	}
	open, err := loadIncidentByIDTx(ctx, tx, incidentID)
	if err != nil {
		return "", false, err
	}
	if open == nil || open.State != "open" {
		return "", false, nil
	}
	var blocking int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM notification_events
WHERE incident_id=? AND status IN ('pending','delivering','failed','dead')`,
		incidentID,
	).Scan(&blocking); err != nil {
		return "", false, &OpenError{Reason: "internal", Path: s.path, Message: "cannot persist probe result", cause: err}
	}
	maxSequence, err := maxSequenceTx(ctx, tx, incidentID)
	if err != nil {
		return "", false, err
	}
	enqueue, sequence := incident.ShouldRemind(incident.ReminderDecision{
		OpenIncidentID:    incidentID,
		Enabled:           true,
		NextReminderAt:    timeOrNow(open.NextReminderAt, now),
		HasNextReminderAt: open.NextReminderAt != nil,
		BlockingEvents:    blocking,
		ReminderCount:     open.ReminderCount,
		ReminderMax:       backend.Notifications.ReminderMax,
		LastSequence:      maxSequence,
	}, now)
	if !enqueue {
		return "", false, nil
	}
	eventID := incident.NewID()
	payload, err := notify.BuildPayload(reminderPayload(open, eventID, backend, result, sequence, now))
	if err != nil {
		return "", false, &OpenError{Reason: "internal", Path: s.path, Message: "cannot persist probe result", cause: err}
	}
	if err := insertEvent(ctx, tx, eventID, incidentID, backend.ID, incident.EventReminder, sequence, payload, now); err != nil {
		return "", false, err
	}
	next := now.Add(backend.Notifications.ReminderInterval)
	if _, err := tx.ExecContext(ctx, `
UPDATE incidents
SET next_reminder_at_ms=?, reminder_count=reminder_count+1, updated_at_ms=?
WHERE id=?`,
		nowMillis(next), nowMillis(now), incidentID,
	); err != nil {
		return "", false, &OpenError{Reason: "internal", Path: s.path, Message: "cannot persist probe result", cause: err}
	}
	return eventID, true, nil
}

func timeOrNow(t *time.Time, now time.Time) time.Time {
	if t == nil || t.IsZero() {
		return now
	}
	return *t
}
