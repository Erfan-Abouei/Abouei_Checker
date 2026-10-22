package app

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "abouei.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func configYAML(urlA, urlB string) string {
	return "version: 1\nbackends:\n" +
		"  - id: a\n    url: " + urlA + "\n    interval: 60s\n    timeout: 5s\n" +
		"  - id: b\n    url: " + urlB + "\n    interval: 60s\n    timeout: 5s\n"
}

func TestCheckAllHealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var logs bytes.Buffer
	path := writeTempConfig(t, configYAML(server.URL, server.URL))
	application := newTestApp(t, Options{ConfigPath: path, LogWriter: &logs})
	if _, err := application.LoadConfig(); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	results, summary, err := application.Check(context.Background(), nil)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if summary.Total != 2 || summary.Healthy != 2 || summary.Unhealthy != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(results) != 2 || results[0].BackendID != "a" || results[1].BackendID != "b" {
		t.Fatalf("results must follow config order: %+v", results)
	}
	if logs.Len() == 0 {
		t.Fatal("expected structured probe logs on stderr")
	}
}

func TestCheckMixedReportsUnhealthyCount(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()

	var logs bytes.Buffer
	path := writeTempConfig(t, configYAML(ok.URL, bad.URL))
	application := newTestApp(t, Options{ConfigPath: path, LogWriter: &logs})
	if _, err := application.LoadConfig(); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	results, summary, err := application.Check(context.Background(), nil)
	if err != nil {
		t.Fatalf("Check returns results with no command error: %v", err)
	}
	if summary.Healthy != 1 || summary.Unhealthy != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if results[1].FailureReason != "status_mismatch" {
		t.Fatalf("result[1] = %+v", results[1])
	}
	if results[1].ThresholdReached {
		t.Fatal("single failure must not reach default threshold 3")
	}
}

func TestCheckUnknownBackendMakesNoRequests(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var logs bytes.Buffer
	path := writeTempConfig(t, configYAML(server.URL, server.URL))
	application := newTestApp(t, Options{ConfigPath: path, LogWriter: &logs})
	if _, err := application.LoadConfig(); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	_, _, checkErr := application.Check(context.Background(), []string{"missing"})
	if checkErr == nil {
		t.Fatal("expected unknown backend error")
	}
	if CategoryOf(checkErr) != CategoryUsage || CodeOf(checkErr) != CodeBackendNotFound {
		t.Fatalf("err = %+v", checkErr)
	}
	if hits != 0 {
		t.Fatalf("no requests must run after unknown ID, hits=%d", hits)
	}
}

func TestCheckSelectedSubset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var logs bytes.Buffer
	path := writeTempConfig(t, configYAML(server.URL, server.URL))
	application := newTestApp(t, Options{ConfigPath: path, LogWriter: &logs})
	if _, err := application.LoadConfig(); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	results, summary, err := application.Check(context.Background(), []string{"b"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if summary.Total != 1 || len(results) != 1 || results[0].BackendID != "b" {
		t.Fatalf("subset selection failed: %+v %+v", results, summary)
	}
}

func TestMonitorGracefulShutdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var logs bytes.Buffer
	path := writeTempConfig(t, configYAML(server.URL, server.URL))
	application := newTestApp(t, Options{ConfigPath: path, LogWriter: &logs})
	if _, err := application.LoadConfig(); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan MonitorEvent, 16)
	done := make(chan error, 1)
	go func() {
		done <- application.Monitor(ctx, MonitorOptions{
			OnEvent: func(event MonitorEvent) { events <- event },
		})
	}()
	deadline := time.After(5 * time.Second)
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case event := <-events:
			seen[event.BackendID] = true
			if !event.Healthy {
				t.Fatalf("expected healthy events: %+v", event)
			}
		case <-deadline:
			t.Fatal("timed out waiting for immediate probes")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Monitor: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("monitor did not shut down promptly")
	}
	if !strings.Contains(logs.String(), "monitor started") || !strings.Contains(logs.String(), "monitor stopped") {
		t.Fatalf("expected startup/shutdown logs:\n%s", logs.String())
	}
}

func TestMonitorRequiresLoadedConfig(t *testing.T) {
	var logs bytes.Buffer
	application := newTestApp(t, Options{LogWriter: &logs})
	if err := application.Monitor(context.Background(), MonitorOptions{}); err == nil {
		t.Fatal("expected error without loaded config")
	}
}
