package notify

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
)

func downPayloadInput() PayloadInput {
	now := time.Now().UTC()
	lastError := "unexpected status code 503"
	statusCode := 503
	lastFailure := now.Add(-time.Minute)
	return PayloadInput{
		EventID:    "8a4d8f8e-7a41-4ac0-9f39-4e1f72d1a0d1",
		EventType:  "incident.opened",
		Sequence:   1,
		OccurredAt: now,
		Backend:    BackendRef{ID: "api", Name: "Example API"},
		Incident: IncidentSnapshot{
			ID:             "7c2c1f5e-42f2-4f96-9b1d-30a5d70fbd0f",
			Status:         "down",
			OpenedAt:       FormatTime(now),
			FirstFailureAt: FormatTime(now.Add(-3 * time.Minute)),
			FailureCount:   3,
			LastFailureAt:  FormatOptionalTime(&lastFailure),
			LastObservedAt: FormatTime(now),
			LastStatusCode: &statusCode,
			LastError:      &lastError,
			DurationMs:     42,
		},
	}
}

func TestBuildPayloadDown(t *testing.T) {
	encoded, err := BuildPayload(downPayloadInput())
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	document := string(encoded)
	for _, want := range []string{
		`"schema_version":"1"`,
		`"event_type":"incident.opened"`,
		`"sequence":1`,
		`"status":"down"`,
		`"failure_count":3`,
		`"duration_ms":42`,
		`"reminder":null`,
		`"resolved_at":null`,
	} {
		if !strings.Contains(document, want) {
			t.Errorf("payload missing %s:\n%s", want, document)
		}
	}
	for _, forbidden := range []string{"http://", "https://", "Authorization", "secret", "token"} {
		if strings.Contains(strings.ToLower(document), forbidden) {
			t.Errorf("payload must not contain %q:\n%s", forbidden, document)
		}
	}
	// Compact: exactly one line.
	if strings.Contains(document, "\n") {
		t.Errorf("payload must be compact JSON:\n%s", document)
	}
}

func TestBuildPayloadReminderAndRecovery(t *testing.T) {
	base := downPayloadInput()
	at := time.Now().UTC()
	base.EventType = "incident.reminder"
	base.Sequence = 2
	base.ReminderAt = &at
	encoded, err := BuildPayload(base)
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	if !strings.Contains(string(encoded), `"scheduled_at"`) {
		t.Errorf("reminder payload missing scheduled_at:\n%s", encoded)
	}

	base.EventType = "incident.recovered"
	base.Sequence = 3
	base.ReminderAt = nil
	base.Incident.Status = "healthy"
	resolved := FormatTime(at)
	base.Incident.ResolvedAt = &resolved
	base.Incident.RecoveryCount = 1
	base.Incident.LastError = nil
	encoded, err = BuildPayload(base)
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	if !strings.Contains(string(encoded), `"status":"healthy"`) {
		t.Errorf("recovery payload:\n%s", encoded)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		status int
		err    error
		want   Verdict
	}{
		{200, nil, VerdictDelivered},
		{204, nil, VerdictDelivered},
		{201, nil, VerdictDelivered},
		{408, nil, VerdictRetryable},
		{425, nil, VerdictRetryable},
		{429, nil, VerdictRetryable},
		{500, nil, VerdictRetryable},
		{503, nil, VerdictRetryable},
		{301, nil, VerdictPermanent},
		{400, nil, VerdictPermanent},
		{401, nil, VerdictPermanent},
		{404, nil, VerdictPermanent},
		{0, errors.New("connection refused"), VerdictRetryable},
		{0, context.DeadlineExceeded, VerdictRetryable},
		{0, context.Canceled, VerdictRetryable},
		{0, errors.New("tls: bad certificate"), VerdictRetryable},
		{0, errWebhookRedirect, VerdictPermanent},
	}
	for _, tc := range cases {
		if got := Classify(tc.status, tc.err); got != tc.want {
			t.Errorf("Classify(%d, %v) = %v, want %v", tc.status, tc.err, got, tc.want)
		}
	}
}

func TestBackoffBounds(t *testing.T) {
	initial := 5 * time.Second
	maximum := 5 * time.Minute
	for attempt := 1; attempt <= 10; attempt++ {
		delay := BackoffDelay(attempt, initial, maximum, func(n int64) int64 { return n - 1 })
		limit := initial << (attempt - 1)
		if limit > maximum || limit <= 0 {
			limit = maximum
		}
		if delay < 0 || delay >= limit {
			t.Errorf("attempt %d: delay %s outside [0, %s)", attempt, delay, limit)
		}
	}
	fixed := BackoffDelay(1, initial, maximum, func(int64) int64 { return 0 })
	if fixed != time.Millisecond {
		t.Errorf("zero jitter must clamp to 1ms, got %s", fixed)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Now().UTC()
	delay, ok := ParseRetryAfter("120", now)
	if !ok || delay != 120*time.Second {
		t.Errorf("seconds = %s, %v", delay, ok)
	}
	future := now.Add(90 * time.Second).UTC().Format(http.TimeFormat)
	delay, ok = ParseRetryAfter(future, now)
	if !ok || delay < 89*time.Second || delay > 90*time.Second {
		t.Errorf("date = %s, %v", delay, ok)
	}
	for _, bad := range []string{"", "soon", "-5"} {
		if _, ok := ParseRetryAfter(bad, now); ok {
			t.Errorf("header %q must be invalid", bad)
		}
	}
}

func TestBoundError(t *testing.T) {
	short := "webhook returned status 500"
	if BoundError(short) != short {
		t.Errorf("short message changed: %q", BoundError(short))
	}
	long := strings.Repeat("é", 300) // 600 bytes, multibyte runes
	bounded := BoundError(long)
	if len(bounded) > 512 {
		t.Errorf("bounded length = %d", len(bounded))
	}
	if !utf8.ValidString(bounded) {
		t.Errorf("bounded message is not valid UTF-8")
	}
}

func TestSafeSummaryRedactsEverything(t *testing.T) {
	const exampleHookURL = "https://hooks.example.com/x?token=abc123"
	summary := SafeSummary(0, errors.New("dial "+exampleHookURL), 10*time.Second)
	if strings.Contains(summary, "abc123") || strings.Contains(summary, "hooks.example") {
		t.Errorf("summary leaks URL: %q", summary)
	}
	if summary != "webhook delivery failed" && !strings.Contains(summary, "connection") {
		t.Errorf("summary = %q", summary)
	}
	if got := SafeSummary(200, nil, time.Second); got != "webhook delivered" {
		t.Errorf("summary = %q", got)
	}
	if got := SafeSummary(503, nil, time.Second); !strings.Contains(got, "503") {
		t.Errorf("summary = %q", got)
	}
	if got := SafeSummary(0, errWebhookRedirect, time.Second); !strings.Contains(got, "redirect") {
		t.Errorf("summary = %q", got)
	}
}

func TestSenderPostsPayloadWithIdempotencyKey(t *testing.T) {
	const secret = "notify-test-secret"
	var gotKey, gotAuth, gotBody string
	var gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Idempotency-Key")
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewSender()
	defer sender.client.CloseIdleConnections()
	encoded, _ := BuildPayload(downPayloadInput())
	result := sender.Send(context.Background(), WebhookConfig{
		URL:     server.URL,
		Headers: []config.Header{{Name: "Authorization", Value: "Bearer " + secret}},
		Timeout: 5 * time.Second,
	}, "event-1", encoded)
	if result.Err != nil || result.StatusCode != 200 {
		t.Fatalf("Send = %+v", result)
	}
	if gotKey != "event-1" {
		t.Errorf("Idempotency-Key = %q", gotKey)
	}
	if gotAuth != "Bearer "+secret {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	if !strings.Contains(gotBody, `"incident.opened"`) {
		t.Errorf("body = %q", gotBody)
	}
}

func TestSenderRejectsRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	sender := NewSender()
	defer sender.client.CloseIdleConnections()
	encoded, _ := BuildPayload(downPayloadInput())
	result := sender.Send(context.Background(), WebhookConfig{URL: redirector.URL, Timeout: 5 * time.Second}, "event-1", encoded)
	if result.Err == nil {
		t.Fatal("redirect must fail the attempt")
	}
	if Classify(result.StatusCode, result.Err) != VerdictPermanent {
		t.Errorf("redirect must be permanent: %+v", result)
	}
}

// fakeOutbox is an in-memory Outbox for worker tests.
type fakeOutbox struct {
	mu     sync.Mutex
	events []*ClaimedEvent
	// failClaimOnce injects a transient store error on the next claim.
	failClaimOnce bool
	marked        map[string]string
	attempts      map[string]int
	nextDue       map[string]time.Time
}

func newFakeOutbox(events ...*ClaimedEvent) *fakeOutbox {
	return &fakeOutbox{
		events:   append([]*ClaimedEvent(nil), events...),
		marked:   map[string]string{},
		attempts: map[string]int{},
		nextDue:  map[string]time.Time{},
	}
}

func (f *fakeOutbox) ClaimDueEvent(_ context.Context, now time.Time) (*ClaimedEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failClaimOnce {
		f.failClaimOnce = false
		return nil, errors.New("database locked")
	}
	for _, event := range f.events {
		if f.marked[event.ID] != "" {
			continue
		}
		if due, ok := f.nextDue[event.ID]; ok && due.After(now) {
			continue
		}
		f.marked[event.ID] = "delivering"
		event.AttemptCount++
		f.attempts[event.ID] = event.AttemptCount
		return event, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeOutbox) MarkDelivered(_ context.Context, id string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked[id] = "delivered"
	return nil
}

func (f *fakeOutbox) MarkFailed(_ context.Context, id string, next time.Time, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked[id] = ""
	f.nextDue[id] = next
	return nil
}

func (f *fakeOutbox) MarkDead(_ context.Context, id string, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked[id] = "dead"
	return nil
}

func testClaimed(id, eventType string) *ClaimedEvent {
	encoded, _ := BuildPayload(downPayloadInput())
	return &ClaimedEvent{ID: id, IncidentID: "incident-1", BackendID: "api", EventType: eventType, Sequence: 1, Payload: encoded}
}

func TestWorkerDeliversAndStabilizesID(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Idempotency-Key") == "" {
			t.Errorf("missing Idempotency-Key")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	_ = server

	outbox := newFakeOutbox(testClaimed("event-1", "incident.opened"))
	worker := NewWorker(WorkerOptions{
		Outbox: outbox,
		Webhook: WebhookConfig{
			URL:     "http://127.0.0.1:1/hook",
			Timeout: 2 * time.Second,
		},
		Retry: RetryConfig{MaxAttempts: 3, InitialBackoff: time.Second, MaxBackoff: time.Minute},
	})
	// Point the worker at the scripted server through a stub sender so the
	// event ID stays stable across attempts without depending on httptest
	// flakiness.
	calls := 0
	worker.send = func(_ context.Context, _ WebhookConfig, eventID string, _ []byte) AttemptResult {
		calls++
		if eventID != "event-1" {
			t.Errorf("event ID changed across attempts: %q", eventID)
		}
		if calls == 1 {
			return AttemptResult{Err: errors.New("connection refused")}
		}
		return AttemptResult{StatusCode: 200}
	}
	attempted, failed, err := worker.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if attempted != 1 || failed != 1 {
		t.Fatalf("first pass: attempted=%d failed=%d", attempted, failed)
	}
	// The retry is not due yet; force it due and process again. The event ID
	// must stay stable across the retry.
	outbox.mu.Lock()
	outbox.nextDue["event-1"] = time.Now().Add(-time.Second)
	outbox.mu.Unlock()
	attempted, failed, err = worker.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if attempted != 1 || failed != 0 {
		t.Fatalf("second pass: attempted=%d failed=%d", attempted, failed)
	}
	if outbox.marked["event-1"] != "delivered" {
		t.Fatalf("event state = %v", outbox.marked)
	}
}

func TestWorkerDeadAfterMaxAttempts(t *testing.T) {
	outbox := newFakeOutbox(testClaimed("event-1", "incident.opened"))
	worker := NewWorker(WorkerOptions{
		Outbox: outbox,
		Send: func(_ context.Context, _ WebhookConfig, _ string, _ []byte) AttemptResult {
			return AttemptResult{StatusCode: 500}
		},
		Webhook: WebhookConfig{URL: "http://127.0.0.1:1/hook", Timeout: time.Second},
		Retry:   RetryConfig{MaxAttempts: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
	})
	_, failed, err := worker.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if failed != 1 {
		t.Fatalf("first pass failed = %d", failed)
	}
	// Force the scheduled retry due; the second failure exhausts attempts.
	outbox.mu.Lock()
	outbox.nextDue["event-1"] = time.Now().Add(-time.Second)
	outbox.mu.Unlock()
	_, failed, err = worker.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if failed != 1 {
		t.Fatalf("second pass failed = %d", failed)
	}
	if outbox.marked["event-1"] != "dead" {
		t.Fatalf("event state = %v", outbox.marked)
	}
}

func TestWorkerPermanentFailureIsDead(t *testing.T) {
	outbox := newFakeOutbox(testClaimed("event-1", "incident.opened"))
	worker := NewWorker(WorkerOptions{
		Outbox: outbox,
		Send: func(_ context.Context, _ WebhookConfig, _ string, _ []byte) AttemptResult {
			return AttemptResult{StatusCode: 400}
		},
		Webhook: WebhookConfig{URL: "http://127.0.0.1:1/hook", Timeout: time.Second},
		Retry:   RetryConfig{MaxAttempts: 5, InitialBackoff: time.Second, MaxBackoff: time.Minute},
	})
	_, failed, err := worker.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if failed != 1 || outbox.marked["event-1"] != "dead" {
		t.Fatalf("failed=%d state=%v", failed, outbox.marked)
	}
}

// alwaysDueOutbox keeps returning the event as due even right after a
// failure was recorded. It models the production race where a short retry
// backoff elapses while the drain loop is still running.
type alwaysDueOutbox struct {
	event    *ClaimedEvent
	attempts int
	terminal string
}

func (f *alwaysDueOutbox) ClaimDueEvent(_ context.Context, _ time.Time) (*ClaimedEvent, error) {
	if f.terminal != "" {
		return nil, sql.ErrNoRows
	}
	f.attempts++
	claimed := *f.event
	claimed.AttemptCount = f.attempts
	return &claimed, nil
}

func (f *alwaysDueOutbox) MarkDelivered(_ context.Context, _ string, _ time.Time) error {
	f.terminal = "delivered"
	return nil
}

func (f *alwaysDueOutbox) MarkFailed(_ context.Context, _ string, _ time.Time, _ string) error {
	return nil
}

func (f *alwaysDueOutbox) MarkDead(_ context.Context, _ string, _ string) error {
	f.terminal = "dead"
	return nil
}

func TestWorkerAttemptsEachEventOncePerDrain(t *testing.T) {
	outbox := &alwaysDueOutbox{event: testClaimed("event-1", "incident.opened")}
	worker := NewWorker(WorkerOptions{
		Outbox: outbox,
		Send: func(_ context.Context, _ WebhookConfig, _ string, _ []byte) AttemptResult {
			return AttemptResult{Err: errors.New("connection refused")}
		},
		Webhook: WebhookConfig{URL: "http://127.0.0.1:1/hook", Timeout: time.Second},
		Retry:   RetryConfig{MaxAttempts: 5, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
	})
	attempted, failed, err := worker.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if attempted != 1 || failed != 1 {
		t.Fatalf("attempted=%d failed=%d, want exactly one attempt per drain", attempted, failed)
	}
	if outbox.terminal != "" {
		t.Fatalf("event must stay pending for the next drain, terminal=%q", outbox.terminal)
	}
}

func TestWorkerHonorsRetryAfter(t *testing.T) {
	outbox := newFakeOutbox(testClaimed("event-1", "incident.opened"))
	worker := NewWorker(WorkerOptions{
		Outbox: outbox,
		Send: func(_ context.Context, _ WebhookConfig, _ string, _ []byte) AttemptResult {
			return AttemptResult{StatusCode: 429, RetryAfter: "120"}
		},
		Webhook: WebhookConfig{URL: "http://127.0.0.1:1/hook", Timeout: time.Second},
		Retry:   RetryConfig{MaxAttempts: 5, InitialBackoff: time.Second, MaxBackoff: 5 * time.Minute},
	})
	_, _, err := worker.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if due := outbox.nextDue["event-1"]; time.Until(due) < 100*time.Second {
		t.Fatalf("Retry-After not honored: due in %s", time.Until(due))
	}
}

func TestWorkerRunStopsOnCancel(t *testing.T) {
	outbox := newFakeOutbox()
	worker := NewWorker(WorkerOptions{
		Outbox:  outbox,
		Webhook: WebhookConfig{URL: "http://127.0.0.1:1/hook", Timeout: time.Second},
		Retry:   RetryConfig{MaxAttempts: 3, InitialBackoff: time.Second, MaxBackoff: time.Minute},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	worker.Poke()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

func TestRetryConfigDefaults(t *testing.T) {
	retry := RetryConfigFrom(config.Retry{})
	if retry.MaxAttempts < 1 || retry.InitialBackoff <= 0 || retry.MaxBackoff < retry.InitialBackoff {
		t.Fatalf("defaults = %+v", retry)
	}
}
