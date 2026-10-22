package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/incident"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/notify"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/probe"
)

// persistedStreaks mirrors the backend_state row for one backend.
type persistedStreaks struct {
	Failures       int
	Successes      int
	FirstFailureAt time.Time
	Found          bool
}

func loadBackendStateTx(ctx context.Context, tx *sql.Tx, backendID string) (persistedStreaks, error) {
	var state persistedStreaks
	var firstNull sql.NullInt64
	err := tx.QueryRowContext(ctx, `
SELECT consecutive_failures, consecutive_successes, first_failure_at_ms
FROM backend_state WHERE backend_id=?`, backendID,
	).Scan(&state.Failures, &state.Successes, &firstNull)
	if err != nil {
		if err == sql.ErrNoRows {
			return persistedStreaks{}, nil
		}
		return persistedStreaks{}, &OpenError{Reason: "internal", Path: "", Message: "cannot persist probe result", cause: err}
	}
	state.Found = true
	if firstNull.Valid {
		state.FirstFailureAt = millisToTime(firstNull.Int64)
	}
	return state, nil
}

func upsertBackendState(
	ctx context.Context,
	tx *sql.Tx,
	backendID string,
	streaks incident.Streaks,
	result probe.Result,
	now time.Time,
) error {
	outcome := "healthy"
	if !result.Healthy() {
		outcome = "failed"
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO backend_state
  (backend_id, consecutive_failures, consecutive_successes, first_failure_at_ms,
   last_outcome, last_status_code, last_observed_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(backend_id) DO UPDATE SET
  consecutive_failures=excluded.consecutive_failures,
  consecutive_successes=excluded.consecutive_successes,
  first_failure_at_ms=excluded.first_failure_at_ms,
  last_outcome=excluded.last_outcome,
  last_status_code=excluded.last_status_code,
  last_observed_at_ms=excluded.last_observed_at_ms,
  updated_at_ms=excluded.updated_at_ms`,
		backendID,
		streaks.Failures, streaks.Successes, nullMillisTime(streaks.FirstFailureAt),
		outcome, nullStatusCode(result.HTTPStatus), nowMillis(resultObservedAt(result, now)),
		nowMillis(now),
	)
	if err != nil {
		return &OpenError{Reason: "internal", Path: "", Message: "cannot persist probe result", cause: err}
	}
	return nil
}

func resultObservedAt(result probe.Result, now time.Time) time.Time {
	if !result.ObservedAt.IsZero() {
		return result.ObservedAt
	}
	return now
}

func insertObservation(
	ctx context.Context,
	tx *sql.Tx,
	backendID string,
	result probe.Result,
	storedOutcome string,
) (int64, error) {
	outcome, err := tx.ExecContext(ctx, `
INSERT INTO observations
  (backend_id, observed_at_ms, outcome, status_code, duration_ms, failure_reason, error_summary)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		backendID,
		nowMillis(resultObservedAt(result, time.Now().UTC())),
		storedOutcome,
		nullStatusCode(result.HTTPStatus),
		result.Duration.Milliseconds(),
		nullFailureReason(result.FailureReason),
		nullErrorSummary(result.Error),
	)
	if err != nil {
		return 0, &OpenError{Reason: "internal", Path: "", Message: "cannot persist probe result", cause: err}
	}
	id, err := outcome.LastInsertId()
	if err != nil {
		return 0, &OpenError{Reason: "internal", Path: "", Message: "cannot persist probe result", cause: err}
	}
	return id, nil
}

func updateOpenMetadata(ctx context.Context, tx *sql.Tx, incidentID string, now time.Time) error {
	result, err := tx.ExecContext(ctx, `
UPDATE incidents
SET last_failure_at_ms=?, failure_count=failure_count+1, updated_at_ms=?
WHERE id=? AND state='open'`,
		nowMillis(now), nowMillis(now), incidentID,
	)
	if err != nil {
		return &OpenError{Reason: "internal", Path: "", Message: "cannot persist probe result", cause: err}
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return &OpenError{Reason: "internal", Path: "", Message: "cannot persist probe result", cause: err}
	}
	if affected != 1 {
		return &OpenError{Reason: "internal", Path: "", Message: "cannot persist probe result"}
	}
	return nil
}

func insertEvent(
	ctx context.Context,
	tx *sql.Tx,
	eventID, incidentID, backendID, eventType string,
	sequence int,
	payload []byte,
	now time.Time,
) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO notification_events
  (id, incident_id, backend_id, event_type, sequence, payload_json,
   status, attempt_count, next_attempt_at_ms, last_error,
   created_at_ms, updated_at_ms, delivered_at_ms)
VALUES (?, ?, ?, ?, ?, ?, 'pending', 0, ?, NULL, ?, ?, NULL)`,
		eventID, incidentID, backendID, eventType, sequence, string(payload),
		nowMillis(now), nowMillis(now), nowMillis(now),
	)
	if err != nil {
		return &OpenError{Reason: "internal", Path: "", Message: "cannot persist probe result", cause: err}
	}
	return nil
}

// nextSequence returns one past the greatest used sequence, reserving 1 for
// incident.opened when no event row exists yet.
func nextSequence(ctx context.Context, tx *sql.Tx, incidentID string) (int, error) {
	maximum, err := maxSequenceTx(ctx, tx, incidentID)
	if err != nil {
		return 0, err
	}
	if maximum < 1 {
		maximum = 1
	}
	return maximum + 1, nil
}

func maxSequenceTx(ctx context.Context, tx *sql.Tx, incidentID string) (int, error) {
	var maximum sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(sequence) FROM notification_events WHERE incident_id=?`,
		incidentID,
	).Scan(&maximum); err != nil {
		return 0, &OpenError{Reason: "internal", Path: "", Message: "cannot persist probe result", cause: err}
	}
	if !maximum.Valid {
		return 0, nil
	}
	return int(maximum.Int64), nil
}

func loadOpenIncidentTx(ctx context.Context, tx *sql.Tx, backendID string) (*IncidentRow, error) {
	var row IncidentRow
	var lastFailure, resolved, nextReminder sql.NullInt64
	err := tx.QueryRowContext(ctx, `
SELECT id, backend_id, state, opened_at_ms, first_failure_at_ms,
       last_failure_at_ms, resolved_at_ms, failure_count, recovery_count,
       next_reminder_at_ms, reminder_count
FROM incidents WHERE backend_id=? AND state='open'`, backendID,
	).Scan(
		&row.ID, &row.BackendID, &row.State,
		(*millisScanner)(&row.OpenedAt), (*millisScanner)(&row.FirstFailureAt),
		&lastFailure, &resolved, &row.FailureCount, &row.RecoveryCount,
		&nextReminder, &row.ReminderCount,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, &OpenError{Reason: "internal", Path: "", Message: "cannot persist probe result", cause: err}
	}
	row.LastFailureAt = nullTime(lastFailure)
	row.ResolvedAt = nullTime(resolved)
	row.NextReminderAt = nullTime(nextReminder)
	return &row, nil
}

func loadIncidentByIDTx(ctx context.Context, tx *sql.Tx, incidentID string) (*IncidentRow, error) {
	var row IncidentRow
	var lastFailure, resolved, nextReminder sql.NullInt64
	err := tx.QueryRowContext(ctx, `
SELECT id, backend_id, state, opened_at_ms, first_failure_at_ms,
       last_failure_at_ms, resolved_at_ms, failure_count, recovery_count,
       next_reminder_at_ms, reminder_count
FROM incidents WHERE id=?`, incidentID,
	).Scan(
		&row.ID, &row.BackendID, &row.State,
		(*millisScanner)(&row.OpenedAt), (*millisScanner)(&row.FirstFailureAt),
		&lastFailure, &resolved, &row.FailureCount, &row.RecoveryCount,
		&nextReminder, &row.ReminderCount,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, &OpenError{Reason: "internal", Path: "", Message: "cannot persist probe result", cause: err}
	}
	row.LastFailureAt = nullTime(lastFailure)
	row.ResolvedAt = nullTime(resolved)
	row.NextReminderAt = nullTime(nextReminder)
	return &row, nil
}

// millisScanner scans a NOT NULL millisecond column into a time.Time.
type millisScanner time.Time

func (m *millisScanner) Scan(value any) error {
	var ms int64
	switch raw := value.(type) {
	case int64:
		ms = raw
	case int:
		ms = int64(raw)
	default:
		return &OpenError{Reason: "internal", Path: "", Message: "cannot persist probe result"}
	}
	*m = millisScanner(millisToTime(ms))
	return nil
}

func nullTime(ms sql.NullInt64) *time.Time {
	if !ms.Valid {
		return nil
	}
	instant := millisToTime(ms.Int64)
	return &instant
}

func nullMillis(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return nowMillis(*t)
}

func nullMillisTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return nowMillis(t)
}

func nullStatusCode(code int) any {
	if code == 0 {
		return nil
	}
	return code
}

func nullFailureReason(reason probe.Reason) any {
	if reason == "" {
		return nil
	}
	return string(reason)
}

func nullErrorSummary(summary string) any {
	if summary == "" {
		return nil
	}
	return notify.BoundError(summary)
}

// ListOpenIncidents returns every open incident for startup reconciliation.
func (s *Store) ListOpenIncidents(ctx context.Context) ([]IncidentRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, backend_id, state, opened_at_ms, first_failure_at_ms,
       last_failure_at_ms, resolved_at_ms, failure_count, recovery_count,
       next_reminder_at_ms, reminder_count
FROM incidents WHERE state='open' ORDER BY opened_at_ms ASC`)
	if err != nil {
		return nil, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read incident state", cause: err}
	}
	defer func() { _ = rows.Close() }()
	var incidents []IncidentRow
	for rows.Next() {
		var row IncidentRow
		var opened, first int64
		var lastFailure, resolved, nextReminder sql.NullInt64
		if err := rows.Scan(
			&row.ID, &row.BackendID, &row.State, &opened, &first,
			&lastFailure, &resolved, &row.FailureCount, &row.RecoveryCount,
			&nextReminder, &row.ReminderCount,
		); err != nil {
			return nil, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read incident state", cause: err}
		}
		row.OpenedAt = millisToTime(opened)
		row.FirstFailureAt = millisToTime(first)
		row.LastFailureAt = nullTime(lastFailure)
		row.ResolvedAt = nullTime(resolved)
		row.NextReminderAt = nullTime(nextReminder)
		incidents = append(incidents, row)
	}
	if err := rows.Err(); err != nil {
		return nil, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read incident state", cause: err}
	}
	return incidents, nil
}

// EnsureBackendState reconstructs a missing streak row from an open incident
// so restarts never lose threshold state. It reports whether it created a
// row; callers log a warning in that case.
func (s *Store) EnsureBackendState(ctx context.Context, backendID string, open *IncidentRow) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM backend_state WHERE backend_id=?`, backendID,
	).Scan(&count); err != nil {
		return false, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read incident state", cause: err}
	}
	if count > 0 {
		return false, nil
	}
	failures := 0
	var first any
	var outcome any = "healthy"
	if open != nil {
		failures = open.FailureCount
		first = nowMillis(open.FirstFailureAt)
		outcome = "failed"
	}
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO backend_state
  (backend_id, consecutive_failures, consecutive_successes, first_failure_at_ms,
   last_outcome, last_status_code, last_observed_at_ms, updated_at_ms)
VALUES (?, ?, 0, ?, ?, NULL, NULL, ?)`,
		backendID, failures, first, outcome, nowMillis(now),
	); err != nil {
		return false, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read incident state", cause: err}
	}
	return true, nil
}

// resetStaleDelivering returns in-flight claims to pending at startup. The
// current attempt is abandoned without incrementing the count; a receiver
// that already committed will see a duplicate with the same event ID.
func (s *Store) resetStaleDelivering(ctx context.Context) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE notification_events SET status='pending', updated_at_ms=? WHERE status='delivering'`,
		nowMillis(time.Now().UTC()),
	)
	if err != nil {
		return &OpenError{Reason: "corrupt", Path: s.path, Message: "monitoring database is not usable", cause: err}
	}
	_, _ = result.RowsAffected()
	return nil
}

// LoadEvent returns one outbox row for delivery or inspection.
func (s *Store) LoadEvent(ctx context.Context, eventID string) (*EventRow, error) {
	var row EventRow
	var payload string
	err := s.db.QueryRowContext(ctx, `
SELECT id, incident_id, backend_id, event_type, sequence, status, attempt_count, payload_json
FROM notification_events WHERE id=?`, eventID,
	).Scan(&row.ID, &row.IncidentID, &row.BackendID, &row.EventType, &row.Sequence, &row.Status, &row.AttemptCount, &payload)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, err
		}
		return nil, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read notification outbox", cause: err}
	}
	row.Payload = []byte(payload)
	return &row, nil
}

// ClaimDueEvent atomically claims the oldest due event whose earlier incident
// sequences are all terminal, preserving per-incident order across restarts.
// It returns sql.ErrNoRows when nothing is due.
func (s *Store) ClaimDueEvent(ctx context.Context, now time.Time) (*notify.ClaimedEvent, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var claimed notify.ClaimedEvent
	var payload string
	err := s.db.QueryRowContext(ctx, `
UPDATE notification_events
SET status='delivering', attempt_count=attempt_count+1, updated_at_ms=?
WHERE id = (
  SELECT e.id FROM notification_events e
  WHERE (e.status='pending'
         OR (e.status='failed' AND (e.next_attempt_at_ms IS NULL OR e.next_attempt_at_ms<=?)))
    AND NOT EXISTS (
      SELECT 1 FROM notification_events p
      WHERE p.incident_id=e.incident_id AND p.sequence<e.sequence
        AND p.status IN ('pending','delivering','failed'))
  ORDER BY e.next_attempt_at_ms ASC, e.created_at_ms ASC
  LIMIT 1)
RETURNING id, incident_id, backend_id, event_type, sequence, attempt_count, payload_json`,
		nowMillis(now), nowMillis(now),
	).Scan(&claimed.ID, &claimed.IncidentID, &claimed.BackendID, &claimed.EventType,
		&claimed.Sequence, &claimed.AttemptCount, &payload)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, sql.ErrNoRows
		}
		return nil, &OpenError{Reason: "internal", Path: s.path, Message: "cannot claim notification event", cause: err}
	}
	claimed.Payload = []byte(payload)
	return &claimed, nil
}

// MarkDelivered records a successful delivery. Terminal rows never run again.
func (s *Store) MarkDelivered(ctx context.Context, id string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE notification_events
SET status='delivered', delivered_at_ms=?, updated_at_ms=?
WHERE id=? AND status='delivering'`, nowMillis(now.UTC()), nowMillis(now.UTC()), id)
	if err != nil {
		return &OpenError{Reason: "internal", Path: s.path, Message: "cannot record notification delivery", cause: err}
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return &OpenError{Reason: "internal", Path: s.path, Message: "cannot record notification delivery", cause: err}
	}
	if affected != 1 {
		return &OpenError{Reason: "internal", Path: s.path, Message: "cannot record notification delivery"}
	}
	return nil
}

// MarkFailed records a retryable failure with durable retry scheduling.
func (s *Store) MarkFailed(ctx context.Context, id string, nextAttempt time.Time, lastError string) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE notification_events
SET status='failed', next_attempt_at_ms=?, last_error=?, updated_at_ms=?
WHERE id=? AND status='delivering'`,
		nowMillis(nextAttempt.UTC()), notify.BoundError(lastError), nowMillis(time.Now().UTC()), id)
	if err != nil {
		return &OpenError{Reason: "internal", Path: s.path, Message: "cannot record notification delivery", cause: err}
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return &OpenError{Reason: "internal", Path: s.path, Message: "cannot record notification delivery", cause: err}
	}
	if affected != 1 {
		return &OpenError{Reason: "internal", Path: s.path, Message: "cannot record notification delivery"}
	}
	return nil
}

// MarkDead records a terminal delivery failure. Dead rows never run again
// and never block later sequences in the same incident.
func (s *Store) MarkDead(ctx context.Context, id string, lastError string) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE notification_events
SET status='dead', next_attempt_at_ms=NULL, last_error=?, updated_at_ms=?
WHERE id=? AND status='delivering'`,
		notify.BoundError(lastError), nowMillis(time.Now().UTC()), id)
	if err != nil {
		return &OpenError{Reason: "internal", Path: s.path, Message: "cannot record notification delivery", cause: err}
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return &OpenError{Reason: "internal", Path: s.path, Message: "cannot record notification delivery", cause: err}
	}
	if affected != 1 {
		return &OpenError{Reason: "internal", Path: s.path, Message: "cannot record notification delivery"}
	}
	return nil
}

// CountPending returns the number of non-terminal outbox rows for operations
// visibility.
func (s *Store) CountPending(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM notification_events
WHERE status IN ('pending','delivering','failed')`).Scan(&count); err != nil {
		return 0, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read notification outbox", cause: err}
	}
	return count, nil
}
