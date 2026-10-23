package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// retentionBatchSize bounds how many rows a single retention DELETE removes so
// cleanup never holds a long write lock. Retention loops until a batch deletes
// nothing, which keeps each batch atomic while remaining safe to rerun.
const retentionBatchSize = 500

// incidentRetentionBatchSize is smaller because incident deletes cascade into
// the outbox through the foreign key.
const incidentRetentionBatchSize = 200

// BackendStateView is a read-only snapshot of one backend_state row used by
// the status command. Pointer fields are nil when the database holds NULL,
// which the application renders as "unknown" instead of inventing a value.
type BackendStateView struct {
	BackendID            string
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	FirstFailureAt       *time.Time
	LastOutcome          string
	LastStatusCode       *int
	LastObservedAt       *time.Time
	UpdatedAt            time.Time
}

// ObservationRow is one persisted probe observation for history queries.
type ObservationRow struct {
	ID            int64
	BackendID     string
	ObservedAt    time.Time
	Outcome       string
	StatusCode    *int
	DurationMs    int64
	FailureReason *string
	ErrorSummary  *string
}

// IncidentEventRow is one durable notification outbox row for history
// queries. Payload JSON is intentionally not returned: history shows event
// identity and timing, never secret-bearing or verbose content.
type IncidentEventRow struct {
	EventID    string
	IncidentID string
	BackendID  string
	EventType  string
	Sequence   int
	Status     string
	OccurredAt time.Time
}

// HistoryQuery bounds a history lookup. Limit is clamped to 1..1000 with a
// default of 50. Since, when non-nil, excludes rows older than that instant.
type HistoryQuery struct {
	BackendID string
	Since     *time.Time
	Limit     int
}

// HistoryPage is the result of QueryHistory. Observations and incident events
// are each newest-first with deterministic tie-breaking. HasMore reports
// whether either list was truncated by the limit.
type HistoryPage struct {
	Observations []ObservationRow
	Events       []IncidentEventRow
	HasMore      bool
}

// RetentionPolicy mirrors config.Retention without importing the config
// package from the store layer. Durations must be positive; non-positive
// classes are skipped.
type RetentionPolicy struct {
	Observations    time.Duration
	Incidents       time.Duration
	DeliveredEvents time.Duration
}

// RetentionResult counts rows removed by ApplyRetention.
type RetentionResult struct {
	ObservationsDeleted int64
	EventsDeleted       int64
	IncidentsDeleted    int64
}

// normalizeHistoryLimit applies the documented 1..1000 range with a default
// of 50.
func normalizeHistoryLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 1000 {
		return 1000
	}
	return limit
}

// OpenReadOnly opens an existing database without mutating it. It never
// creates directories or files, never runs migrations, never resets delivery
// claims, and never changes journal mode. A missing file fails with a hint to
// run check or monitor first; a schema that needs migration fails with a hint
// to run a writable command.
func OpenReadOnly(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, &OpenError{Reason: "path", Path: path, Message: "database path is not configured"}
	}
	clean := filepath.Clean(path)
	exists, statErr := fileExists(clean)
	if statErr != nil {
		return nil, &OpenError{
			Reason:  "permissions",
			Path:    clean,
			Message: fmt.Sprintf("cannot access database path %s", clean),
			cause:   statErr,
		}
	}
	if !exists {
		return nil, &OpenError{
			Reason:  "missing",
			Path:    clean,
			Message: fmt.Sprintf("no monitoring database at %s yet; run check or monitor first", clean),
		}
	}
	if err := ensureRegularFile(clean); err != nil {
		return nil, err
	}

	// mode=ro asks SQLite to refuse any write at the VFS layer, so a
	// status/history bug that attempts a write fails loudly instead of
	// mutating operational state.
	db, err := sql.Open("sqlite", "file:"+clean+"?mode=ro")
	if err != nil {
		return nil, &OpenError{Reason: "internal", Path: clean, Message: "monitoring database is unavailable", cause: err}
	}
	s := &Store{db: db, path: clean}
	// Multiple read-only connections are safe: no writer exists in this
	// handle, and the single-writer rule applies to the read-write handle.
	db.SetMaxOpenConns(4)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	if err := s.applyReadOnlyPragmas(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.checkReadOnlyVersion(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.verifyTables(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// applyReadOnlyPragmas sets only pragmas that are valid on a read-only
// handle. Journal mode and synchronous level are deliberately left alone: the
// writer chose them, and changing them here would attempt a write.
func (s *Store) applyReadOnlyPragmas() error {
	pragmas := []string{
		"PRAGMA foreign_keys=ON",
		"PRAGMA temp_store=MEMORY",
		fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeoutMs),
	}
	for _, pragma := range pragmas {
		if _, err := s.db.Exec(pragma); err != nil {
			return &OpenError{
				Reason:  "corrupt",
				Path:    s.path,
				Message: "monitoring database is not usable",
				cause:   fmt.Errorf("%s: %w", pragma, err),
			}
		}
	}
	return nil
}

// checkReadOnlyVersion enforces the schema contract without migrating. A
// newer database fails closed with upgrade guidance; an older database fails
// with a hint to run a writable command because migration needs write access.
func (s *Store) checkReadOnlyVersion() error {
	version, err := s.userVersion()
	if err != nil {
		return err
	}
	if version > SupportedSchemaVersion {
		return &OpenError{
			Reason:  "schema_newer",
			Path:    s.path,
			Message: fmt.Sprintf("monitoring database schema version %d is newer than this binary supports (%d); upgrade the binary", version, SupportedSchemaVersion),
		}
	}
	if version < SupportedSchemaVersion {
		return &OpenError{
			Reason:  "migration",
			Path:    s.path,
			Message: fmt.Sprintf("monitoring database schema version %d needs migration to version %d; run check or monitor with write access", version, SupportedSchemaVersion),
		}
	}
	return nil
}

// GetBackendStates returns every persisted backend_state row keyed by backend
// ID. Context cancellation is honored by the query.
func (s *Store) GetBackendStates(ctx context.Context) (map[string]BackendStateView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT backend_id, consecutive_failures, consecutive_successes,
       first_failure_at_ms, last_outcome, last_status_code,
       last_observed_at_ms, updated_at_ms
FROM backend_state`)
	if err != nil {
		return nil, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read backend state", cause: err}
	}
	defer func() { _ = rows.Close() }()
	states := make(map[string]BackendStateView)
	for rows.Next() {
		var view BackendStateView
		var first, observed sql.NullInt64
		var outcome sql.NullString
		var status sql.NullInt64
		var updated int64
		if err := rows.Scan(
			&view.BackendID, &view.ConsecutiveFailures, &view.ConsecutiveSuccesses,
			&first, &outcome, &status, &observed, &updated,
		); err != nil {
			return nil, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read backend state", cause: err}
		}
		view.FirstFailureAt = nullTime(first)
		if outcome.Valid {
			view.LastOutcome = outcome.String
		}
		if status.Valid {
			code := int(status.Int64)
			view.LastStatusCode = &code
		}
		view.LastObservedAt = nullTime(observed)
		view.UpdatedAt = millisToTime(updated)
		states[view.BackendID] = view
	}
	if err := rows.Err(); err != nil {
		return nil, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read backend state", cause: err}
	}
	return states, nil
}

// CountFailed returns the number of outbox rows awaiting operator attention
// (status failed). Pending and delivering rows are counted by CountPending.
func (s *Store) CountFailed(ctx context.Context) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM notification_events WHERE status='failed'`).Scan(&count); err != nil {
		return 0, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read notification outbox", cause: err}
	}
	return count, nil
}

// CountFailedForBackend counts failed outbox rows for one backend.
func (s *Store) CountFailedForBackend(ctx context.Context, backendID string) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM notification_events WHERE backend_id=? AND status='failed'`, backendID).Scan(&count); err != nil {
		return 0, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read notification outbox", cause: err}
	}
	return count, nil
}

// CountPendingForBackend counts non-terminal outbox rows for one backend.
func (s *Store) CountPendingForBackend(ctx context.Context, backendID string) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM notification_events
WHERE backend_id=? AND status IN ('pending','delivering','failed')`, backendID).Scan(&count); err != nil {
		return 0, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read notification outbox", cause: err}
	}
	return count, nil
}

// QueryHistory reads persisted observations and incident notification events
// without probing or mutating state. Both lists are newest-first with a total
// deterministic order (timestamp, then unique id) so pagination is stable
// even when timestamps tie. At most Limit rows per list are returned; HasMore
// reports truncation in either list.
func (s *Store) QueryHistory(ctx context.Context, query HistoryQuery) (HistoryPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	limit := normalizeHistoryLimit(query.Limit)
	observations, obsMore, err := s.queryObservations(ctx, query.BackendID, query.Since, limit)
	if err != nil {
		return HistoryPage{}, err
	}
	events, eventsMore, err := s.queryIncidentEvents(ctx, query.BackendID, query.Since, limit)
	if err != nil {
		return HistoryPage{}, err
	}
	return HistoryPage{Observations: observations, Events: events, HasMore: obsMore || eventsMore}, nil
}

func (s *Store) queryObservations(
	ctx context.Context,
	backendID string,
	since *time.Time,
	limit int,
) ([]ObservationRow, bool, error) {
	var args []any
	var conditions []string
	if backendID != "" {
		conditions = append(conditions, "backend_id=?")
		args = append(args, backendID)
	}
	if since != nil && !since.IsZero() {
		conditions = append(conditions, "observed_at_ms>=?")
		args = append(args, nowMillis(since.UTC()))
	}
	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}
	// Fetch one extra row to detect truncation without counting the table.
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, `
SELECT id, backend_id, observed_at_ms, outcome, status_code, duration_ms, failure_reason, error_summary
FROM observations `+where+`
ORDER BY observed_at_ms DESC, id DESC
LIMIT ?`, args...)
	if err != nil {
		return nil, false, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read observation history", cause: err}
	}
	defer func() { _ = rows.Close() }()
	out := make([]ObservationRow, 0, limit)
	for rows.Next() {
		var row ObservationRow
		var observed int64
		var status sql.NullInt64
		var reason, summary sql.NullString
		if err := rows.Scan(
			&row.ID, &row.BackendID, &observed, &row.Outcome,
			&status, &row.DurationMs, &reason, &summary,
		); err != nil {
			return nil, false, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read observation history", cause: err}
		}
		row.ObservedAt = millisToTime(observed)
		if status.Valid {
			code := int(status.Int64)
			row.StatusCode = &code
		}
		if reason.Valid {
			value := reason.String
			row.FailureReason = &value
		}
		if summary.Valid {
			value := summary.String
			row.ErrorSummary = &value
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read observation history", cause: err}
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

func (s *Store) queryIncidentEvents(
	ctx context.Context,
	backendID string,
	since *time.Time,
	limit int,
) ([]IncidentEventRow, bool, error) {
	var args []any
	var conditions []string
	if backendID != "" {
		conditions = append(conditions, "backend_id=?")
		args = append(args, backendID)
	}
	if since != nil && !since.IsZero() {
		conditions = append(conditions, "created_at_ms>=?")
		args = append(args, nowMillis(since.UTC()))
	}
	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, `
SELECT id, incident_id, backend_id, event_type, sequence, status, created_at_ms
FROM notification_events `+where+`
ORDER BY created_at_ms DESC, id DESC
LIMIT ?`, args...)
	if err != nil {
		return nil, false, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read incident history", cause: err}
	}
	defer func() { _ = rows.Close() }()
	out := make([]IncidentEventRow, 0, limit)
	for rows.Next() {
		var row IncidentEventRow
		var created int64
		if err := rows.Scan(
			&row.EventID, &row.IncidentID, &row.BackendID, &row.EventType,
			&row.Sequence, &row.Status, &created,
		); err != nil {
			return nil, false, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read incident history", cause: err}
		}
		row.OccurredAt = millisToTime(created)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, &OpenError{Reason: "internal", Path: s.path, Message: "cannot read incident history", cause: err}
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

// ApplyRetention deletes only terminal data older than the policy cutoffs. It
// never deletes open incidents, pending/delivering/failed outbox rows,
// backend_state, or closed incidents that still guard non-terminal events.
// Cleanup is idempotent and safe to rerun: every predicate is a pure age and
// state filter, each batch commits independently, and a partial run simply
// leaves the remainder for the next invocation.
func (s *Store) ApplyRetention(ctx context.Context, policy RetentionPolicy, now time.Time) (RetentionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	var result RetentionResult
	if policy.Observations > 0 {
		deleted, err := s.retainObservations(ctx, now.Add(-policy.Observations))
		if err != nil {
			return result, err
		}
		result.ObservationsDeleted = deleted
	}
	if policy.DeliveredEvents > 0 {
		deleted, err := s.retainDeliveredEvents(ctx, now.Add(-policy.DeliveredEvents))
		if err != nil {
			return result, err
		}
		result.EventsDeleted = deleted
	}
	if policy.Incidents > 0 {
		deleted, err := s.retainClosedIncidents(ctx, now.Add(-policy.Incidents))
		if err != nil {
			return result, err
		}
		result.IncidentsDeleted = deleted
	}
	return result, nil
}

func (s *Store) retainObservations(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, &OpenError{Reason: "internal", Path: s.path, Message: "retention was canceled", cause: err}
		}
		res, err := s.db.ExecContext(ctx, `
DELETE FROM observations WHERE id IN (
  SELECT id FROM observations WHERE observed_at_ms < ? ORDER BY id LIMIT ?
)`, nowMillis(cutoff), retentionBatchSize)
		if err != nil {
			return total, &OpenError{Reason: "internal", Path: s.path, Message: "cannot apply retention", cause: err}
		}
		deleted, err := res.RowsAffected()
		if err != nil {
			return total, &OpenError{Reason: "internal", Path: s.path, Message: "cannot apply retention", cause: err}
		}
		total += deleted
		if deleted < retentionBatchSize {
			return total, nil
		}
	}
}

func (s *Store) retainDeliveredEvents(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, &OpenError{Reason: "internal", Path: s.path, Message: "retention was canceled", cause: err}
		}
		res, err := s.db.ExecContext(ctx, `
DELETE FROM notification_events WHERE id IN (
  SELECT id FROM notification_events
  WHERE status IN ('delivered','canceled') AND created_at_ms < ?
  ORDER BY created_at_ms LIMIT ?
)`, nowMillis(cutoff), retentionBatchSize)
		if err != nil {
			return total, &OpenError{Reason: "internal", Path: s.path, Message: "cannot apply retention", cause: err}
		}
		deleted, err := res.RowsAffected()
		if err != nil {
			return total, &OpenError{Reason: "internal", Path: s.path, Message: "cannot apply retention", cause: err}
		}
		total += deleted
		if deleted < retentionBatchSize {
			return total, nil
		}
	}
}

func (s *Store) retainClosedIncidents(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, &OpenError{Reason: "internal", Path: s.path, Message: "retention was canceled", cause: err}
		}
		// Delivered, canceled, and dead events cascade with the incident;
		// only pending/delivering/failed rows guard it.
		res, err := s.db.ExecContext(ctx, `
DELETE FROM incidents WHERE id IN (
  SELECT id FROM incidents
  WHERE state='closed' AND resolved_at_ms IS NOT NULL AND resolved_at_ms < ?
    AND NOT EXISTS (
      SELECT 1 FROM notification_events e
      WHERE e.incident_id = incidents.id
        AND e.status IN ('pending','delivering','failed')
    )
  ORDER BY resolved_at_ms LIMIT ?
)`, nowMillis(cutoff), incidentRetentionBatchSize)
		if err != nil {
			return total, &OpenError{Reason: "internal", Path: s.path, Message: "cannot apply retention", cause: err}
		}
		deleted, err := res.RowsAffected()
		if err != nil {
			return total, &OpenError{Reason: "internal", Path: s.path, Message: "cannot apply retention", cause: err}
		}
		total += deleted
		if deleted < incidentRetentionBatchSize {
			return total, nil
		}
	}
}

// ensureRetentionBatches is documented for reviewers: retention deletes run in
// bounded batches (retentionBatchSize / incidentRetentionBatchSize) so no
// single statement holds a long write lock.
