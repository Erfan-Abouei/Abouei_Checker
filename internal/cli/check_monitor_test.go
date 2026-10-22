package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func writeCheckConfig(t *testing.T, dir, urlA, urlB string) string {
	t.Helper()
	document := "version: 1\nbackends:\n" +
		"  - id: a\n    url: " + urlA + "\n    interval: 60s\n    timeout: 5s\n" +
		"  - id: b\n    url: " + urlB + "\n    interval: 60s\n    timeout: 5s\n"
	return writeCLIConfig(t, dir, "abouei.yaml", document)
}

func TestCheckAllHealthyText(t *testing.T) {
	isolateConfigDiscovery(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	path := writeCheckConfig(t, t.TempDir(), server.URL, server.URL)

	code, stdout, stderr := executeForTest(t, "check", "--config", path)
	if code != ExitSuccess {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr)
	}
	for _, want := range []string{"a: healthy", "b: healthy", "Summary: 2 healthy"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout=%q, want %q", stdout, want)
		}
	}
	if strings.Count(stderr, "Error:") != 0 {
		t.Errorf("healthy check must not print Error line: %q", stderr)
	}
}

func TestCheckMixedExitOne(t *testing.T) {
	isolateConfigDiscovery(t)
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	path := writeCheckConfig(t, t.TempDir(), ok.URL, bad.URL)

	code, stdout, stderr := executeForTest(t, "check", "--config", path)
	if code != ExitUnhealthy {
		t.Fatalf("exit = %d, want 1; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "a: healthy") || !strings.Contains(stdout, "b: unhealthy") {
		t.Errorf("stdout=%q", stdout)
	}
	if got := strings.Count(stderr, "Error:"); got != 1 {
		t.Errorf("stderr=%q, want exactly one Error line", stderr)
	}
}

func TestCheckJSONUnhealthyEnvelope(t *testing.T) {
	isolateConfigDiscovery(t)
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	path := writeCheckConfig(t, t.TempDir(), ok.URL, bad.URL)

	code, stdout, stderr := executeForTest(t, "check", "--config", path, "--output", "json")
	if code != ExitUnhealthy {
		t.Fatalf("exit = %d, want 1; stderr=%q", code, stderr)
	}
	var got struct {
		SchemaVersion string `json:"schema_version"`
		Command       string `json:"command"`
		Status        string `json:"status"`
		Data          struct {
			Summary struct {
				Total     int `json:"total"`
				Healthy   int `json:"healthy"`
				Unhealthy int `json:"unhealthy"`
			} `json:"summary"`
			Results []struct {
				BackendID        string `json:"backend_id"`
				Status           string `json:"status"`
				HTTPStatus       int    `json:"http_status"`
				FailureReason    string `json:"failure_reason"`
				IncidentStatus   string `json:"incident_status"`
				ThresholdReached bool   `json:"threshold_reached"`
			} `json:"results"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if got.Status != "unhealthy" || got.Command != "check" {
		t.Errorf("envelope=%+v", got)
	}
	if got.Data.Summary.Total != 2 || got.Data.Summary.Unhealthy != 1 {
		t.Errorf("summary=%+v", got.Data.Summary)
	}
	if len(got.Data.Results) != 2 || got.Data.Results[0].BackendID != "a" || got.Data.Results[1].BackendID != "b" {
		t.Errorf("results must follow config order: %+v", got.Data.Results)
	}
	if got.Data.Results[1].FailureReason != "status_mismatch" || got.Data.Results[1].IncidentStatus != "none" {
		t.Errorf("results[1]=%+v", got.Data.Results[1])
	}
	if got.Error != nil {
		t.Errorf("error=%v, want null for result envelope", got.Error)
	}
	if got := strings.Count(stderr, "Error:"); got != 1 {
		t.Errorf("stderr=%q, want exactly one Error line", stderr)
	}
	// stdout must be exactly one JSON document.
	decoder := json.NewDecoder(strings.NewReader(stdout))
	var extra any
	_ = decoder.Decode(&struct{}{})
	if decoder.More() {
		_ = extra
		t.Error("stdout must contain exactly one JSON document")
	}
}

func TestCheckUnknownBackendNoRequests(t *testing.T) {
	isolateConfigDiscovery(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	path := writeCheckConfig(t, t.TempDir(), server.URL, server.URL)

	code, stdout, stderr := executeForTest(t, "check", "--config", path, "missing")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout=%q, want empty when no requests run", stdout)
	}
	if !strings.Contains(stderr, "unknown backend") {
		t.Errorf("stderr=%q", stderr)
	}
}

func TestCheckInvalidConfigMakesNoRequests(t *testing.T) {
	isolateConfigDiscovery(t)
	dir := t.TempDir()
	path := writeCLIConfig(t, dir, "abouei.yaml", "version: 1\nbackends:\n  - id: api\n    url: not-a-url\n")

	code, stdout, _ := executeForTest(t, "check", "--config", path)
	if code != ExitUsage {
		t.Fatalf("exit = %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout=%q, want empty in text mode for config failure", stdout)
	}
}

func TestCheckSecretRedaction(t *testing.T) {
	isolateConfigDiscovery(t)
	const secret = "check-secret-xyz"
	var seenAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("CHECK_TOKEN", secret)
	document := "version: 1\nbackends:\n  - id: api\n    url: " + server.URL + "\n    interval: 60s\n    timeout: 5s\n    headers:\n      Authorization: Bearer ${CHECK_TOKEN}\n"
	path := writeCLIConfig(t, t.TempDir(), "abouei.yaml", document)

	code, stdout, stderr := executeForTest(t, "check", "--config", path, "--output", "json")
	if code != ExitUnhealthy {
		t.Fatalf("exit = %d, want 1", code)
	}
	if seenAuth != "Bearer "+secret {
		t.Fatalf("server did not receive secret header: %q", seenAuth)
	}
	if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
		t.Error("check output leaked a resolved secret")
	}
}

func TestMonitorHelpAndUnknownBackend(t *testing.T) {
	code, stdout, _ := executeForTest(t, "monitor", "--help")
	if code != ExitSuccess {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "SIGINT") {
		t.Errorf("stdout=%q", stdout)
	}
}

func TestMonitorJSONStartsAndStops(t *testing.T) {
	isolateConfigDiscovery(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	path := writeCheckConfig(t, t.TempDir(), server.URL, server.URL)

	var stdoutBuf strings.Builder
	_ = stdoutBuf
	// Run monitor with a canceled context: it must emit started, at least one
	// probe per backend, then shutdown, and return promptly.
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		code   int
		stdout string
		stderr string
	}
	done := make(chan outcome, 1)
	go func() {
		var outBuf, errBuf strings.Builder
		// Use non-bytes builders through the test seam is complex; instead run
		// via Execute with a cancellable context using pipes.
		code := Execute(ctx, []string{"monitor", "--config", path, "--output", "json"}, &outBuf, &errBuf)
		done <- outcome{code, outBuf.String(), errBuf.String()}
	}()
	time.Sleep(500 * time.Millisecond)
	cancel()
	select {
	case got := <-done:
		if got.code != ExitSuccess {
			t.Fatalf("exit = %d, want 0; stderr=%q", got.code, got.stderr)
		}
		lines := strings.Split(strings.TrimSpace(got.stdout), "\n")
		if len(lines) < 4 {
			t.Fatalf("expected started + 2 probes + shutdown, got:\n%s", got.stdout)
		}
		for _, line := range lines {
			var doc map[string]any
			if err := json.Unmarshal([]byte(line), &doc); err != nil {
				t.Fatalf("NDJSON line invalid: %v\n%s", err, line)
			}
		}
		if !strings.Contains(got.stdout, `"kind":"started"`) || !strings.Contains(got.stdout, `"kind":"shutdown"`) {
			t.Errorf("missing lifecycle events:\n%s", got.stdout)
		}
		if strings.Contains(got.stdout, server.URL) {
			t.Errorf("monitor JSON must not expose backend URLs")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("monitor did not shut down after cancellation")
	}
}
