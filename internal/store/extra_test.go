package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/incident"
)

// TestApplyOpenedRaceFallsBack verifies the concurrent-opener defense: when
// the incident INSERT hits the one-open-per-backend constraint, the
// transaction keeps the existing incident open instead of duplicating Down.
func TestApplyOpenedRaceFallsBack(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	backend.FailureThreshold = 1

	opened, err := store.ProcessResult(ctx, backend, failedResult("api"), now)
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if opened.Transition != incident.TransitionOpened {
		t.Fatalf("setup: %+v", opened)
	}

	tx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	decision := incident.Outcome{
		Streaks:    incident.Streaks{Failures: 1, FirstFailureAt: now},
		Transition: incident.TransitionOpened,
		IncidentID: incident.NewID(),
		EventType:  incident.EventOpened,
	}
	applied, err := store.applyOpened(ctx, tx, backend, failedResult("api"), decision, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("applyOpened: %v", err)
	}
	if applied.incidentID != opened.IncidentID || applied.eventID != "" {
		t.Fatalf("must reuse the open incident without a new event: %+v", applied)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var incidents int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM incidents`).Scan(&incidents); err != nil {
		t.Fatalf("count: %v", err)
	}
	if incidents != 1 {
		t.Fatalf("incidents = %d, want 1", incidents)
	}
}

func TestIsConstraintError(t *testing.T) {
	for _, message := range []string{
		"UNIQUE constraint failed: incidents.backend_id",
		"CHECK constraint failed: failure_count",
		"FOREIGN KEY constraint failed",
		"UNIQUE (incident_id, sequence)",
	} {
		if !isConstraintError(errors.New(message)) {
			t.Errorf("must detect %q", message)
		}
	}
	if isConstraintError(nil) || isConstraintError(errors.New("database is locked")) {
		t.Error("must not misclassify nil or lock errors")
	}
}

func TestEnsureBackendStateReconstructsFromOpenIncident(t *testing.T) {
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
	if _, err := store.DB().Exec(`DELETE FROM backend_state WHERE backend_id='api'`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	open, err := store.ListOpenIncidents(ctx)
	if err != nil || len(open) != 1 {
		t.Fatalf("open = %+v, %v", open, err)
	}
	reconstructed, err := store.EnsureBackendState(ctx, "api", &open[0])
	if err != nil {
		t.Fatalf("EnsureBackendState: %v", err)
	}
	if !reconstructed {
		t.Fatal("missing streak row must be reconstructed")
	}
	var failures int
	if err := store.DB().QueryRow(`SELECT consecutive_failures FROM backend_state WHERE backend_id='api'`).Scan(&failures); err != nil {
		t.Fatalf("select: %v", err)
	}
	if failures != open[0].FailureCount {
		t.Fatalf("rebuilt streak = %d, want %d", failures, open[0].FailureCount)
	}
	// Existing rows are left alone.
	again, err := store.EnsureBackendState(ctx, "api", nil)
	if err != nil || again {
		t.Fatalf("existing row must not be rebuilt: %v %v", again, err)
	}
}

func TestEnsureBackendStateCreatesFreshRow(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	rebuilt, err := store.EnsureBackendState(ctx, "new-backend", nil)
	if err != nil || !rebuilt {
		t.Fatalf("fresh row must be created: %v %v", rebuilt, err)
	}
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	_, err := Open("")
	var openErr *OpenError
	if !errors.As(err, &openErr) || openErr.Reason != "path" {
		t.Fatalf("want path error, got %#v", err)
	}
}

func TestOpenRejectsFileAsParent(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	// Create the blocker file first: the parent is not a directory.
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Open(blocker + "/abouei.db")
	var openErr *OpenError
	if !errors.As(err, &openErr) {
		t.Fatalf("want OpenError, got %#v", err)
	}
}

func TestMarksRequireDeliveringRow(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	backend.FailureThreshold = 1
	opened, err := store.ProcessResult(ctx, backend, failedResult("api"), now)
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	_ = opened
	// The fresh event is pending, not delivering: marks must fail closed.
	rows, err := store.DB().Query(`SELECT id FROM notification_events`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	var id string
	for rows.Next() {
		_ = rows.Scan(&id)
	}
	_ = rows.Close()
	if id == "" {
		t.Fatal("expected an event row")
	}
	for name, mark := range map[string]func() error{
		"delivered": func() error { return store.MarkDelivered(ctx, id, now) },
		"failed":    func() error { return store.MarkFailed(ctx, id, now, "x") },
		"dead":      func() error { return store.MarkDead(ctx, id, "x") },
	} {
		if err := mark(); err == nil {
			t.Errorf("Mark%s on a pending row must fail", name)
		}
	}
}

func TestLoadEventMissing(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.LoadEvent(context.Background(), "does-not-exist"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("want ErrNoRows, got %v", err)
	}
}

func TestOpenErrorMethods(t *testing.T) {
	var nilErr *OpenError
	if nilErr.Error() == "" || nilErr.Unwrap() != nil {
		t.Fatal("nil receiver must be safe")
	}
	wrapped := &OpenError{Reason: "internal", Message: "boom", cause: errors.New("cause")}
	if wrapped.Error() != "boom" || wrapped.Unwrap() == nil {
		t.Fatalf("methods = %q %v", wrapped.Error(), wrapped.Unwrap())
	}
	var nilStore *Store
	if nilStore.Path() != "" || nilStore.Close() != nil {
		t.Fatal("nil store must be safe")
	}
}

func TestMillisScannerRejectsUnexpectedTypes(t *testing.T) {
	var target millisScanner
	if err := target.Scan("not-a-number"); err == nil {
		t.Fatal("expected a scan error")
	}
	if err := target.Scan(42); err != nil {
		t.Fatalf("int must scan: %v", err)
	}
}

func TestLoadIncidentByIDMissing(t *testing.T) {
	store := openTestStore(t)
	tx, err := store.DB().Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	row, err := loadIncidentByIDTx(context.Background(), tx, "missing")
	if err != nil || row != nil {
		t.Fatalf("row = %+v, err = %v", row, err)
	}
}

func TestProcessResultAfterCloseFails(t *testing.T) {
	store := openTestStore(t)
	_ = store.Close()
	_, err := store.ProcessResult(context.Background(), testBackend("api"), failedResult("api"), time.Now().UTC())
	if err == nil {
		t.Fatal("expected an error on a closed store")
	}
}
