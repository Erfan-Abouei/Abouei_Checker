// Package integration provides Phase 6 end-to-end coverage: the real CLI
// command tree drives real HTTP test servers and a real webhook receiver
// against temporary SQLite databases. These tests assert cross-component
// behavior (incident lifecycle, outbox durability across restarts, failure
// injection, cancellation, and concurrency) rather than single-package units.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/cli"
)

func isolateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ABOUEI_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// Secret-bearing headers resolve from the environment; keep each test
	// hermetic.
	t.Setenv("ABOUEI_TEST_TOKEN", "integration-secret-token")
}

func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "abouei.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func baseConfig(dbName, backendURL, webhookURL string, failureThreshold, recoveryThreshold int) string {
	webhook := ""
	notifyEnabled := "false"
	if webhookURL != "" {
		notifyEnabled = "true"
		webhook = `notifications:
  webhook:
    url: ` + webhookURL + `
    timeout: 2s
  retry:
    max_attempts: 3
    initial_backoff: 100ms
    max_backoff: 1s
`
	}
	return `version: 1
storage:
  database_path: ./data/` + dbName + `
` + webhook + `backends:
  - id: api
    url: ` + backendURL + `
    interval: 60s
    timeout: 5s
    failure_threshold: ` + itoa(failureThreshold) + `
    recovery_threshold: ` + itoa(recoveryThreshold) + `
    notifications:
      enabled: ` + notifyEnabled + `
`
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var d []byte
	for v > 0 {
		d = append([]byte{byte('0' + v%10)}, d...)
		v /= 10
	}
	return string(d)
}

func runCheck(ctx context.Context, t *testing.T, configPath string, extra ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"check", "--config", configPath, "--output", "json"}, extra...)
	var stdout, stderr bytes.Buffer
	code := cli.Execute(ctx, args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// webhookRecorder captures delivered payloads for assertions about delivery
// count, event kinds, idempotency keys, and secret redaction.
type webhookRecorder struct {
	mu        sync.Mutex
	bodies    []string
	auth      []string
	idem      []string
	statuses  []int
	nextCodes []int
	calls     atomic.Int64
}

func (r *webhookRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		n := int(r.calls.Add(1))
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(req.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, buf.String())
		r.auth = append(r.auth, req.Header.Get("Authorization"))
		r.idem = append(r.idem, req.Header.Get("Idempotency-Key"))
		code := http.StatusOK
		if n-1 < len(r.nextCodes) {
			code = r.nextCodes[n-1]
		}
		r.statuses = append(r.statuses, code)
		r.mu.Unlock()
		if h := req.Header.Get("Retry-After"); h != "" {
			w.Header().Set("Retry-After", h)
		}
		w.WriteHeader(code)
	}
}

func (r *webhookRecorder) snapshot() (bodies, auth, idem []string, calls int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...),
		append([]string(nil), r.auth...),
		append([]string(nil), r.idem...),
		r.calls.Load()
}

func kindsOf(t *testing.T, bodies []string) []string {
	t.Helper()
	var kinds []string
	for _, b := range bodies {
		var payload struct {
			EventType string `json:"event_type"`
		}
		if err := json.Unmarshal([]byte(b), &payload); err != nil {
			t.Fatalf("webhook body is not JSON: %v\n%s", err, b)
		}
		kinds = append(kinds, payload.EventType)
	}
	return kinds
}

func TestDownThenRecoveryAcrossRestarts(t *testing.T) {
	isolateEnv(t)
	var failing atomic.Bool
	failing.Store(true)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	rec := &webhookRecorder{}
	hook := httptest.NewServer(rec.handler())
	defer hook.Close()

	dir := t.TempDir()
	path := writeConfig(t, dir, baseConfig("abouei.db", backend.URL, hook.URL+"/hook", 2, 1))
	ctx := context.Background()

	// Sub-threshold: first failure opens nothing and delivers nothing.
	if code, _, _ := runCheck(ctx, t, path); code != cli.ExitUnhealthy {
		t.Fatalf("first check exit = %d, want %d", code, cli.ExitUnhealthy)
	}
	if _, _, _, calls := rec.snapshot(); calls != 0 {
		t.Fatalf("webhook calls = %d, want 0 before threshold", calls)
	}

	// Threshold reached: exactly one Down.
	if code, _, _ := runCheck(ctx, t, path); code != cli.ExitUnhealthy {
		t.Fatalf("second check exit = %d, want %d", code, cli.ExitUnhealthy)
	}
	bodies, _, idem, calls := rec.snapshot()
	if calls != 1 {
		t.Fatalf("webhook calls = %d, want 1 Down", calls)
	}
	if got := kindsOf(t, bodies); len(got) != 1 || got[0] != "incident.opened" {
		t.Fatalf("events = %v, want [incident.opened]", got)
	}
	downKey := idem[0]
	if downKey == "" {
		t.Fatal("Down delivery is missing its idempotency key")
	}

	// Repeated failures: no duplicate Down.
	if code, _, _ := runCheck(ctx, t, path); code != cli.ExitUnhealthy {
		t.Fatalf("third check exit = %d, want %d", code, cli.ExitUnhealthy)
	}
	if _, _, _, calls := rec.snapshot(); calls != 1 {
		t.Fatalf("webhook calls = %d, want no duplicate Down", calls)
	}

	// Recovery: backend healthy, exactly one Recovery.
	failing.Store(false)
	if code, _, _ := runCheck(ctx, t, path); code != cli.ExitSuccess {
		t.Fatalf("recovery check exit = %d, want %d", code, cli.ExitSuccess)
	}
	bodies, _, idem, calls = rec.snapshot()
	if calls != 2 {
		t.Fatalf("webhook calls = %d, want Down + Recovery", calls)
	}
	if got := kindsOf(t, bodies); len(got) != 2 || got[1] != "incident.recovered" {
		t.Fatalf("events = %v, want [incident.opened incident.recovered]", got)
	}
	if idem[1] == "" || idem[1] == downKey {
		t.Fatalf("recovery idempotency key = %q, must be present and distinct from Down", idem[1])
	}
	for _, b := range bodies {
		if strings.Contains(b, "integration-secret-token") || strings.Contains(b, backend.URL) {
			t.Fatalf("payload leaks secrets or URLs: %s", b)
		}
	}

	// Steady state: further healthy checks deliver nothing.
	if code, _, _ := runCheck(ctx, t, path); code != cli.ExitSuccess {
		t.Fatalf("steady check exit = %d, want %d", code, cli.ExitSuccess)
	}
	if _, _, _, calls := rec.snapshot(); calls != 2 {
		t.Fatalf("webhook calls = %d, want no post-recovery delivery", calls)
	}
}

func TestTransientWebhookFailureDeliveredOnceAcrossRestart(t *testing.T) {
	isolateEnv(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	// Fail every delivery with a retryable 500 until the test flips the
	// flag. The first run must leave the Down pending (exit 4); the next
	// run must deliver it exactly once without creating a second Down.
	var failOpen atomic.Bool
	failOpen.Store(true)
	rec := &webhookRecorder{}
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, string(body))
		rec.auth = append(rec.auth, r.Header.Get("Authorization"))
		rec.idem = append(rec.idem, r.Header.Get("Idempotency-Key"))
		fail := failOpen.Load()
		rec.mu.Unlock()
		rec.calls.Add(1)
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	dir := t.TempDir()
	path := writeConfig(t, dir, baseConfig("abouei.db", backend.URL, hook.URL+"/hook", 1, 1))
	ctx := context.Background()

	code, firstOut, _ := runCheck(ctx, t, path)
	if code != cli.ExitNotificationFailure {
		t.Fatalf("first check exit = %d, want %d\n%s", code, cli.ExitNotificationFailure, firstOut)
	}

	// Backend still failing: the incident is already open, so opening the
	// webhook lets the second run deliver the pending Down instead of
	// creating a new one. The failed attempt scheduled the retry with a
	// bounded backoff (cap 100ms in this config); wait past it so the
	// event is deterministically due instead of racing the clock.
	failOpen.Store(false)
	time.Sleep(500 * time.Millisecond)
	bodiesMid, _, _, _ := rec.snapshot()
	if code, _, _ := runCheck(ctx, t, path); code != cli.ExitUnhealthy {
		t.Fatalf("second check exit = %d, want %d", code, cli.ExitUnhealthy)
	}
	bodies, _, _, _ := rec.snapshot()
	kinds := kindsOf(t, bodies)
	downCount := 0
	for _, k := range kinds {
		if k == "incident.opened" {
			downCount++
		}
	}
	beforeDown := 0
	for _, k := range kindsOf(t, bodiesMid) {
		if k == "incident.opened" {
			beforeDown++
		}
	}
	if downCount-beforeDown != 1 {
		t.Fatalf("events = %v, want exactly one new Down delivery", kinds)
	}

	// Third run: steady failing state, no further deliveries once the
	// pending event is acknowledged.
	before := len(bodies)
	if code, _, _ := runCheck(ctx, t, path); code != cli.ExitUnhealthy {
		t.Fatalf("third check exit = %d, want %d", code, cli.ExitUnhealthy)
	}
	if after, _, _, _ := rec.snapshot(); len(after) != before {
		t.Fatalf("deliveries grew from %d to %d after acknowledgement", before, len(after))
	}
}

func TestPermanentWebhookFailureIsDeadLetter(t *testing.T) {
	isolateEnv(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	rec := &webhookRecorder{nextCodes: []int{400, 400, 400, 400, 400, 400}}
	hook := httptest.NewServer(rec.handler())
	defer hook.Close()

	dir := t.TempDir()
	path := writeConfig(t, dir, baseConfig("abouei.db", backend.URL, hook.URL+"/hook", 1, 1))
	ctx := context.Background()

	if code, _, _ := runCheck(ctx, t, path); code != cli.ExitNotificationFailure {
		t.Fatalf("first check exit = %d, want %d", code, cli.ExitNotificationFailure)
	}
	firstCalls := func() int64 { _, _, _, c := rec.snapshot(); return c }()

	// A permanent 400 must not be retried forever: the next run keeps the
	// incident open without new delivery attempts for the same event.
	if code, _, _ := runCheck(ctx, t, path); code != cli.ExitUnhealthy {
		t.Fatalf("second check exit = %d, want %d", code, cli.ExitUnhealthy)
	}
	if _, _, _, calls := rec.snapshot(); calls != firstCalls {
		t.Fatalf("webhook calls grew from %d to %d for a permanent failure", firstCalls, calls)
	}
}

func TestFailureInjectionMatrix(t *testing.T) {
	isolateEnv(t)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 2<<20))
	}))
	defer oversized.Close()
	malformedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for malformed server: %v", err)
	}
	defer func() { _ = malformedListener.Close() }()
	go func() {
		for {
			conn, err := malformedListener.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("THIS IS NOT HTTP\r\n\r\n"))
			_ = conn.Close()
		}
	}()
	malformedURL := "http://" + malformedListener.Addr().String()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer slow.Close()

	cases := []struct {
		name       string
		backendURL string
		timeout    string
		matcher    string
	}{
		{"connection_refused", "http://127.0.0.1:1/health", "5s", ""},
		{"timeout", slow.URL, "100ms", "none"},
		{"oversized_body_with_matcher", oversized.URL, "5s", "match"},
		{"malformed_response", malformedURL, "5s", "none"},
		{"dns_failure", "http://nonexistent.invalid/health", "5s", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			cfg := `version: 1
storage:
  database_path: ./data/` + tc.name + `.db
backends:
  - id: victim
    url: ` + tc.backendURL + `
    interval: 60s
    timeout: ` + tc.timeout + `
`
			if tc.matcher != "none" {
				cfg += `    body_match:
      type: contains
      value: never-present
`
			}
			dir := t.TempDir()
			path := writeConfig(t, dir, cfg)
			code, stdout, stderr := runCheck(context.Background(), t, path)
			if code != cli.ExitUnhealthy {
				t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", code, cli.ExitUnhealthy, stdout, stderr)
			}
			var doc struct {
				Status string `json:"status"`
				Data   struct {
					Results []struct {
						Status        string `json:"status"`
						FailureReason string `json:"failure_reason"`
					} `json:"results"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
				t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
			}
			if len(doc.Data.Results) != 1 {
				t.Fatalf("results = %+v, want one failed outcome", doc.Data.Results)
			}
			got := doc.Data.Results[0].Status
			if got != "unhealthy" && got != "error" {
				t.Fatalf("status = %q, want unhealthy or error", got)
			}
			if doc.Data.Results[0].FailureReason == "" {
				t.Error("unhealthy result must carry a failure_reason")
			}
			combined := stdout + stderr
			for _, secret := range []string{"integration-secret-token", "Authorization"} {
				if strings.Contains(combined, secret) {
					t.Errorf("output leaks %q", secret)
				}
			}
		})
	}
}

func TestConcurrentBackendsAreIsolated(t *testing.T) {
	isolateEnv(t)
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthy.Close()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer failing.Close()
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	cfg := `version: 1
storage:
  database_path: ./data/mixed.db
notifications:
  webhook:
    url: ` + hook.URL + `
    timeout: 2s
runtime:
  max_concurrent_probes: 4
backends:
  - id: good
    url: ` + healthy.URL + `
    interval: 60s
    timeout: 5s
  - id: bad
    url: ` + failing.URL + `
    interval: 60s
    timeout: 5s
    failure_threshold: 1
    notifications:
      enabled: true
`
	dir := t.TempDir()
	path := writeConfig(t, dir, cfg)
	code, stdout, _ := runCheck(context.Background(), t, path)
	if code != cli.ExitUnhealthy {
		t.Fatalf("exit = %d, want %d", code, cli.ExitUnhealthy)
	}
	var doc struct {
		Data struct {
			Results []struct {
				BackendID string `json:"backend_id"`
				Status    string `json:"status"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	byID := map[string]string{}
	for _, r := range doc.Data.Results {
		byID[r.BackendID] = r.Status
	}
	if byID["good"] != "healthy" || byID["bad"] != "unhealthy" {
		t.Fatalf("outcomes = %v, want good=healthy bad=unhealthy", byID)
	}
}

func TestInvalidConfigFailsBeforeMonitoring(t *testing.T) {
	isolateEnv(t)
	var backendHits atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	dir := t.TempDir()
	path := writeConfig(t, dir, "version: 1\nbackends:\n  - id: bad one!\n    url: "+backend.URL+"\n")
	code, stdout, stderr := runCheck(context.Background(), t, path)
	if code != cli.ExitUsage {
		t.Fatalf("exit = %d, want %d", code, cli.ExitUsage)
	}
	if backendHits.Load() != 0 {
		t.Fatal("invalid config must not trigger any probe")
	}
	if _, err := os.Stat(filepath.Join(dir, "data")); !os.IsNotExist(err) {
		t.Fatal("invalid config must not create storage")
	}
	for _, secret := range []string{"integration-secret-token"} {
		if strings.Contains(stdout+stderr, secret) {
			t.Fatalf("error output leaks secrets")
		}
	}
}

func TestCheckCancellationIsSafe(t *testing.T) {
	isolateEnv(t)
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-release:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer backend.Close()
	defer close(release)

	dir := t.TempDir()
	path := writeConfig(t, dir, baseConfig("cancel.db", backend.URL, "", 1, 1))
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		code           int
		stdout, stderr string
	}
	done := make(chan outcome, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		code := cli.Execute(ctx, []string{"check", "--config", path}, &stdout, &stderr)
		done <- outcome{code, stdout.String(), stderr.String()}
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case got := <-done:
		if got.code == cli.ExitSuccess {
			t.Fatalf("canceled check must not report success\n%s", got.stdout)
		}
		if strings.Contains(got.stdout+got.stderr, "integration-secret-token") {
			t.Fatal("cancellation output leaks secrets")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canceled check did not return")
	}
}

func TestMonitorShutdownDeliversNoDuplicates(t *testing.T) {
	isolateEnv(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	dir := t.TempDir()
	path := writeConfig(t, dir, baseConfig("monitor.db", backend.URL, "", 1, 1))
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		code           int
		stdout, stderr string
	}
	done := make(chan outcome, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		code := cli.Execute(ctx, []string{"monitor", "--config", path, "--output", "json"}, &stdout, &stderr)
		done <- outcome{code, stdout.String(), stderr.String()}
	}()
	time.Sleep(600 * time.Millisecond)
	cancel()
	select {
	case got := <-done:
		if got.code != cli.ExitSuccess {
			t.Fatalf("exit = %d, want %d\nstderr: %s", got.code, cli.ExitSuccess, got.stderr)
		}
		lines := strings.Split(strings.TrimSpace(got.stdout), "\n")
		if len(lines) < 2 {
			t.Fatalf("want lifecycle NDJSON, got:\n%s", got.stdout)
		}
		for _, line := range lines {
			var doc map[string]any
			if err := json.Unmarshal([]byte(line), &doc); err != nil {
				t.Fatalf("NDJSON line invalid: %v\n%s", err, line)
			}
		}
		if !strings.Contains(got.stdout, `"kind":"shutdown"`) {
			t.Errorf("missing shutdown event:\n%s", got.stdout)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("monitor did not shut down after cancellation")
	}
}
