package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// statusHistoryConfig builds a config with the given backend IDs sharing one
// failing or healthy target, a Down-capable notification policy, and a
// database under the config directory.
func statusHistoryConfig(backendURL, webhookURL string, ids ...string) string {
	var sb strings.Builder
	sb.WriteString("version: 1\nstorage:\n  database_path: ./data/abouei.db\n")
	sb.WriteString("notifications:\n  webhook:\n    url: " + webhookURL + "\n    timeout: 2s\n  retry:\n    max_attempts: 3\n    initial_backoff: 100ms\n    max_backoff: 1s\n")
	sb.WriteString("backends:\n")
	for _, id := range ids {
		sb.WriteString("  - id: " + id + "\n    url: " + backendURL + "\n    interval: 60s\n    timeout: 5s\n    failure_threshold: 1\n    recovery_threshold: 1\n    notifications:\n      enabled: true\n")
	}
	return sb.String()
}

func seedStatusHistoryDB(t *testing.T, dir, config string) string {
	t.Helper()
	return writeCLIConfig(t, dir, "abouei.yaml", config)
}

func TestStatusTextNoIncidentsExitZero(t *testing.T) {
	isolateConfigDiscovery(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	dir := t.TempDir()
	path := seedStatusHistoryDB(t, dir, statusHistoryConfig(backend.URL, hook.URL+"/hook", "api"))

	if code, _, stderr := executeForTest(t, "check", "--config", path); code != ExitSuccess {
		t.Fatalf("check exit = %d, want 0; stderr=%q", code, stderr)
	}
	code, stdout, stderr := executeForTest(t, "status", "--config", path)
	if code != ExitSuccess {
		t.Fatalf("status exit = %d, want 0; stderr=%q", code, stderr)
	}
	for _, want := range []string{"api: healthy", "0 open incidents", "Summary:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestStatusExitOneWithOpenIncident(t *testing.T) {
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
	path := seedStatusHistoryDB(t, dir, statusHistoryConfig(backend.URL, hook.URL+"/hook", "api"))

	if code, _, stderr := executeForTest(t, "check", "--config", path); code != ExitUnhealthy {
		t.Fatalf("check exit = %d, want 1; stderr=%q", code, stderr)
	}
	code, stdout, stderr := executeForTest(t, "status", "--config", path)
	if code != ExitUnhealthy {
		t.Fatalf("status exit = %d, want 1; stderr=%q", code, stderr)
	}
	for _, want := range []string{"api: down", "1 open incidents"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}
	if count := strings.Count(stderr, "Error:"); count != 1 {
		t.Errorf("stderr = %q, want exactly one Error line", stderr)
	}
}

func TestStatusJSONShape(t *testing.T) {
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
	path := seedStatusHistoryDB(t, dir, statusHistoryConfig(backend.URL, hook.URL+"/hook", "api"))

	if code, _, stderr := executeForTest(t, "check", "--config", path); code != ExitUnhealthy {
		t.Fatalf("check exit = %d, want 1; stderr=%q", code, stderr)
	}
	code, stdout, stderr := executeForTest(t, "status", "--config", path, "--output", "json")
	if code != ExitUnhealthy {
		t.Fatalf("status exit = %d, want 1; stderr=%q", code, stderr)
	}
	var got struct {
		SchemaVersion string `json:"schema_version"`
		Command       string `json:"command"`
		GeneratedAt   string `json:"generated_at"`
		Status        string `json:"status"`
		Data          struct {
			Backends []struct {
				BackendID            string  `json:"backend_id"`
				Name                 string  `json:"name"`
				Status               string  `json:"status"`
				LastObservedAt       *string `json:"last_observed_at"`
				LastStatusCode       *int    `json:"last_status_code"`
				ConsecutiveFailures  int     `json:"consecutive_failures"`
				ConsecutiveSuccesses int     `json:"consecutive_successes"`
				OpenIncidentID       *string `json:"open_incident_id"`
				OpenedAt             *string `json:"opened_at"`
			} `json:"backends"`
			OpenIncidents        int `json:"open_incidents"`
			PendingNotifications int `json:"pending_notifications"`
			FailedNotifications  int `json:"failed_notifications"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if got.SchemaVersion != "1" || got.Command != "status" || got.Status != "unhealthy" {
		t.Errorf("envelope = %+v", got)
	}
	if _, err := time.Parse(time.RFC3339, got.GeneratedAt); err != nil {
		t.Errorf("generated_at = %q is not RFC3339: %v", got.GeneratedAt, err)
	}
	if len(got.Data.Backends) != 1 || got.Data.OpenIncidents != 1 {
		t.Fatalf("data = %+v", got.Data)
	}
	entry := got.Data.Backends[0]
	if entry.BackendID != "api" || entry.Status != "down" {
		t.Errorf("entry = %+v", entry)
	}
	if entry.LastObservedAt == nil || entry.LastStatusCode == nil || *entry.LastStatusCode != http.StatusServiceUnavailable {
		t.Errorf("entry = %+v", entry)
	}
	if entry.OpenIncidentID == nil || entry.OpenedAt == nil {
		t.Errorf("entry = %+v, want the open incident link", entry)
	}
	if got.Error != nil {
		t.Errorf("error = %v, want null for a written payload", got.Error)
	}
	if strings.Contains(stdout, "127.0.0.1") && strings.Contains(stdout, "/hook") {
		t.Error("status JSON must not contain endpoint or webhook URLs")
	}
}

func TestStatusMissingDatabaseExitThree(t *testing.T) {
	isolateConfigDiscovery(t)
	dir := t.TempDir()
	path := writeCLIConfig(t, dir, "abouei.yaml", statusHistoryConfig("http://127.0.0.1:9/health", "http://127.0.0.1:9/hook", "api"))

	code, stdout, stderr := executeForTest(t, "status", "--config", path)
	if code != ExitInternal {
		t.Fatalf("status exit = %d, want 3; stderr=%q", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty in text mode", stdout)
	}
	if !strings.Contains(stderr, "run check or monitor first") {
		t.Errorf("stderr = %q, want the first-run hint", stderr)
	}

	code, stdout, stderr = executeForTest(t, "history", "--config", path)
	if code != ExitInternal {
		t.Fatalf("history exit = %d, want 3; stderr=%q", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty in text mode", stdout)
	}

	code, stdout, stderr = executeForTest(t, "status", "--config", path, "--output", "json")
	if code != ExitInternal {
		t.Fatalf("JSON status exit = %d, want 3; stderr=%q", code, stderr)
	}
	var envelope struct {
		Command string `json:"command"`
		Status  string `json:"status"`
		Data    any    `json:"data"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if envelope.Command != "status" || envelope.Status != "error" || envelope.Data != nil {
		t.Errorf("envelope = %+v", envelope)
	}
	if envelope.Error.Code != "database_unavailable" {
		t.Errorf("code = %q, want database_unavailable", envelope.Error.Code)
	}
}

func TestStatusUnknownBackendExitTwo(t *testing.T) {
	isolateConfigDiscovery(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	dir := t.TempDir()
	path := seedStatusHistoryDB(t, dir, statusHistoryConfig(backend.URL, hook.URL+"/hook", "api"))
	if code, _, stderr := executeForTest(t, "check", "--config", path); code != ExitSuccess {
		t.Fatalf("check exit = %d, want 0; stderr=%q", code, stderr)
	}
	code, stdout, stderr := executeForTest(t, "status", "--config", path, "--backend", "missing")
	if code != ExitUsage {
		t.Fatalf("status exit = %d, want 2; stderr=%q", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on usage error", stdout)
	}
	if !strings.Contains(stderr, `unknown backend "missing"`) {
		t.Errorf("stderr = %q, want the backend name", stderr)
	}
}

func TestStatusNeverProbes(t *testing.T) {
	isolateConfigDiscovery(t)
	var probes atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	dir := t.TempDir()
	path := seedStatusHistoryDB(t, dir, statusHistoryConfig(backend.URL, hook.URL+"/hook", "api"))
	if code, _, stderr := executeForTest(t, "check", "--config", path); code != ExitSuccess {
		t.Fatalf("check exit = %d, want 0; stderr=%q", code, stderr)
	}
	before := probes.Load()
	if code, _, stderr := executeForTest(t, "status", "--config", path); code != ExitSuccess {
		t.Fatalf("status exit = %d, want 0; stderr=%q", code, stderr)
	}
	if code, _, stderr := executeForTest(t, "history", "--config", path); code != ExitSuccess {
		t.Fatalf("history exit = %d, want 0; stderr=%q", code, stderr)
	}
	if got := probes.Load(); got != before {
		t.Fatalf("status/history probed the backend %d extra times", got-before)
	}
}

func TestStatusHistoryReadOnlyStable(t *testing.T) {
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
	path := seedStatusHistoryDB(t, dir, statusHistoryConfig(backend.URL, hook.URL+"/hook", "api"))
	if code, _, stderr := executeForTest(t, "check", "--config", path); code != ExitUnhealthy {
		t.Fatalf("check exit = %d, want 1; stderr=%q", code, stderr)
	}
	_, firstStatus, _ := executeForTest(t, "status", "--config", path)
	_, secondStatus, _ := executeForTest(t, "status", "--config", path)
	if firstStatus != secondStatus {
		t.Fatalf("status is not stable:\n%s\n---\n%s", firstStatus, secondStatus)
	}
	_, firstHistory, _ := executeForTest(t, "history", "--config", path)
	_, secondHistory, _ := executeForTest(t, "history", "--config", path)
	if firstHistory != secondHistory {
		t.Fatalf("history is not stable:\n%s\n---\n%s", firstHistory, secondHistory)
	}
}

func TestHistoryTextAndFilters(t *testing.T) {
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
	path := seedStatusHistoryDB(t, dir, statusHistoryConfig(backend.URL, hook.URL+"/hook", "api"))
	for i := 0; i < 2; i++ {
		if code, _, stderr := executeForTest(t, "check", "--config", path); code != ExitUnhealthy {
			t.Fatalf("check %d exit = %d, want 1; stderr=%q", i, code, stderr)
		}
	}

	code, stdout, _ := executeForTest(t, "history", "--config", path)
	if code != ExitSuccess {
		t.Fatalf("history exit = %d, want 0", code)
	}
	for _, want := range []string{"Observations:", "Incidents:", "api", "incident.opened", "has_more: false"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}

	code, stdout, _ = executeForTest(t, "history", "--config", path, "--limit", "1")
	if code != ExitSuccess {
		t.Fatalf("limited history exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "has_more: true") {
		t.Errorf("stdout = %q, want truncation marker", stdout)
	}

	code, stdout, _ = executeForTest(t, "history", "--config", path, "--backend", "api", "--limit", "50")
	if code != ExitSuccess || !strings.Contains(stdout, "api") {
		t.Errorf("backend filter exit = %d, stdout = %q", code, stdout)
	}

	code, stdout, _ = executeForTest(t, "history", "--config", path, "--since", "2099-01-01T00:00:00Z")
	if code != ExitSuccess {
		t.Fatalf("future since exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "(none)") {
		t.Errorf("stdout = %q, want the empty marker", stdout)
	}

	code, _, _ = executeForTest(t, "history", "--config", path, "--since", "1h")
	if code != ExitSuccess {
		t.Fatalf("duration since exit = %d, want 0", code)
	}

	for _, args := range [][]string{
		{"history", "--config", path, "--since", "not-a-time"},
		{"history", "--config", path, "--since", "-5m"},
		{"history", "--config", path, "--limit", "0"},
		{"history", "--config", path, "--limit", "1001"},
		{"history", "--config", path, "--limit", "nope"},
	} {
		code, stdout, stderr := executeForTest(t, args...)
		if code != ExitUsage {
			t.Errorf("args %v exit = %d, want 2; stderr=%q", args, code, stderr)
		}
		if stdout != "" {
			t.Errorf("args %v stdout = %q, want empty", args, stdout)
		}
	}
}

func TestHistoryJSONShape(t *testing.T) {
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
	path := seedStatusHistoryDB(t, dir, statusHistoryConfig(backend.URL, hook.URL+"/hook", "api"))
	if code, _, stderr := executeForTest(t, "check", "--config", path); code != ExitUnhealthy {
		t.Fatalf("check exit = %d, want 1; stderr=%q", code, stderr)
	}

	code, stdout, stderr := executeForTest(t, "history", "--config", path, "--output", "json")
	if code != ExitSuccess {
		t.Fatalf("history exit = %d, want 0; stderr=%q", code, stderr)
	}
	var got struct {
		SchemaVersion string `json:"schema_version"`
		Command       string `json:"command"`
		GeneratedAt   string `json:"generated_at"`
		Status        string `json:"status"`
		Data          struct {
			Observations []struct {
				BackendID     string  `json:"backend_id"`
				ObservedAt    string  `json:"observed_at"`
				Outcome       string  `json:"outcome"`
				StatusCode    *int    `json:"status_code"`
				DurationMs    int64   `json:"duration_ms"`
				FailureReason *string `json:"failure_reason"`
				Error         *string `json:"error"`
			} `json:"observations"`
			Incidents []struct {
				IncidentID string `json:"incident_id"`
				BackendID  string `json:"backend_id"`
				EventType  string `json:"event_type"`
				OccurredAt string `json:"occurred_at"`
			} `json:"incidents"`
			HasMore bool `json:"has_more"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if got.SchemaVersion != "1" || got.Command != "history" || got.Status != "success" {
		t.Errorf("envelope = %+v", got)
	}
	if _, err := time.Parse(time.RFC3339, got.GeneratedAt); err != nil {
		t.Errorf("generated_at = %q: %v", got.GeneratedAt, err)
	}
	if len(got.Data.Observations) != 1 || got.Data.HasMore {
		t.Errorf("data = %+v", got.Data)
	}
	observation := got.Data.Observations[0]
	if observation.BackendID != "api" || observation.Outcome != "failed" || observation.StatusCode == nil {
		t.Errorf("observation = %+v", observation)
	}
	if _, err := time.Parse(time.RFC3339, observation.ObservedAt); err != nil {
		t.Errorf("observed_at = %q: %v", observation.ObservedAt, err)
	}
	if len(got.Data.Incidents) != 1 || got.Data.Incidents[0].EventType != "incident.opened" {
		t.Errorf("incidents = %+v", got.Data.Incidents)
	}
	if _, err := time.Parse(time.RFC3339, got.Data.Incidents[0].OccurredAt); err != nil {
		t.Errorf("occurred_at = %q: %v", got.Data.Incidents[0].OccurredAt, err)
	}
	if got.Error != nil {
		t.Errorf("error = %v, want null", got.Error)
	}
}

func TestStatusHistoryRedactsSecrets(t *testing.T) {
	isolateConfigDiscovery(t)
	const secret = "phase5-operational-secret"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	t.Setenv("PHASE5_WEBHOOK_TOKEN", secret)
	document := `version: 1
storage:
  database_path: ./data/abouei.db
notifications:
  webhook:
    url: ` + hook.URL + `/hook?token=${PHASE5_WEBHOOK_TOKEN}
    timeout: 2s
backends:
  - id: api
    url: ` + backend.URL + `
    interval: 60s
    timeout: 5s
    failure_threshold: 1
    recovery_threshold: 1
    headers:
      X-Api-Key: ${PHASE5_WEBHOOK_TOKEN}
    notifications:
      enabled: true
`
	dir := t.TempDir()
	path := writeCLIConfig(t, dir, "abouei.yaml", document)
	if code, _, stderr := executeForTest(t, "check", "--config", path); code != ExitUnhealthy {
		t.Fatalf("check exit = %d, want 1; stderr=%q", code, stderr)
	}
	_, statusOut, statusErr := executeForTest(t, "status", "--config", path, "--output", "json")
	_, historyOut, historyErr := executeForTest(t, "history", "--config", path, "--output", "json")
	for _, output := range []string{statusOut, statusErr, historyOut, historyErr} {
		if strings.Contains(output, secret) {
			t.Error("operational output leaked the resolved secret")
		}
	}
	//nolint:gosec // test-only read of a temp-dir database file.
	content, err := os.ReadFile(filepath.Join(dir, "data", "abouei.db"))
	if err != nil {
		t.Fatalf("read db: %v", err)
	}
	if strings.Contains(string(content), secret) {
		t.Error("the SQLite file contains the resolved secret")
	}
}

func TestStatusHistoryJSONKeepsStdoutClean(t *testing.T) {
	isolateConfigDiscovery(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	dir := t.TempDir()
	path := seedStatusHistoryDB(t, dir, statusHistoryConfig(backend.URL, hook.URL+"/hook", "api"))
	if code, _, stderr := executeForTest(t, "check", "--config", path); code != ExitSuccess {
		t.Fatalf("check exit = %d, want 0; stderr=%q", code, stderr)
	}
	for _, args := range [][]string{
		{"status", "--config", path, "--output", "json", "--log-level", "debug"},
		{"history", "--config", path, "--output", "json", "--log-level", "debug"},
	} {
		code, stdout, stderr := executeForTest(t, args...)
		if code != ExitSuccess {
			t.Fatalf("args %v exit = %d, want 0; stderr=%q", args, code, stderr)
		}
		if !json.Valid([]byte(stdout)) {
			t.Errorf("args %v stdout is not valid JSON:\n%s", args, stdout)
		}
		if strings.Contains(stdout, "level=DEBUG") {
			t.Errorf("args %v stdout was contaminated by logs", args)
		}
		if !strings.Contains(stderr, "level=DEBUG") {
			t.Errorf("args %v stderr = %q, want debug logs on stderr", args, stderr)
		}
	}
}

func TestStatusShowsOrphanedEndToEnd(t *testing.T) {
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
	full := writeCLIConfig(t, dir, "full.yaml", statusHistoryConfig(backend.URL, hook.URL+"/hook", "api", "temp"))
	if code, _, stderr := executeForTest(t, "check", "--config", full); code != ExitUnhealthy {
		t.Fatalf("check exit = %d, want 1; stderr=%q", code, stderr)
	}
	reduced := writeCLIConfig(t, dir, "reduced.yaml", statusHistoryConfig(backend.URL, hook.URL+"/hook", "api"))
	code, stdout, stderr := executeForTest(t, "status", "--config", reduced)
	if code != ExitUnhealthy {
		t.Fatalf("status exit = %d, want 1; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "orphaned") || !strings.Contains(stdout, "temp") {
		t.Errorf("stdout = %q, want the orphaned incident", stdout)
	}
	if !strings.Contains(stdout, "2 open incidents") {
		t.Errorf("stdout = %q, want both open incidents", stdout)
	}
}
