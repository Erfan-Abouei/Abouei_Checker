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

	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/incident"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/probe"
)

func testBackend(id string) config.Backend {
	return config.Backend{
		ID:                id,
		Name:              id,
		URL:               "http://127.0.0.1:1/" + id,
		Interval:          60 * time.Second,
		Timeout:           5 * time.Second,
		FailureThreshold:  3,
		RecoveryThreshold: 1,
		MaxResponseBytes:  1 << 20,
		Notifications: config.Policy{
			Enabled:          true,
			NotifyOnDown:     true,
			NotifyOnRecovery: true,
		},
	}
}

func failedResult(id string) probe.Result {
	return probe.Result{
		BackendID:     id,
		Name:          id,
		Outcome:       probe.OutcomeUnhealthy,
		HTTPStatus:    503,
		Duration:      42 * time.Millisecond,
		ObservedAt:    time.Now().UTC(),
		FailureReason: probe.ReasonStatusMismatch,
		Error:         "unexpected status code 503",
	}
}

func healthyResult(id string) probe.Result {
	return probe.Result{
		BackendID:  id,
		Name:       id,
		Outcome:    probe.OutcomeHealthy,
		HTTPStatus: 200,
		Duration:   21 * time.Millisecond,
		ObservedAt: time.Now().UTC(),
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "abouei.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestOpenCreatesDirectoriesAndSchema(t *testing.T) {
	store := openTestStore(t)
	if version, err := store.userVersion(); err != nil || version != SupportedSchemaVersion {
		t.Fatalf("user_version = %d, %v", version, err)
	}
	if err := store.verifyTables(); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "abouei.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	_ = second.Close()
}

func TestOpenSetsOwnerOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "abouei.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database mode = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %o, want 700", dirInfo.Mode().Perm())
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "abouei.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := store.DB().Exec("PRAGMA user_version=99"); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	_ = store.Close()
	_, err = Open(path)
	var openErr *OpenError
	if !errors.As(err, &openErr) || openErr.Reason != "schema_newer" {
		t.Fatalf("want schema_newer, got %#v", err)
	}
}

func TestOpenRejectsDirectoryPath(t *testing.T) {
	dir := t.TempDir()
	_, err := Open(dir)
	if err == nil {
		t.Fatal("expected an error for a directory path")
	}
}

func TestOpenRejectsInconsistentVersionZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "abouei.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE backend_state (backend_id TEXT PRIMARY KEY)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = db.Close()
	_, err = Open(path)
	var openErr *OpenError
	if !errors.As(err, &openErr) || openErr.Reason != "schema_inconsistent" {
		t.Fatalf("want schema_inconsistent, got %#v", err)
	}
}

func TestOpenRejectsGarbageFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "abouei.db")
	if err := os.WriteFile(path, []byte("not a sqlite file"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Open(path)
	if err == nil {
		t.Fatal("expected an error for a corrupt database")
	}
}

func TestSubThresholdFailurePersistsStreakOnly(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	outcome, err := store.ProcessResult(ctx, testBackend("api"), failedResult("api"), now)
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if outcome.Transition != incident.TransitionNone || outcome.IncidentID != "" {
		t.Fatalf("sub-threshold must not open: %+v", outcome)
	}
	if outcome.Failures != 1 || outcome.IncidentStatus != IncidentNone {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.EventID != "" {
		t.Fatalf("sub-threshold must not enqueue: %+v", outcome)
	}
	// Restart: streak survives.
	_ = store.Close()
	reopened, err := Open(store.Path())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	outcome, err = reopened.ProcessResult(ctx, testBackend("api"), failedResult("api"), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if outcome.Failures != 2 {
		t.Fatalf("streak must persist across restart, got %+v", outcome)
	}
}

func TestThresholdOpensIncidentWithDownEvent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	var outcome ProcessOutcome
	var err error
	for i := 0; i < 3; i++ {
		outcome, err = store.ProcessResult(ctx, backend, failedResult("api"), now.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatalf("ProcessResult: %v", err)
		}
	}
	if outcome.Transition != incident.TransitionOpened || outcome.IncidentStatus != IncidentOpen {
		t.Fatalf("threshold must open: %+v", outcome)
	}
	if outcome.EventType != incident.EventOpened || outcome.EventSequence != 1 || outcome.EventID == "" {
		t.Fatalf("Down event missing: %+v", outcome)
	}
	event, err := store.LoadEvent(ctx, outcome.EventID)
	if err != nil {
		t.Fatalf("LoadEvent: %v", err)
	}
	if event.Status != "pending" || event.AttemptCount != 0 {
		t.Fatalf("fresh event must be pending: %+v", event)
	}
	payload := string(event.Payload)
	if !strings.Contains(payload, `"event_type":"incident.opened"`) || !strings.Contains(payload, `"sequence":1`) {
		t.Fatalf("payload = %s", payload)
	}
	if strings.Contains(payload, "http://127.0.0.1") {
		t.Fatalf("payload leaks backend URL: %s", payload)
	}
	// Still failing: no duplicate Down.
	again, err := store.ProcessResult(ctx, backend, failedResult("api"), now.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if again.Transition != incident.TransitionStillOpen || again.EventID != "" {
		t.Fatalf("repeated failure must not duplicate Down: %+v", again)
	}
	if again.IncidentStatus != IncidentStillOpen {
		t.Fatalf("status = %q", again.IncidentStatus)
	}
}

func TestRecoveryClosesWithOneRecovery(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	backend.FailureThreshold = 2
	backend.RecoveryThreshold = 2
	for i := 0; i < 2; i++ {
		if _, err := store.ProcessResult(ctx, backend, failedResult("api"), now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("fail: %v", err)
		}
	}
	first, err := store.ProcessResult(ctx, backend, healthyResult("api"), now.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("healthy: %v", err)
	}
	if first.Transition != incident.TransitionNone || first.IncidentStatus != IncidentStillOpen {
		t.Fatalf("below recovery threshold stays open: %+v", first)
	}
	if first.EventID != "" {
		t.Fatalf("no event below recovery threshold: %+v", first)
	}
	recovered, err := store.ProcessResult(ctx, backend, healthyResult("api"), now.Add(11*time.Minute))
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered.Transition != incident.TransitionRecovered || recovered.IncidentStatus != IncidentRecovered {
		t.Fatalf("recovery must close: %+v", recovered)
	}
	if recovered.EventType != incident.EventRecovered || recovered.EventSequence != 2 {
		t.Fatalf("recovery sequence must follow Down: %+v", recovered)
	}
	// Repeated healthy probes: no duplicate recovery.
	again, err := store.ProcessResult(ctx, backend, healthyResult("api"), now.Add(12*time.Minute))
	if err != nil {
		t.Fatalf("healthy: %v", err)
	}
	if again.Transition != incident.TransitionNone || again.EventID != "" || again.IncidentStatus != IncidentNone {
		t.Fatalf("no duplicate recovery: %+v", again)
	}
	// New outage opens a new incident.
	second := backend
	for i := 0; i < 2; i++ {
		outcome, err := store.ProcessResult(ctx, second, failedResult("api"), now.Add(time.Duration(20+i)*time.Minute))
		if err != nil {
			t.Fatalf("fail: %v", err)
		}
		if i == 1 && (outcome.IncidentID == recovered.IncidentID || outcome.EventSequence != 1) {
			t.Fatalf("new outage needs a new incident with reset sequence: %+v", outcome)
		}
	}
}

func TestOneOpenIncidentEnforcedConcurrently(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	backend.FailureThreshold = 1
	done := make(chan ProcessOutcome, 8)
	for i := 0; i < 8; i++ {
		go func() {
			outcome, err := store.ProcessResult(ctx, backend, failedResult("api"), now)
			if err != nil {
				t.Errorf("ProcessResult: %v", err)
				return
			}
			done <- outcome
		}()
	}
	ids := map[string]int{}
	for i := 0; i < 8; i++ {
		outcome := <-done
		if outcome.IncidentID != "" {
			ids[outcome.IncidentID]++
		}
	}
	if len(ids) != 1 {
		t.Fatalf("must converge on one open incident, got %v", ids)
	}
	open, err := store.ListOpenIncidents(ctx)
	if err != nil {
		t.Fatalf("ListOpenIncidents: %v", err)
	}
	if len(open) != 1 || open[0].BackendID != "api" {
		t.Fatalf("open = %+v", open)
	}
}

func TestRestartWhileDownEmitsNoDuplicate(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	backend.FailureThreshold = 2
	for i := 0; i < 2; i++ {
		if _, err := store.ProcessResult(ctx, backend, failedResult("api"), now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("fail: %v", err)
		}
	}
	path := store.Path()
	_ = store.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	outcome, err := reopened.ProcessResult(ctx, backend, failedResult("api"), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("fail after restart: %v", err)
	}
	if outcome.Transition != incident.TransitionStillOpen || outcome.EventID != "" {
		t.Fatalf("restart must not duplicate Down: %+v", outcome)
	}
	if outcome.Failures != 3 {
		t.Fatalf("streak must continue across restart: %+v", outcome)
	}
}

func TestRestartWhileRecoveredEmitsOneRecovery(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	backend.FailureThreshold = 1
	opened, err := store.ProcessResult(ctx, backend, failedResult("api"), now)
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	path := store.Path()
	_ = store.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	recovered, err := reopened.ProcessResult(ctx, backend, healthyResult("api"), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered.Transition != incident.TransitionRecovered || recovered.IncidentID != opened.IncidentID {
		t.Fatalf("restart recovery must close the same incident: %+v", recovered)
	}
	if recovered.EventType != incident.EventRecovered {
		t.Fatalf("recovery must enqueue: %+v", recovered)
	}
}

func TestDisabledPolicyRecordsIncidentWithoutEvents(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	backend.FailureThreshold = 1
	backend.Notifications.Enabled = false
	opened, err := store.ProcessResult(ctx, backend, failedResult("api"), now)
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	if opened.Transition != incident.TransitionOpened || opened.EventID != "" {
		t.Fatalf("disabled policy: incident without event: %+v", opened)
	}
	recovered, err := store.ProcessResult(ctx, backend, healthyResult("api"), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered.Transition != incident.TransitionRecovered || recovered.EventID != "" {
		t.Fatalf("disabled policy: close without event: %+v", recovered)
	}
	pending, err := store.CountPending(ctx)
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	if pending != 0 {
		t.Fatalf("no outbox rows expected, got %d", pending)
	}
}

func TestCanceledProbeTouchesNothing(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	canceled := probe.Result{BackendID: "api", Outcome: probe.OutcomeCanceled, ObservedAt: time.Now().UTC()}
	outcome, err := store.ProcessResult(ctx, testBackend("api"), canceled, time.Now().UTC())
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if !outcome.Skipped {
		t.Fatalf("canceled must skip: %+v", outcome)
	}
	var observations int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&observations); err != nil {
		t.Fatalf("count: %v", err)
	}
	if observations != 0 {
		t.Fatalf("canceled must not insert observations: %d", observations)
	}
}

func TestObservationsRecordedWithSafeSummaries(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	if _, err := store.ProcessResult(ctx, backend, failedResult("api"), now); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	var outcome, reason, summary string
	var statusCode int
	err := store.DB().QueryRow(
		`SELECT outcome, status_code, failure_reason, error_summary FROM observations WHERE backend_id='api'`,
	).Scan(&outcome, &statusCode, &reason, &summary)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if outcome != "failed" || statusCode != 503 || reason != "status_mismatch" {
		t.Fatalf("observation row wrong: %q %d %q", outcome, statusCode, reason)
	}
	if summary != "unexpected status code 503" {
		t.Fatalf("summary = %q", summary)
	}
}
