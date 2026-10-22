package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// failingBackendConfig builds a config with one failing backend and an
// enabled notification policy pointed at webhookURL.
func failingBackendConfig(backendURL, webhookURL string, failureThreshold int) string {
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
    failure_threshold: ` + itoa(failureThreshold) + `
    recovery_threshold: 1
    notifications:
      enabled: true
`
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

func TestCheckOpensIncidentOnThreshold(t *testing.T) {
	isolateConfigDiscovery(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	dir := t.TempDir()
	path := writeCLIConfig(t, dir, "abouei.yaml", failingBackendConfig(backend.URL, hook.URL+"/hook", 2))

	// First run: below threshold, no incident, exit 1 for the failed probe.
	code, stdout, _ := executeForTest(t, "check", "--config", path, "--output", "json")
	if code != ExitUnhealthy {
		t.Fatalf("first exit = %d, want 1", code)
	}
	var first struct {
		Status string `json:"status"`
		Data   struct {
			Summary struct {
				Unhealthy       int `json:"unhealthy"`
				IncidentsOpened int `json:"incidents_opened"`
			} `json:"summary"`
			Results []struct {
				IncidentStatus string `json:"incident_status"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &first); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if first.Data.Summary.IncidentsOpened != 0 || first.Data.Results[0].IncidentStatus != "none" {
		t.Fatalf("first run must not open: %+v", first.Data)
	}

	// Second run: threshold reached, incident opens, Down delivered.
	code, stdout, _ = executeForTest(t, "check", "--config", path, "--output", "json")
	if code != ExitUnhealthy {
		t.Fatalf("second exit = %d, want 1", code)
	}
	var second struct {
		Status string `json:"status"`
		Data   struct {
			Summary struct {
				IncidentsOpened        int `json:"incidents_opened"`
				NotificationsAttempted int `json:"notifications_attempted"`
				NotificationsFailed    int `json:"notifications_failed"`
			} `json:"summary"`
			Results []struct {
				IncidentStatus string `json:"incident_status"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &second); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if second.Data.Summary.IncidentsOpened != 1 ||
		second.Data.Summary.NotificationsAttempted != 1 ||
		second.Data.Summary.NotificationsFailed != 0 {
		t.Fatalf("second run summary = %+v", second.Data.Summary)
	}
	if second.Data.Results[0].IncidentStatus != "open" {
		t.Fatalf("results = %+v", second.Data.Results)
	}
}

func TestCheckNotificationFailureExitsFour(t *testing.T) {
	isolateConfigDiscovery(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer backend.Close()

	dir := t.TempDir()
	// Webhook refuses everything with a permanent failure.
	path := writeCLIConfig(t, dir, "abouei.yaml", failingBackendConfig(backend.URL, "http://127.0.0.1:1/hook", 1))

	code, stdout, stderr := executeForTest(t, "check", "--config", path, "--output", "json")
	if code != ExitNotificationFailure {
		t.Fatalf("exit = %d, want 4; stderr=%q", code, stderr)
	}
	var got struct {
		Status string `json:"status"`
		Data   struct {
			Summary struct {
				NotificationsAttempted int `json:"notifications_attempted"`
				NotificationsFailed    int `json:"notifications_failed"`
			} `json:"summary"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if got.Data.Summary.NotificationsFailed != 1 {
		t.Fatalf("summary = %+v", got.Data.Summary)
	}
	if count := strings.Count(stderr, "Error:"); count != 1 {
		t.Errorf("stderr = %q, want exactly one Error line", stderr)
	}
	if !strings.Contains(stderr, "remain queued") {
		t.Errorf("stderr = %q, want the queued-event hint", stderr)
	}
}

func TestCheckTextShowsIncidentStatus(t *testing.T) {
	isolateConfigDiscovery(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	dir := t.TempDir()
	path := writeCLIConfig(t, dir, "abouei.yaml", failingBackendConfig(backend.URL, hook.URL+"/hook", 1))

	code, stdout, _ := executeForTest(t, "check", "--config", path)
	if code != ExitUnhealthy {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stdout, "[incident open]") {
		t.Errorf("stdout = %q, want the incident marker", stdout)
	}
	if !strings.Contains(stdout, "1 incidents opened") {
		t.Errorf("stdout = %q, want incident counts", stdout)
	}
}

func TestCheckPayloadRedactsSecrets(t *testing.T) {
	isolateConfigDiscovery(t)
	const secret = "phase4-delivery-secret"
	var mu sync.Mutex
	var bodies []string
	var authHeaders []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		mu.Lock()
		bodies = append(bodies, string(body))
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer webhook.Close()

	t.Setenv("PHASE4_WEBHOOK_TOKEN", secret)
	document := `version: 1
storage:
  database_path: ./data/abouei.db
notifications:
  webhook:
    url: ` + webhook.URL + `/hook?token=${PHASE4_WEBHOOK_TOKEN}
    timeout: 2s
    headers:
      Authorization: Bearer ${PHASE4_WEBHOOK_TOKEN}
backends:
  - id: flaky
    url: ` + backend.URL + `
    interval: 60s
    timeout: 5s
    failure_threshold: 1
    recovery_threshold: 1
    headers:
      X-Api-Key: ${PHASE4_WEBHOOK_TOKEN}
    notifications:
      enabled: true
`
	dir := t.TempDir()
	path := writeCLIConfig(t, dir, "abouei.yaml", document)

	code, stdout, stderr := executeForTest(t, "check", "--config", path, "--output", "json")
	if code != ExitUnhealthy {
		t.Fatalf("exit = %d, want 1; stderr=%q", code, stderr)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("deliveries = %d", len(bodies))
	}
	if authHeaders[0] != "Bearer "+secret {
		t.Errorf("webhook did not receive the auth header")
	}
	for _, output := range append([]string{stdout, stderr}, bodies...) {
		if strings.Contains(output, secret) {
			t.Error("a CLI or webhook output leaked the resolved secret")
		}
	}
	if strings.Contains(bodies[0], "127.0.0.1") && strings.Contains(bodies[0], "/hook?token=") {
		t.Errorf("payload must not contain the webhook URL: %s", bodies[0])
	}
	// The persisted payload snapshot must be secret-free too. The path is
	// confined to this test's temporary directory.
	//
	//nolint:gosec // test-only read of a temp-dir database file.
	content, err := os.ReadFile(filepath.Join(dir, "data", "abouei.db"))
	if err != nil {
		t.Fatalf("read db: %v", err)
	}
	if strings.Contains(string(content), secret) {
		t.Error("the SQLite file contains the resolved secret")
	}
}

func TestMonitorProbeJSONCarriesIncidentStatus(t *testing.T) {
	isolateConfigDiscovery(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	dir := t.TempDir()
	path := writeCLIConfig(t, dir, "abouei.yaml", failingBackendConfig(backend.URL, hook.URL+"/hook", 1))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	type outcome struct {
		code   int
		stdout string
		stderr string
	}
	done := make(chan outcome, 1)
	go func() {
		var outBuf, errBuf strings.Builder
		code := Execute(ctx, []string{"monitor", "--config", path, "--output", "json"}, &outBuf, &errBuf)
		done <- outcome{code, outBuf.String(), errBuf.String()}
	}()
	time.Sleep(2 * time.Second)
	cancel()
	select {
	case got := <-done:
		if got.code != ExitSuccess {
			t.Fatalf("exit = %d, want 0; stderr=%q", got.code, got.stderr)
		}
		lines := strings.Split(strings.TrimSpace(got.stdout), "\n")
		sawOpen := false
		for _, line := range lines {
			var doc struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal([]byte(line), &doc); err != nil {
				t.Fatalf("NDJSON invalid: %v\n%s", err, line)
			}
			if doc.Data["kind"] == "probe" {
				if _, ok := doc.Data["incident_status"]; !ok {
					t.Errorf("probe event lacks incident_status: %s", line)
				}
				if doc.Data["incident_status"] == "open" || doc.Data["incident_status"] == "still_open" {
					sawOpen = true
				}
			}
		}
		if !sawOpen {
			t.Errorf("no probe reported an open incident:\n%s", got.stdout)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("monitor did not stop after cancellation")
	}
}
