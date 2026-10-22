package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// webhookCapture records webhook deliveries for assertions.
type webhookCapture struct {
	mu       sync.Mutex
	requests []capturedRequest
	status   int
}

type capturedRequest struct {
	idempotencyKey string
	authorization  string
	body           string
}

func (w *webhookCapture) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		_ = request.Body.Close()
		w.mu.Lock()
		w.requests = append(w.requests, capturedRequest{
			idempotencyKey: request.Header.Get("Idempotency-Key"),
			authorization:  request.Header.Get("Authorization"),
			body:           string(body),
		})
		w.mu.Unlock()
		writer.WriteHeader(w.status)
	}
}

func (w *webhookCapture) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.requests)
}

func (w *webhookCapture) bodies() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	bodies := make([]string, len(w.requests))
	for i, request := range w.requests {
		bodies[i] = request.body
	}
	return bodies
}

func durableConfig(t *testing.T, backendURL, webhookURL string) string {
	t.Helper()
	return `version: 1
storage:
  database_path: ./data/abouei.db
notifications:
  webhook:
    url: ` + webhookURL + `
    timeout: 2s
  retry:
    max_attempts: 3
    initial_backoff: 100ms
    max_backoff: 1s
backends:
  - id: flaky
    url: ` + backendURL + `
    interval: 60s
    timeout: 5s
    failure_threshold: 2
    recovery_threshold: 1
    notifications:
      enabled: true
`
}

func writeDurableConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "abouei.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func loadDurableApp(t *testing.T, configPath string) *App {
	t.Helper()
	var logs bytes.Buffer
	application := newTestApp(t, Options{ConfigPath: configPath, LogWriter: &logs})
	if _, err := application.LoadConfig(); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return application
}

func TestCheckPersistsStreaksAcrossOneShotRuns(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	hook := &webhookCapture{status: http.StatusOK}
	webhook := httptest.NewServer(hook.handler(t))
	defer webhook.Close()

	path := writeDurableConfig(t, durableConfig(t, backend.URL, webhook.URL+"/hook"))

	first := loadDurableApp(t, path)
	_, firstSummary, err := first.Check(context.Background(), nil)
	if err != nil {
		t.Fatalf("first check: %v", err)
	}
	if firstSummary.IncidentsOpened != 0 || firstSummary.Unhealthy != 1 {
		t.Fatalf("first summary = %+v", firstSummary)
	}

	// A new process (new App) against the same database continues the streak
	// and opens exactly one incident on the second failure.
	second := loadDurableApp(t, path)
	results, secondSummary, err := second.Check(context.Background(), nil)
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	if secondSummary.IncidentsOpened != 1 || secondSummary.Unhealthy != 1 {
		t.Fatalf("second summary = %+v", secondSummary)
	}
	if results[0].IncidentStatus != "open" || !results[0].ThresholdReached {
		t.Fatalf("result = %+v", results[0])
	}
	if hook.count() != 1 {
		t.Fatalf("Down must be delivered once, got %d", hook.count())
	}
	if !strings.Contains(hook.bodies()[0], `"event_type":"incident.opened"`) {
		t.Fatalf("Down payload = %s", hook.bodies()[0])
	}

	// A third failing run updates metadata but duplicates nothing.
	third := loadDurableApp(t, path)
	_, thirdSummary, err := third.Check(context.Background(), nil)
	if err != nil {
		t.Fatalf("third check: %v", err)
	}
	if thirdSummary.IncidentsOpened != 0 || hook.count() != 1 {
		t.Fatalf("third summary = %+v, deliveries = %d", thirdSummary, hook.count())
	}
}

func TestCheckRecoveryAcrossRestarts(t *testing.T) {
	failures := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		failures++
		if failures <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	hook := &webhookCapture{status: http.StatusOK}
	webhook := httptest.NewServer(hook.handler(t))
	defer webhook.Close()

	path := writeDurableConfig(t, durableConfig(t, backend.URL, webhook.URL+"/hook"))

	for i := 0; i < 2; i++ {
		application := loadDurableApp(t, path)
		if _, _, err := application.Check(context.Background(), nil); err != nil {
			t.Fatalf("failing check %d: %v", i, err)
		}
	}
	if hook.count() != 1 {
		t.Fatalf("deliveries = %d, want 1 Down", hook.count())
	}

	recovering := loadDurableApp(t, path)
	results, summary, err := recovering.Check(context.Background(), nil)
	if err != nil {
		t.Fatalf("recovery check: %v", err)
	}
	if summary.IncidentsClosed != 1 || results[0].IncidentStatus != "recovered" {
		t.Fatalf("summary = %+v result = %+v", summary, results[0])
	}
	if hook.count() != 2 {
		t.Fatalf("deliveries = %d, want Down + Recovery", hook.count())
	}
	body := hook.bodies()[1]
	if !strings.Contains(body, `"event_type":"incident.recovered"`) {
		t.Fatalf("Recovery payload = %s", body)
	}
	// Same incident, next sequence, stable deduplication identity per event.
	if !strings.Contains(hook.bodies()[0], `"sequence":1`) || !strings.Contains(body, `"sequence":2`) {
		t.Fatalf("sequences wrong:\n%s\n%s", hook.bodies()[0], body)
	}
}

func TestCheckNotificationFailureReportedDurably(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer backend.Close()

	// Webhook endpoint refuses everything: Down is queued and stays queued.
	path := writeDurableConfig(t, durableConfig(t, backend.URL, "http://127.0.0.1:1/hook"))
	application := loadDurableApp(t, path)
	application.config.Backends[0].FailureThreshold = 1

	_, summary, err := application.Check(context.Background(), nil)
	if err != nil {
		t.Fatalf("check must not fail on delivery errors: %v", err)
	}
	if summary.IncidentsOpened != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.NotificationsAttempted != 1 || summary.NotificationsFailed != 1 {
		t.Fatalf("delivery must be attempted and recorded failed: %+v", summary)
	}
	// The failed event remains retryable in the outbox.
	db, err := application.OpenStore()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() { _ = db.Close() }()
	pending, err := db.CountPending(context.Background())
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	if pending != 1 {
		t.Fatalf("failed event must stay queued, pending = %d", pending)
	}
}

func TestCheckDatabaseFailureIsInternal(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	content := "version: 1\nstorage:\n  database_path: " + blocker + "/abouei.db\nbackends:\n  - id: ok\n    url: " + backend.URL + "\n"
	path := writeDurableConfig(t, content)
	application := loadDurableApp(t, path)
	_, _, checkErr := application.Check(context.Background(), nil)
	if checkErr == nil {
		t.Fatal("expected a database error")
	}
	err := checkErr
	if CategoryOf(err) != CategoryInternal || CodeOf(err) != CodeDatabaseUnavailable {
		t.Fatalf("err = %+v", err)
	}
}

func TestCheckUnknownBackendCreatesNoDatabase(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	dir := t.TempDir()
	content := "version: 1\nstorage:\n  database_path: ./data/abouei.db\nbackends:\n  - id: ok\n    url: " + backend.URL + "\n"
	path := filepath.Join(dir, "abouei.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	application := loadDurableApp(t, path)
	_, _, checkErr := application.Check(context.Background(), []string{"missing"})
	err := checkErr
	if err == nil || CategoryOf(err) != CategoryUsage {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "data", "abouei.db")); !os.IsNotExist(statErr) {
		t.Fatal("unknown backend must not create a database")
	}
}

func TestMonitorDeliversDownAndRecovery(t *testing.T) {
	fail := true
	var mu sync.Mutex
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		shouldFail := fail
		mu.Unlock()
		if shouldFail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	hook := &webhookCapture{status: http.StatusOK}
	webhook := httptest.NewServer(hook.handler(t))
	defer webhook.Close()

	content := `version: 1
storage:
  database_path: ./data/abouei.db
notifications:
  webhook:
    url: ` + webhook.URL + `/hook
    timeout: 2s
  retry:
    max_attempts: 3
    initial_backoff: 100ms
    max_backoff: 1s
backends:
  - id: flaky
    url: ` + backend.URL + `
    interval: 1s
    timeout: 900ms
    failure_threshold: 1
    recovery_threshold: 1
    notifications:
      enabled: true
`
	path := writeDurableConfig(t, content)
	application := loadDurableApp(t, path)

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan MonitorEvent, 32)
	done := make(chan error, 1)
	go func() {
		done <- application.Monitor(ctx, MonitorOptions{
			OnEvent: func(event MonitorEvent) { events <- event },
		})
	}()

	deadline := time.After(15 * time.Second)
	sawOpen := false
	for !sawOpen {
		select {
		case event := <-events:
			if event.IncidentStatus == "open" || event.IncidentStatus == "still_open" {
				sawOpen = true
			}
		case <-deadline:
			t.Fatal("monitor never reported an open incident")
		}
	}

	// Let the worker deliver Down, then recover the backend.
	deliverDeadline := time.After(15 * time.Second)
	for hook.count() == 0 {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deliverDeadline:
			t.Fatal("Down was never delivered")
		}
	}
	mu.Lock()
	fail = false
	mu.Unlock()

	recoverDeadline := time.After(20 * time.Second)
	for hook.count() < 2 {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-recoverDeadline:
			t.Fatalf("Recovery was never delivered (got %d)", hook.count())
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Monitor: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("monitor did not stop")
	}
	if !strings.Contains(hook.bodies()[0], `"incident.opened"`) ||
		!strings.Contains(hook.bodies()[1], `"incident.recovered"`) {
		t.Fatalf("payloads = %v", hook.bodies())
	}
}
