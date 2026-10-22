package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/incident"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/notify"
)

func openIncidentFixture(t *testing.T, store *Store, backendID string, threshold int) (ProcessOutcome, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	backend := testBackend(backendID)
	backend.FailureThreshold = threshold
	var outcome ProcessOutcome
	var err error
	for i := 0; i < threshold; i++ {
		outcome, err = store.ProcessResult(context.Background(), backend, failedResult(backendID), now.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatalf("fail: %v", err)
		}
	}
	if outcome.Transition != incident.TransitionOpened {
		t.Fatalf("no incident opened: %+v", outcome)
	}
	return outcome, now.Add(time.Duration(threshold) * time.Minute)
}

func TestReminderEnqueuedWhenDue(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	backend := testBackend("api")
	backend.FailureThreshold = 1
	backend.Notifications.ReminderInterval = time.Hour
	opened, now := openIncidentFixture(t, store, "api", 1)
	_ = opened
	// Not due yet: another failure only updates metadata.
	quiet, err := store.ProcessResult(ctx, backend, failedResult("api"), now)
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if quiet.ReminderEventID != "" {
		t.Fatalf("reminder before interval: %+v", quiet)
	}
	// The Down event blocks reminders until delivered.
	claimed, err := store.ClaimDueEvent(ctx, now)
	if err != nil {
		t.Fatalf("claim Down: %v", err)
	}
	if err := store.MarkDelivered(ctx, claimed.ID, now); err != nil {
		t.Fatalf("deliver Down: %v", err)
	}
	// Due: exactly one reminder.
	due, err := store.ProcessResult(ctx, backend, failedResult("api"), now.Add(61*time.Minute))
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if due.ReminderEventID == "" {
		t.Fatalf("due reminder missing: %+v", due)
	}
	event, err := store.LoadEvent(ctx, due.ReminderEventID)
	if err != nil {
		t.Fatalf("LoadEvent: %v", err)
	}
	if event.EventType != incident.EventReminder || event.Status != "pending" {
		t.Fatalf("reminder row = %+v", event)
	}
}

func TestReminderSuppressedWhileDownPending(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	backend := testBackend("api")
	backend.FailureThreshold = 1
	backend.Notifications.ReminderInterval = time.Minute
	_, now := openIncidentFixture(t, store, "api", 1)
	// The Down event is still pending, so even a due reminder must wait.
	waiting, err := store.ProcessResult(ctx, backend, failedResult("api"), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if waiting.ReminderEventID != "" {
		t.Fatalf("reminder must wait for Down delivery: %+v", waiting)
	}
	// After the Down event is delivered, the next due probe enqueues one.
	down, err := store.LoadEvent(ctx, waiting.IncidentID+"-missing")
	_ = down
	_ = err
	// Deliver the Down event directly through the claim path.
	claimed, err := store.ClaimDueEvent(ctx, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("ClaimDueEvent: %v", err)
	}
	if claimed.EventType != incident.EventOpened {
		t.Fatalf("oldest due must be Down: %+v", claimed)
	}
	if err := store.MarkDelivered(ctx, claimed.ID, now.Add(2*time.Hour)); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}
	ready, err := store.ProcessResult(ctx, backend, failedResult("api"), now.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if ready.ReminderEventID == "" {
		t.Fatalf("reminder should enqueue after Down delivered: %+v", ready)
	}
}

func TestReminderCapAndCancelOnRecovery(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	backend.FailureThreshold = 1
	backend.Notifications.ReminderInterval = time.Minute
	backend.Notifications.ReminderMax = 1
	if _, err := store.ProcessResult(ctx, backend, failedResult("api"), now); err != nil {
		t.Fatalf("fail: %v", err)
	}
	// Deliver Down so reminders are unblocked.
	claimed, err := store.ClaimDueEvent(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.MarkDelivered(ctx, claimed.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	first, err := store.ProcessResult(ctx, backend, failedResult("api"), now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if first.ReminderEventID == "" {
		t.Fatalf("first reminder missing: %+v", first)
	}
	// Cap reached: no second reminder even when due.
	second, err := store.ProcessResult(ctx, backend, failedResult("api"), now.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if second.ReminderEventID != "" {
		t.Fatalf("reminder beyond cap: %+v", second)
	}
	// Recovery cancels the queued reminder and enqueues Recovery.
	if err := func() error {
		_, err := store.ClaimDueEvent(ctx, now.Add(11*time.Minute))
		return err
	}(); err != nil {
		t.Fatalf("claim reminder: %v", err)
	}
	// Leave the reminder pending (do not deliver) to prove cancellation.
	recovered, err := store.ProcessResult(ctx, backend, healthyResult("api"), now.Add(12*time.Minute))
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered.Transition != incident.TransitionRecovered || recovered.EventType != incident.EventRecovered {
		t.Fatalf("recovery: %+v", recovered)
	}
	var statuses []string
	rows, err := store.DB().Query(
		`SELECT status FROM notification_events WHERE event_type='incident.reminder'`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatalf("scan: %v", err)
		}
		statuses = append(statuses, status)
	}
	for _, status := range statuses {
		if status == "pending" || status == "failed" {
			t.Fatalf("queued reminder must be canceled on recovery: %v", statuses)
		}
	}
}

func TestRestartEnqueuesAtMostOneOverdueReminder(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	backend.FailureThreshold = 1
	backend.Notifications.ReminderInterval = time.Minute
	if _, err := store.ProcessResult(ctx, backend, failedResult("api"), now); err != nil {
		t.Fatalf("fail: %v", err)
	}
	// Deliver Down before restart so the reminder is unblocked afterwards.
	claimed, err := store.ClaimDueEvent(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.MarkDelivered(ctx, claimed.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	path := store.Path()
	_ = store.Close()
	// Hours pass while the process is down: only one reminder may catch up.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	first, err := reopened.ProcessResult(ctx, backend, failedResult("api"), now.Add(5*time.Hour))
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if first.ReminderEventID == "" {
		t.Fatalf("overdue reminder missing after restart: %+v", first)
	}
	second, err := reopened.ProcessResult(ctx, backend, failedResult("api"), now.Add(5*time.Hour).Add(time.Second))
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if second.ReminderEventID != "" {
		t.Fatalf("only one overdue reminder after restart: %+v", second)
	}
}

func TestClaimOrderingAndDeadSkipping(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	backend.FailureThreshold = 1
	backend.Notifications.ReminderInterval = time.Minute
	if _, err := store.ProcessResult(ctx, backend, failedResult("api"), now); err != nil {
		t.Fatalf("fail: %v", err)
	}
	// Oldest due first: Down before anything else.
	first, err := store.ClaimDueEvent(ctx, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if first.EventType != incident.EventOpened || first.AttemptCount != 1 {
		t.Fatalf("claim = %+v", first)
	}
	// A second claim finds nothing: the Down row is delivering.
	if _, err := store.ClaimDueEvent(ctx, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("want ErrNoRows while delivering, got %v", err)
	}
	// Dead rows never block later sequences.
	if err := store.MarkDead(ctx, first.ID, "webhook returned status 400"); err != nil {
		t.Fatalf("dead: %v", err)
	}
	recovered, err := store.ProcessResult(ctx, backend, healthyResult("api"), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered.EventType == "" {
		t.Fatalf("recovery must enqueue despite dead Down: %+v", recovered)
	}
	next, err := store.ClaimDueEvent(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("claim recovery: %v", err)
	}
	if next.EventType != incident.EventRecovered {
		t.Fatalf("claim = %+v", next)
	}
}

func TestClaimIsAtomicUnderConcurrency(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	backend.FailureThreshold = 1
	if _, err := store.ProcessResult(ctx, backend, failedResult("api"), now); err != nil {
		t.Fatalf("fail: %v", err)
	}
	results := make(chan string, 8)
	for i := 0; i < 8; i++ {
		go func() {
			claimed, err := store.ClaimDueEvent(ctx, now)
			if err != nil {
				results <- "none"
				return
			}
			results <- "claimed:" + claimed.ID
		}()
	}
	wins := 0
	for i := 0; i < 8; i++ {
		if got := <-results; got != "none" {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("exactly one claim must win, winners = %d", wins)
	}
	delivered := 0
	rows, err := store.DB().Query(`SELECT status FROM notification_events`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if status == "delivering" {
			delivered++
		}
	}
	if delivered != 1 {
		t.Fatalf("exactly one claim must win, delivering rows = %d", delivered)
	}
}

func TestMarkFailedSchedulesRetry(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	backend := testBackend("api")
	backend.FailureThreshold = 1
	if _, err := store.ProcessResult(ctx, backend, failedResult("api"), now); err != nil {
		t.Fatalf("fail: %v", err)
	}
	claimed, err := store.ClaimDueEvent(ctx, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	next := now.Add(30 * time.Second)
	if err := store.MarkFailed(ctx, claimed.ID, next, "webhook returned status 500"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if _, err := store.ClaimDueEvent(ctx, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("not due before backoff, got %v", err)
	}
	again, err := store.ClaimDueEvent(ctx, next)
	if err != nil {
		t.Fatalf("due after backoff: %v", err)
	}
	if again.AttemptCount != 2 {
		t.Fatalf("attempt count = %d", again.AttemptCount)
	}
	row, err := store.LoadEvent(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("LoadEvent: %v", err)
	}
	if row.Status != "delivering" || row.AttemptCount != 2 {
		t.Fatalf("row = %+v", row)
	}
	_ = notify.BoundError("x")
}
