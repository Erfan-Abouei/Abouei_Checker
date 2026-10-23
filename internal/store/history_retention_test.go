package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/incident"
)

// backdateObservation moves one observation row to an absolute instant.
func backdateObservation(t *testing.T, s *Store, backendID string, instant time.Time) {
	t.Helper()
	if _, err := s.DB().Exec(
		`UPDATE observations SET observed_at_ms=? WHERE backend_id=?`,
		nowMillis(instant.UTC()), backendID,
	); err != nil {
		t.Fatalf("backdate observation: %v", err)
	}
}

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func TestOpenReadOnlyMissingHintsFirstRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "abouei.db")
	_, err := OpenReadOnly(path)
	var openErr *OpenError
	if !errors.As(err, &openErr) || openErr.Reason != "missing" {
		t.Fatalf("want missing, got %#v", err)
	}
	if !strings.Contains(openErr.Message, "run check or monitor first") {
		t.Fatalf("message = %q, want the first-run hint", openErr.Message)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("read-only open must not create the database file")
	}
}

func TestOpenReadOnlyRejectsWrites(t *testing.T) {
	store := openTestStore(t)
	path := store.Path()
	_ = store.Close()

	readonly, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() { _ = readonly.Close() }()
	if _, err := readonly.DB().Exec(
		`INSERT INTO observations (backend_id, observed_at_ms, outcome, duration_ms) VALUES ('x', 1, 'healthy', 1)`); err == nil {
		t.Fatal("read-only handle must refuse writes")
	}
	// The failed write must not have created a row.
	var count int
	if err := readonly.DB().QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("observations = %d, want 0", count)
	}
}

func TestOpenReadOnlyRejectsNewerSchema(t *testing.T) {
	store := openTestStore(t)
	path := store.Path()
	if _, err := store.DB().Exec("PRAGMA user_version=99"); err != nil {
		t.Fatalf("bump: %v", err)
	}
	_ = store.Close()
	_, err := OpenReadOnly(path)
	var openErr *OpenError
	if !errors.As(err, &openErr) || openErr.Reason != "schema_newer" {
		t.Fatalf("want schema_newer, got %#v", err)
	}
	if !strings.Contains(openErr.Message, "upgrade the binary") {
		t.Fatalf("message = %q, want upgrade guidance", openErr.Message)
	}
}

func TestOpenReadOnlyRequiresMigrationForOldVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "abouei.db")
	// A zero-byte file is a valid empty SQLite database with user_version 0.
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := OpenReadOnly(path)
	var openErr *OpenError
	if !errors.As(err, &openErr) || openErr.Reason != "migration" {
		t.Fatalf("want migration, got %#v", err)
	}
	if !strings.Contains(openErr.Message, "run check or monitor with write access") {
		t.Fatalf("message = %q, want the writable-command hint", openErr.Message)
	}
}

func TestOpenReadOnlyPreservesOperationalData(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	backend.FailureThreshold = 1
	opened, err := store.ProcessResult(ctx, backend, failedResult("api"), now)
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	path := store.Path()
	_ = store.Close()

	readonly, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() { _ = readonly.Close() }()

	states, err := readonly.GetBackendStates(ctx)
	if err != nil {
		t.Fatalf("GetBackendStates: %v", err)
	}
	if states["api"].ConsecutiveFailures != 1 {
		t.Fatalf("streak = %+v", states["api"])
	}
	open, err := readonly.ListOpenIncidents(ctx)
	if err != nil {
		t.Fatalf("ListOpenIncidents: %v", err)
	}
	if len(open) != 1 || open[0].ID != opened.IncidentID {
		t.Fatalf("open = %+v", open)
	}
	pending, err := readonly.CountPending(ctx)
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}
	page, err := readonly.QueryHistory(ctx, HistoryQuery{Limit: 10})
	if err != nil {
		t.Fatalf("QueryHistory: %v", err)
	}
	if len(page.Observations) != 1 || len(page.Events) != 1 {
		t.Fatalf("page = %+v observations, %+v events", page.Observations, page.Events)
	}
}

func TestReadOnlyDoesNotResetDelivering(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	backend.FailureThreshold = 1
	if _, err := store.ProcessResult(ctx, backend, failedResult("api"), now); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	claimed, err := store.ClaimDueEvent(ctx, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	path := store.Path()
	_ = store.Close()

	readonly, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	event, err := readonly.LoadEvent(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("LoadEvent: %v", err)
	}
	_ = readonly.Close()
	if event.Status != "delivering" {
		t.Fatalf("read-only open reset delivery to %q; status/history must never mutate", event.Status)
	}

	// The read-write open performs the documented stale-delivering reset.
	writer, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = writer.Close() }()
	event, err = writer.LoadEvent(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("LoadEvent: %v", err)
	}
	if event.Status != "pending" || event.AttemptCount != claimed.AttemptCount {
		t.Fatalf("stale reset = %+v, want pending with attempt %d", event, claimed.AttemptCount)
	}
}

func TestReadOnlyQueriesDoNotMutate(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	backend.FailureThreshold = 1
	if _, err := store.ProcessResult(ctx, backend, failedResult("api"), now); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	before := map[string]int{
		"backend_state":       countRows(t, store, "backend_state"),
		"incidents":           countRows(t, store, "incidents"),
		"observations":        countRows(t, store, "observations"),
		"notification_events": countRows(t, store, "notification_events"),
	}
	var versionBefore int
	if err := store.DB().QueryRow("PRAGMA user_version").Scan(&versionBefore); err != nil {
		t.Fatalf("version: %v", err)
	}
	path := store.Path()
	_ = store.Close()

	readonly, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	if _, err := readonly.GetBackendStates(ctx); err != nil {
		t.Fatalf("states: %v", err)
	}
	if _, err := readonly.ListOpenIncidents(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := readonly.QueryHistory(ctx, HistoryQuery{Limit: 50}); err != nil {
		t.Fatalf("history: %v", err)
	}
	if _, err := readonly.CountPending(ctx); err != nil {
		t.Fatalf("pending: %v", err)
	}
	if _, err := readonly.CountFailed(ctx); err != nil {
		t.Fatalf("failed: %v", err)
	}
	_ = readonly.Close()

	verify, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = verify.Close() }()
	for table, want := range before {
		if got := countRows(t, verify, table); got != want {
			t.Fatalf("%s = %d, want %d: read-only queries mutated state", table, got, want)
		}
	}
	var versionAfter int
	if err := verify.DB().QueryRow("PRAGMA user_version").Scan(&versionAfter); err != nil {
		t.Fatalf("version: %v", err)
	}
	if versionAfter != versionBefore {
		t.Fatalf("user_version = %d, want %d", versionAfter, versionBefore)
	}
}

func TestGetBackendStatesEmpty(t *testing.T) {
	store := openTestStore(t)
	states, err := store.GetBackendStates(context.Background())
	if err != nil {
		t.Fatalf("GetBackendStates: %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("states = %+v, want empty", states)
	}
}

func TestQueryHistoryEmpty(t *testing.T) {
	store := openTestStore(t)
	page, err := store.QueryHistory(context.Background(), HistoryQuery{Limit: 10})
	if err != nil {
		t.Fatalf("QueryHistory: %v", err)
	}
	if len(page.Observations) != 0 || len(page.Events) != 0 || page.HasMore {
		t.Fatalf("empty history must be valid with has_more=false: %+v", page)
	}
}

func seedHistory(t *testing.T, s *Store, backendID string, base time.Time, count int) {
	t.Helper()
	backend := testBackend(backendID)
	backend.FailureThreshold = 100 // never open incidents here
	for i := 0; i < count; i++ {
		result := failedResult(backendID)
		result.ObservedAt = base.Add(time.Duration(i) * time.Minute)
		if _, err := s.ProcessResult(context.Background(), backend, result, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func TestQueryHistoryNewestFirstWithLimitAndHasMore(t *testing.T) {
	store := openTestStore(t)
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	seedHistory(t, store, "api", base, 5)

	page, err := store.QueryHistory(context.Background(), HistoryQuery{Limit: 2})
	if err != nil {
		t.Fatalf("QueryHistory: %v", err)
	}
	if len(page.Observations) != 2 || !page.HasMore {
		t.Fatalf("limit 2 of 5 must return 2 rows with has_more: %+v", page)
	}
	if !page.Observations[0].ObservedAt.After(page.Observations[1].ObservedAt) {
		t.Fatalf("not newest-first: %+v", page.Observations)
	}
	wantNewest := base.Add(4 * time.Minute)
	if !page.Observations[0].ObservedAt.Equal(wantNewest) {
		t.Fatalf("newest = %v, want %v", page.Observations[0].ObservedAt, wantNewest)
	}

	full, err := store.QueryHistory(context.Background(), HistoryQuery{Limit: 50})
	if err != nil {
		t.Fatalf("QueryHistory: %v", err)
	}
	if len(full.Observations) != 5 || full.HasMore {
		t.Fatalf("full page = %d rows has_more=%v", len(full.Observations), full.HasMore)
	}
}

func TestQueryHistoryFilters(t *testing.T) {
	store := openTestStore(t)
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	seedHistory(t, store, "api", base, 3)
	seedHistory(t, store, "web", base, 2)

	filtered, err := store.QueryHistory(context.Background(), HistoryQuery{BackendID: "web", Limit: 50})
	if err != nil {
		t.Fatalf("QueryHistory: %v", err)
	}
	if len(filtered.Observations) != 2 {
		t.Fatalf("backend filter = %d rows, want 2", len(filtered.Observations))
	}
	for _, row := range filtered.Observations {
		if row.BackendID != "web" {
			t.Fatalf("row leaked backend %q", row.BackendID)
		}
	}

	since := base.Add(2 * time.Minute)
	ranged, err := store.QueryHistory(context.Background(), HistoryQuery{Since: &since, Limit: 50})
	if err != nil {
		t.Fatalf("QueryHistory: %v", err)
	}
	// api rows at +0,+1 excluded; api +2, web +0? no: web rows at +0,+1
	// excluded too. Remaining: api@+2 only.
	if len(ranged.Observations) != 1 || ranged.Observations[0].BackendID != "api" {
		t.Fatalf("since filter = %+v", ranged.Observations)
	}

	none, err := store.QueryHistory(context.Background(), HistoryQuery{BackendID: "missing", Limit: 50})
	if err != nil {
		t.Fatalf("QueryHistory: %v", err)
	}
	if len(none.Observations) != 0 || len(none.Events) != 0 || none.HasMore {
		t.Fatalf("unknown backend must be empty, not an error: %+v", none)
	}
}

func TestQueryHistoryDeterministicOrderOnTies(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	backend := testBackend("api")
	backend.FailureThreshold = 100
	instant := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		result := failedResult("api")
		result.ObservedAt = instant // identical timestamps
		if _, err := store.ProcessResult(ctx, backend, result, instant); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	first, err := store.QueryHistory(ctx, HistoryQuery{Limit: 50})
	if err != nil {
		t.Fatalf("QueryHistory: %v", err)
	}
	second, err := store.QueryHistory(ctx, HistoryQuery{Limit: 50})
	if err != nil {
		t.Fatalf("QueryHistory: %v", err)
	}
	if len(first.Observations) != 4 {
		t.Fatalf("rows = %d", len(first.Observations))
	}
	for i := range first.Observations {
		if first.Observations[i].ID != second.Observations[i].ID {
			t.Fatal("tied timestamps produced unstable pagination order")
		}
	}
}

func TestQueryHistoryLimitClamp(t *testing.T) {
	store := openTestStore(t)
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	seedHistory(t, store, "api", base, 3)
	// Zero means the documented default of 50, which holds all 3 rows.
	page, err := store.QueryHistory(context.Background(), HistoryQuery{Limit: 0})
	if err != nil {
		t.Fatalf("QueryHistory: %v", err)
	}
	if len(page.Observations) != 3 || page.HasMore {
		t.Fatalf("default limit page = %+v", page)
	}
	// Oversized limits clamp instead of erroring or loading unbounded rows.
	page, err = store.QueryHistory(context.Background(), HistoryQuery{Limit: 5000})
	if err != nil {
		t.Fatalf("QueryHistory: %v", err)
	}
	if len(page.Observations) != 3 {
		t.Fatalf("clamped page = %+v", page)
	}
}

func TestQueryHistoryContextCanceled(t *testing.T) {
	store := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.QueryHistory(ctx, HistoryQuery{Limit: 10}); err == nil {
		t.Fatal("canceled context must fail, not return success")
	}
}

func TestRetentionDeletesOnlyExpiredObservations(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	backend := testBackend("api")
	backend.FailureThreshold = 100
	old := failedResult("api")
	old.ObservedAt = now.Add(-800 * time.Hour)
	if _, err := store.ProcessResult(ctx, backend, old, old.ObservedAt); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	recent := failedResult("api")
	recent.ObservedAt = now.Add(-time.Hour)
	if _, err := store.ProcessResult(ctx, backend, recent, recent.ObservedAt); err != nil {
		t.Fatalf("seed recent: %v", err)
	}

	result, err := store.ApplyRetention(ctx, RetentionPolicy{
		Observations: 720 * time.Hour, Incidents: 4320 * time.Hour, DeliveredEvents: 168 * time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if result.ObservationsDeleted != 1 {
		t.Fatalf("deleted = %+v", result)
	}
	if got := countRows(t, store, "observations"); got != 1 {
		t.Fatalf("observations = %d, want 1", got)
	}
	// Backend state is never removed by routine cleanup.
	if got := countRows(t, store, "backend_state"); got != 1 {
		t.Fatalf("backend_state = %d, want 1", got)
	}
}

func TestRetentionNeverDeletesOpenIncidentsOrPendingEvents(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	backend := testBackend("api")
	backend.FailureThreshold = 1
	outcome, err := store.ProcessResult(ctx, backend, failedResult("api"), now.Add(-5000*time.Hour))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Age every row far beyond all windows.
	if _, err := store.DB().Exec(`UPDATE incidents SET opened_at_ms=?, first_failure_at_ms=?, updated_at_ms=? WHERE id=?`,
		nowMillis(now.Add(-5000*time.Hour)), nowMillis(now.Add(-5000*time.Hour)), nowMillis(now.Add(-5000*time.Hour)), outcome.IncidentID); err != nil {
		t.Fatalf("age incident: %v", err)
	}
	if _, err := store.DB().Exec(`UPDATE notification_events SET created_at_ms=?, updated_at_ms=? WHERE incident_id=?`,
		nowMillis(now.Add(-5000*time.Hour)), nowMillis(now.Add(-5000*time.Hour)), outcome.IncidentID); err != nil {
		t.Fatalf("age events: %v", err)
	}
	backdateObservation(t, store, "api", now.Add(-5000*time.Hour))

	result, err := store.ApplyRetention(ctx, RetentionPolicy{
		Observations: time.Hour, Incidents: 24 * time.Hour, DeliveredEvents: time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if result.IncidentsDeleted != 0 || result.EventsDeleted != 0 {
		t.Fatalf("open incident and pending event must survive: %+v", result)
	}
	if got := countRows(t, store, "incidents"); got != 1 {
		t.Fatalf("incidents = %d, want 1", got)
	}
	if got := countRows(t, store, "notification_events"); got != 1 {
		t.Fatalf("events = %d, want 1", got)
	}
}

func TestRetentionDeletesClosedIncidentsWithTerminalEvents(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	backend := testBackend("api")
	backend.FailureThreshold = 1
	opened, err := store.ProcessResult(ctx, backend, failedResult("api"), now.Add(-5000*time.Hour))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	claimed, err := store.ClaimDueEvent(ctx, now.Add(-5000*time.Hour))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.MarkDelivered(ctx, claimed.ID, now.Add(-5000*time.Hour)); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	closed, err := store.ProcessResult(ctx, backend, healthyResult("api"), now.Add(-4000*time.Hour))
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.Transition == incident.TransitionNone || closed.IncidentID == "" {
		t.Fatalf("must close: %+v", closed)
	}
	if closed.EventID != "" {
		recovery, err := store.ClaimDueEvent(ctx, now.Add(-4000*time.Hour))
		if err != nil {
			t.Fatalf("claim recovery: %v", err)
		}
		if err := store.MarkDelivered(ctx, recovery.ID, now.Add(-4000*time.Hour)); err != nil {
			t.Fatalf("deliver recovery: %v", err)
		}
	}
	// Age the closed incident and its terminal events beyond every window.
	if _, err := store.DB().Exec(`UPDATE incidents SET opened_at_ms=?, resolved_at_ms=?, updated_at_ms=? WHERE id=?`,
		nowMillis(now.Add(-5000*time.Hour)), nowMillis(now.Add(-4000*time.Hour)), nowMillis(now.Add(-4000*time.Hour)), opened.IncidentID); err != nil {
		t.Fatalf("age incident: %v", err)
	}
	if _, err := store.DB().Exec(`UPDATE notification_events SET created_at_ms=?, updated_at_ms=? WHERE incident_id=?`,
		nowMillis(now.Add(-5000*time.Hour)), nowMillis(now.Add(-4000*time.Hour)), opened.IncidentID); err != nil {
		t.Fatalf("age events: %v", err)
	}

	result, err := store.ApplyRetention(ctx, RetentionPolicy{
		Observations: 8760 * time.Hour, Incidents: 24 * time.Hour, DeliveredEvents: time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if result.IncidentsDeleted != 1 {
		t.Fatalf("closed incident must be removed: %+v", result)
	}
	if got := countRows(t, store, "incidents"); got != 0 {
		t.Fatalf("incidents = %d, want 0", got)
	}
	// Terminal events cascade with their incident.
	if got := countRows(t, store, "notification_events"); got != 0 {
		t.Fatalf("events = %d, want 0", got)
	}
	// Backend state still supports current status.
	if got := countRows(t, store, "backend_state"); got != 1 {
		t.Fatalf("backend_state = %d, want 1", got)
	}
}

func TestRetentionKeepsClosedIncidentGuardedByPendingEvent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	backend := testBackend("api")
	backend.FailureThreshold = 1
	backend.Notifications.Enabled = false // no Down event row
	opened, err := store.ProcessResult(ctx, backend, failedResult("api"), now.Add(-5000*time.Hour))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	backend.Notifications.Enabled = true // recovery enqueues a pending event
	recovered, err := store.ProcessResult(ctx, backend, healthyResult("api"), now.Add(-4000*time.Hour))
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if recovered.EventID == "" {
		t.Fatal("recovery must enqueue a pending event for this test")
	}
	if _, err := store.DB().Exec(`UPDATE incidents SET opened_at_ms=?, resolved_at_ms=?, updated_at_ms=? WHERE id=?`,
		nowMillis(now.Add(-5000*time.Hour)), nowMillis(now.Add(-4000*time.Hour)), nowMillis(now.Add(-4000*time.Hour)), opened.IncidentID); err != nil {
		t.Fatalf("age incident: %v", err)
	}

	result, err := store.ApplyRetention(ctx, RetentionPolicy{
		Observations: 8760 * time.Hour, Incidents: 24 * time.Hour, DeliveredEvents: time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if result.IncidentsDeleted != 0 {
		t.Fatalf("incident guarded by a pending event must survive: %+v", result)
	}
	if got := countRows(t, store, "incidents"); got != 1 {
		t.Fatalf("incidents = %d, want 1", got)
	}
}

func TestRetentionDeadEventsDoNotGuardIncident(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	backend := testBackend("api")
	backend.FailureThreshold = 1
	opened, err := store.ProcessResult(ctx, backend, failedResult("api"), now.Add(-5000*time.Hour))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	claimed, err := store.ClaimDueEvent(ctx, now.Add(-5000*time.Hour))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.MarkDead(ctx, claimed.ID, "permanent failure"); err != nil {
		t.Fatalf("dead: %v", err)
	}
	backend.Notifications.Enabled = false // close without a recovery event
	if _, err := store.ProcessResult(ctx, backend, healthyResult("api"), now.Add(-4000*time.Hour)); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := store.DB().Exec(`UPDATE incidents SET opened_at_ms=?, resolved_at_ms=?, updated_at_ms=? WHERE id=?`,
		nowMillis(now.Add(-5000*time.Hour)), nowMillis(now.Add(-4000*time.Hour)), nowMillis(now.Add(-4000*time.Hour)), opened.IncidentID); err != nil {
		t.Fatalf("age incident: %v", err)
	}

	result, err := store.ApplyRetention(ctx, RetentionPolicy{
		Observations: 8760 * time.Hour, Incidents: 24 * time.Hour, DeliveredEvents: 8760 * time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if result.IncidentsDeleted != 1 || countRows(t, store, "incidents") != 0 {
		t.Fatalf("dead events must not guard the incident: %+v", result)
	}
}

func TestRetentionIsIdempotent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	seedHistory(t, store, "api", now.Add(-800*time.Hour), 600)
	policy := RetentionPolicy{Observations: 720 * time.Hour, Incidents: 4320 * time.Hour, DeliveredEvents: 168 * time.Hour}
	first, err := store.ApplyRetention(ctx, policy, now)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.ObservationsDeleted != 600 {
		t.Fatalf("first deleted = %+v", first)
	}
	second, err := store.ApplyRetention(ctx, policy, now)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.ObservationsDeleted != 0 || second.EventsDeleted != 0 || second.IncidentsDeleted != 0 {
		t.Fatalf("rerun must delete nothing: %+v", second)
	}
}

func TestRetentionCanceledContext(t *testing.T) {
	store := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ApplyRetention(ctx, RetentionPolicy{Observations: time.Hour}, time.Now().UTC()); err == nil {
		t.Fatal("canceled retention must fail, not report success")
	}
}

func TestCountPendingFailedForBackend(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"api", "web"} {
		backend := testBackend(id)
		backend.FailureThreshold = 1
		if _, err := store.ProcessResult(ctx, backend, failedResult(id), now); err != nil {
			t.Fatalf("open %s: %v", id, err)
		}
	}
	claimed, err := store.ClaimDueEvent(ctx, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.MarkFailed(ctx, claimed.ID, now.Add(time.Minute), "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if got, err := store.CountFailedForBackend(ctx, claimed.BackendID); err != nil || got != 1 {
		t.Fatalf("failed for %s = %d, %v", claimed.BackendID, got, err)
	}
	if got, err := store.CountPendingForBackend(ctx, claimed.BackendID); err != nil || got != 1 {
		t.Fatalf("pending for %s = %d, %v", claimed.BackendID, got, err)
	}
	other := "web"
	if claimed.BackendID == "web" {
		other = "api"
	}
	if got, err := store.CountFailedForBackend(ctx, other); err != nil || got != 0 {
		t.Fatalf("failed for %s = %d, %v", other, got, err)
	}
}

func TestCorruptDatabaseFailsClosedWithoutOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "abouei.db")
	if err := os.WriteFile(path, []byte("not a sqlite file"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("read-write open of garbage must fail")
	}
	if _, err := OpenReadOnly(path); err == nil {
		t.Fatal("read-only open of garbage must fail")
	}
	// Nothing repaired or deleted the file: it still holds the same bytes.
	//nolint:gosec // test-only read of a temp-dir database file.
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(content) != "not a sqlite file" {
		t.Fatal("corrupt database was modified by a failed open")
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	store := openTestStore(t)
	defer func() { _ = store.Close() }()
	err := store.applyMigration(migration{version: 2, name: "bad test migration", ddl: "CREATE TABLE nope (;"})
	if err == nil {
		t.Fatal("bad migration must fail")
	}
	var openErr *OpenError
	if !errors.As(err, &openErr) || openErr.Reason != "migration" {
		t.Fatalf("want migration error, got %#v", err)
	}
	var version int
	if err := store.DB().QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("version: %v", err)
	}
	if version != SupportedSchemaVersion {
		t.Fatalf("user_version = %d after rollback, want %d", version, SupportedSchemaVersion)
	}
	var name string
	err = store.DB().QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='nope'").Scan(&name)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("partial migration table leaked: %q, %v", name, err)
	}
}

func TestOpenErrorRedactsSecrets(t *testing.T) {
	_, err := OpenReadOnly("")
	if err == nil || strings.Contains(err.Error(), "token") {
		t.Fatalf("err = %v", err)
	}
	_ = incident.TransitionNone // keep incident import if unused in future edits
}
