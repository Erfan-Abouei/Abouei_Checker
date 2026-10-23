package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/probe"
)

type historyBackendSpec struct {
	id                string
	url               string
	failureThreshold  int
	recoveryThreshold int
	notifications     bool
}

func writeHistoryConfig(t *testing.T, dir, name, webhookURL string, backends []historyBackendSpec) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("version: 1\nstorage:\n  database_path: ./data/abouei.db\n")
	if webhookURL != "" {
		fmt.Fprintf(&sb, "notifications:\n  webhook:\n    url: %s\n    timeout: 2s\n  retry:\n    max_attempts: 3\n    initial_backoff: 100ms\n    max_backoff: 1s\n", webhookURL)
	}
	sb.WriteString("backends:\n")
	for _, backend := range backends {
		fmt.Fprintf(&sb, "  - id: %s\n    url: %s\n    interval: 60s\n    timeout: 5s\n    failure_threshold: %d\n    recovery_threshold: %d\n",
			backend.id, backend.url, backend.failureThreshold, backend.recoveryThreshold)
		if backend.notifications {
			sb.WriteString("    notifications:\n      enabled: true\n")
		}
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func failingServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	return server
}

func healthyServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server
}

func okWebhook(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestStatusMissingDatabaseHintsFirstRun(t *testing.T) {
	dir := t.TempDir()
	path := writeHistoryConfig(t, dir, "abouei.yaml", "", []historyBackendSpec{
		{id: "api", url: "http://127.0.0.1:9/health", failureThreshold: 1, recoveryThreshold: 1},
	})
	application := loadDurableApp(t, path)
	_, err := application.Status(context.Background(), "")
	if err == nil {
		t.Fatal("status with no database must fail, not report success")
	}
	if CodeOf(err) != CodeDatabaseUnavailable {
		t.Fatalf("code = %q, want database_unavailable", CodeOf(err))
	}
	if !strings.Contains(SafeMessage(err), "run check or monitor first") {
		t.Fatalf("message = %q, want the first-run hint", SafeMessage(err))
	}
	if _, historyErr := application.History(context.Background(), HistoryOptions{Limit: 10}); historyErr == nil {
		t.Fatal("history with no database must fail, not report success")
	}
}

func TestStatusHealthyAfterCheck(t *testing.T) {
	backend := healthyServer(t)
	dir := t.TempDir()
	path := writeHistoryConfig(t, dir, "abouei.yaml", "", []historyBackendSpec{
		{id: "api", url: backend.URL, failureThreshold: 2, recoveryThreshold: 1},
		{id: "web", url: "http://127.0.0.1:9/status", failureThreshold: 2, recoveryThreshold: 1},
	})
	application := loadDurableApp(t, path)
	if _, _, err := application.Check(context.Background(), []string{"api"}); err != nil {
		t.Fatalf("check: %v", err)
	}

	report, err := application.Status(context.Background(), "")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.OpenIncidents != 0 || len(report.Backends) != 2 {
		t.Fatalf("report = %+v", report)
	}
	if report.PendingNotifications != 0 || report.FailedNotifications != 0 {
		t.Fatalf("no notifications expected: %+v", report)
	}
	byID := make(map[string]BackendStatus)
	for _, view := range report.Backends {
		byID[view.BackendID] = view
	}
	healthy := byID["api"]
	if healthy.Status != StatusHealthy || healthy.LastObservedAt == nil {
		t.Fatalf("api = %+v", healthy)
	}
	if healthy.LastStatusCode == nil || *healthy.LastStatusCode != http.StatusOK {
		t.Fatalf("api status code = %+v", healthy.LastStatusCode)
	}
	if byID["web"].Status != StatusUnknown {
		t.Fatalf("unobserved backend must be unknown: %+v", byID["web"])
	}
}

func TestStatusDownAfterThreshold(t *testing.T) {
	backend := failingServer(t)
	hook := okWebhook(t)
	dir := t.TempDir()
	path := writeHistoryConfig(t, dir, "abouei.yaml", hook.URL+"/hook", []historyBackendSpec{
		{id: "api", url: backend.URL, failureThreshold: 1, recoveryThreshold: 1, notifications: true},
	})
	application := loadDurableApp(t, path)
	_, summary, err := application.Check(context.Background(), nil)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if summary.Unhealthy != 1 || summary.IncidentsOpened != 1 {
		t.Fatalf("summary = %+v", summary)
	}

	report, err := application.Status(context.Background(), "")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.OpenIncidents != 1 || len(report.Backends) != 1 {
		t.Fatalf("report = %+v", report)
	}
	view := report.Backends[0]
	if view.Status != StatusDown || view.OpenIncidentID == nil || *view.OpenIncidentID == "" {
		t.Fatalf("api = %+v", view)
	}
	if view.OpenedAt == nil || view.ConsecutiveFailures != 1 {
		t.Fatalf("api = %+v", view)
	}
}

func TestStatusFailedNotificationsVisible(t *testing.T) {
	backend := failingServer(t)
	dir := t.TempDir()
	// Unreachable webhook: the Down event stays failed and retryable.
	path := writeHistoryConfig(t, dir, "abouei.yaml", "http://127.0.0.1:1/hook", []historyBackendSpec{
		{id: "api", url: backend.URL, failureThreshold: 1, recoveryThreshold: 1, notifications: true},
	})
	application := loadDurableApp(t, path)
	_, summary, err := application.Check(context.Background(), nil)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if summary.NotificationsFailed != 1 {
		t.Fatalf("failed delivery must be counted: %+v", summary)
	}

	report, err := application.Status(context.Background(), "")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.OpenIncidents != 1 {
		t.Fatalf("report = %+v", report)
	}
	if report.PendingNotifications != 1 || report.FailedNotifications != 1 {
		t.Fatalf("failed delivery must be visible: %+v", report)
	}
	filtered, err := application.Status(context.Background(), "api")
	if err != nil {
		t.Fatalf("filtered Status: %v", err)
	}
	if filtered.PendingNotifications != 1 || filtered.FailedNotifications != 1 {
		t.Fatalf("filtered report = %+v", filtered)
	}
}

func TestStatusBackendFilter(t *testing.T) {
	backend := healthyServer(t)
	dir := t.TempDir()
	path := writeHistoryConfig(t, dir, "abouei.yaml", "", []historyBackendSpec{
		{id: "api", url: backend.URL, failureThreshold: 1, recoveryThreshold: 1},
		{id: "web", url: backend.URL, failureThreshold: 1, recoveryThreshold: 1},
	})
	application := loadDurableApp(t, path)
	if _, _, err := application.Check(context.Background(), nil); err != nil {
		t.Fatalf("check: %v", err)
	}
	report, err := application.Status(context.Background(), "web")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(report.Backends) != 1 || report.Backends[0].BackendID != "web" {
		t.Fatalf("filtered report = %+v", report)
	}
	if _, err := application.Status(context.Background(), "missing"); err == nil {
		t.Fatal("unknown backend filter must be a usage error")
	} else if CodeOf(err) != CodeBackendNotFound {
		t.Fatalf("code = %q, want backend_not_found", CodeOf(err))
	}
}

func TestStatusOrphanedIncidentStaysVisible(t *testing.T) {
	backend := failingServer(t)
	hook := okWebhook(t)
	dir := t.TempDir()
	twoBackends := []historyBackendSpec{
		{id: "api", url: backend.URL, failureThreshold: 1, recoveryThreshold: 1, notifications: true},
		{id: "temp", url: backend.URL, failureThreshold: 1, recoveryThreshold: 1, notifications: true},
	}
	first := writeHistoryConfig(t, dir, "a.yaml", hook.URL+"/hook", twoBackends)
	application := loadDurableApp(t, first)
	if _, summary, err := application.Check(context.Background(), nil); err != nil {
		t.Fatalf("check: %v", err)
	} else if summary.IncidentsOpened != 2 {
		t.Fatalf("summary = %+v", summary)
	}

	// The backend is removed from configuration; its incident becomes
	// orphaned. The database file is shared through the same directory.
	second := writeHistoryConfig(t, dir, "b.yaml", hook.URL+"/hook", twoBackends[:1])
	restarted := loadDurableApp(t, second)
	report, err := restarted.Status(context.Background(), "")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.OpenIncidents != 2 || len(report.Backends) != 2 {
		t.Fatalf("report = %+v", report)
	}
	if report.Backends[0].BackendID != "api" || report.Backends[0].Status != StatusDown {
		t.Fatalf("configured backend = %+v", report.Backends[0])
	}
	orphan := report.Backends[1]
	if orphan.BackendID != "temp" || orphan.Status != StatusOrphaned {
		t.Fatalf("orphan = %+v", orphan)
	}
	if orphan.OpenIncidentID == nil {
		t.Fatal("orphaned incident id must be visible")
	}
	// Filtering to the orphan still shows it.
	filtered, err := restarted.Status(context.Background(), "temp")
	if err != nil {
		t.Fatalf("filtered Status: %v", err)
	}
	if filtered.OpenIncidents != 1 || filtered.Backends[0].Status != StatusOrphaned {
		t.Fatalf("filtered report = %+v", filtered)
	}
}

func TestHistoryAfterCheck(t *testing.T) {
	backend := failingServer(t)
	hook := okWebhook(t)
	dir := t.TempDir()
	path := writeHistoryConfig(t, dir, "abouei.yaml", hook.URL+"/hook", []historyBackendSpec{
		{id: "api", url: backend.URL, failureThreshold: 1, recoveryThreshold: 1, notifications: true},
	})
	application := loadDurableApp(t, path)
	for range 2 {
		if _, _, err := application.Check(context.Background(), nil); err != nil {
			t.Fatalf("check: %v", err)
		}
	}

	report, err := application.History(context.Background(), HistoryOptions{Limit: 50})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(report.Observations) != 2 || report.HasMore {
		t.Fatalf("observations = %d has_more=%v", len(report.Observations), report.HasMore)
	}
	if !report.Observations[0].ObservedAt.After(report.Observations[1].ObservedAt) &&
		!report.Observations[0].ObservedAt.Equal(report.Observations[1].ObservedAt) {
		t.Fatal("observations must be newest-first")
	}
	if report.Observations[0].Outcome != "failed" || report.Observations[0].StatusCode == nil {
		t.Fatalf("observation = %+v", report.Observations[0])
	}
	if len(report.Events) != 1 || report.Events[0].EventType != "incident.opened" {
		t.Fatalf("events = %+v", report.Events)
	}
	if report.Events[0].Sequence != 1 || report.Events[0].Status != "delivered" {
		t.Fatalf("event = %+v", report.Events[0])
	}

	limited, err := application.History(context.Background(), HistoryOptions{Limit: 1})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(limited.Observations) != 1 || !limited.HasMore {
		t.Fatalf("limit 1 must truncate with has_more: %+v", limited)
	}
}

func TestHistoryEmptyIsValid(t *testing.T) {
	dir := t.TempDir()
	path := writeHistoryConfig(t, dir, "abouei.yaml", "", []historyBackendSpec{
		{id: "api", url: "http://127.0.0.1:9/health", failureThreshold: 1, recoveryThreshold: 1},
	})
	application := loadDurableApp(t, path)
	db, err := application.OpenStore()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	_ = db.Close()

	report, err := application.History(context.Background(), HistoryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(report.Observations) != 0 || len(report.Events) != 0 || report.HasMore {
		t.Fatalf("empty history must be valid: %+v", report)
	}
}

func TestHistoryFilters(t *testing.T) {
	backend := failingServer(t)
	dir := t.TempDir()
	path := writeHistoryConfig(t, dir, "abouei.yaml", "", []historyBackendSpec{
		{id: "api", url: backend.URL, failureThreshold: 100, recoveryThreshold: 1},
		{id: "web", url: backend.URL, failureThreshold: 100, recoveryThreshold: 1},
	})
	application := loadDurableApp(t, path)
	if _, _, err := application.Check(context.Background(), nil); err != nil {
		t.Fatalf("check: %v", err)
	}

	filtered, err := application.History(context.Background(), HistoryOptions{BackendID: "web", Limit: 50})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(filtered.Observations) != 1 || filtered.Observations[0].BackendID != "web" {
		t.Fatalf("backend filter = %+v", filtered.Observations)
	}

	future := time.Now().UTC().Add(time.Hour)
	empty, err := application.History(context.Background(), HistoryOptions{Since: &future, Limit: 50})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(empty.Observations) != 0 || len(empty.Events) != 0 || empty.HasMore {
		t.Fatalf("future since must be empty: %+v", empty)
	}

	unknown, err := application.History(context.Background(), HistoryOptions{BackendID: "missing", Limit: 50})
	if err != nil {
		t.Fatalf("unknown backend history must be empty, not an error: %v", err)
	}
	if len(unknown.Observations) != 0 || unknown.HasMore {
		t.Fatalf("unknown backend = %+v", unknown)
	}
}

func TestRestartPreservesStatusHistoryAndPending(t *testing.T) {
	backend := failingServer(t)
	hook := okWebhook(t)
	dir := t.TempDir()
	path := writeHistoryConfig(t, dir, "abouei.yaml", hook.URL+"/hook", []historyBackendSpec{
		{id: "api", url: backend.URL, failureThreshold: 1, recoveryThreshold: 1, notifications: true},
	})
	first := loadDurableApp(t, path)
	if _, _, err := first.Check(context.Background(), nil); err != nil {
		t.Fatalf("check: %v", err)
	}
	before, err := first.Status(context.Background(), "")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	beforeHistory, err := first.History(context.Background(), HistoryOptions{Limit: 50})
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	// A new application against the same database observes the same state:
	// open incident, streak, and delivered Down event, with no duplicate.
	second := loadDurableApp(t, path)
	after, err := second.Status(context.Background(), "")
	if err != nil {
		t.Fatalf("Status after restart: %v", err)
	}
	if after.OpenIncidents != 1 || len(after.Backends) != 1 {
		t.Fatalf("after = %+v", after)
	}
	if *after.Backends[0].OpenIncidentID != *before.Backends[0].OpenIncidentID {
		t.Fatal("restart must preserve the open incident identity")
	}
	afterHistory, err := second.History(context.Background(), HistoryOptions{Limit: 50})
	if err != nil {
		t.Fatalf("History after restart: %v", err)
	}
	if len(afterHistory.Observations) != len(beforeHistory.Observations) ||
		len(afterHistory.Events) != len(beforeHistory.Events) {
		t.Fatalf("history changed across restart: %+v vs %+v", beforeHistory, afterHistory)
	}

	// Still failing: no duplicate Down event is created on restart.
	if _, _, err := second.Check(context.Background(), nil); err != nil {
		t.Fatalf("check: %v", err)
	}
	third := loadDurableApp(t, path)
	final, err := third.History(context.Background(), HistoryOptions{Limit: 50})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	opened := 0
	for _, event := range final.Events {
		if event.EventType == "incident.opened" {
			opened++
		}
	}
	if opened != 1 {
		t.Fatalf("restart created %d Down events, want 1: %+v", opened, final.Events)
	}
}

func TestRestartWhileRecoveredClosesWithOneRecovery(t *testing.T) {
	failing := failingServer(t)
	healthy := healthyServer(t)
	hook := okWebhook(t)
	dir := t.TempDir()
	downConfig := writeHistoryConfig(t, dir, "down.yaml", hook.URL+"/hook", []historyBackendSpec{
		{id: "api", url: failing.URL, failureThreshold: 1, recoveryThreshold: 1, notifications: true},
	})
	first := loadDurableApp(t, downConfig)
	if _, _, err := first.Check(context.Background(), nil); err != nil {
		t.Fatalf("check: %v", err)
	}

	upConfig := writeHistoryConfig(t, dir, "up.yaml", hook.URL+"/hook", []historyBackendSpec{
		{id: "api", url: healthy.URL, failureThreshold: 1, recoveryThreshold: 1, notifications: true},
	})
	second := loadDurableApp(t, upConfig)
	// Same database file: both configs live in dir with ./data/abouei.db.
	results, _, err := second.Check(context.Background(), nil)
	if err != nil {
		t.Fatalf("recovery check: %v", err)
	}
	if len(results) != 1 || results[0].IncidentStatus != "recovered" {
		t.Fatalf("results = %+v", results)
	}
	report, err := second.Status(context.Background(), "")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.OpenIncidents != 0 {
		t.Fatalf("incident must be closed: %+v", report)
	}
	history, err := second.History(context.Background(), HistoryOptions{Limit: 50})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	types := map[string]int{}
	for _, event := range history.Events {
		types[event.EventType]++
	}
	if types["incident.opened"] != 1 || types["incident.recovered"] != 1 {
		t.Fatalf("events = %+v", history.Events)
	}
}

func TestCrashWindowDeliversWithoutDuplicateDown(t *testing.T) {
	failing := failingServer(t)
	healthy := healthyServer(t)
	hook := okWebhook(t)
	dir := t.TempDir()
	downConfig := writeHistoryConfig(t, dir, "down.yaml", hook.URL+"/hook", []historyBackendSpec{
		{id: "api", url: failing.URL, failureThreshold: 1, recoveryThreshold: 1, notifications: true},
	})
	first := loadDurableApp(t, downConfig)
	db, err := first.OpenStore()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	outcome, err := db.ProcessResult(context.Background(), first.Config().Backends[0], failingProbe(t, "api"), time.Now().UTC())
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if outcome.IncidentID == "" {
		t.Fatalf("must open an incident: %+v", outcome)
	}
	claimed, err := db.ClaimDueEvent(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Crash before the delivery status commit: close with the row delivering.
	_ = db.Close()

	upConfig := writeHistoryConfig(t, dir, "up.yaml", hook.URL+"/hook", []historyBackendSpec{
		{id: "api", url: healthy.URL, failureThreshold: 1, recoveryThreshold: 1, notifications: true},
	})
	recovered := loadDurableApp(t, upConfig)
	results, _, err := recovered.Check(context.Background(), nil)
	if err != nil {
		t.Fatalf("recovery after crash: %v", err)
	}
	if results[0].IncidentStatus != "recovered" {
		t.Fatalf("results = %+v", results)
	}
	history, err := recovered.History(context.Background(), HistoryOptions{Limit: 50})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	opened := 0
	for _, entry := range history.Events {
		if entry.EventType == "incident.opened" {
			opened++
			if entry.IncidentID != claimed.IncidentID || entry.Sequence != claimed.Sequence {
				t.Fatalf("Down event identity changed: %+v vs claim %+v", entry, claimed)
			}
		}
	}
	if opened != 1 {
		t.Fatalf("crash window duplicated Down: %+v", history.Events)
	}
}

func TestMigrationDoesNotEraseData(t *testing.T) {
	backend := failingServer(t)
	hook := okWebhook(t)
	dir := t.TempDir()
	path := writeHistoryConfig(t, dir, "abouei.yaml", hook.URL+"/hook", []historyBackendSpec{
		{id: "api", url: backend.URL, failureThreshold: 1, recoveryThreshold: 1, notifications: true},
	})
	first := loadDurableApp(t, path)
	if _, _, err := first.Check(context.Background(), nil); err != nil {
		t.Fatalf("check: %v", err)
	}
	before, err := first.History(context.Background(), HistoryOptions{Limit: 50})
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	second := loadDurableApp(t, path)
	db, err := second.OpenStore()
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = db.Close()
	after, err := second.History(context.Background(), HistoryOptions{Limit: 50})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(after.Observations) != len(before.Observations) || len(after.Events) != len(before.Events) {
		t.Fatalf("migration erased data: %+v vs %+v", before, after)
	}
}

func TestDatabaseFailuresSurfaced(t *testing.T) {
	dir := t.TempDir()
	path := writeHistoryConfig(t, dir, "abouei.yaml", "", []historyBackendSpec{
		{id: "api", url: "http://127.0.0.1:9/health", failureThreshold: 1, recoveryThreshold: 1},
	})
	application := loadDurableApp(t, path)
	dbPath := filepath.Join(dir, "data", "abouei.db")
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dbPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := application.Status(context.Background(), ""); err == nil {
		t.Fatal("corrupt database must fail, not report success")
	} else if CategoryOf(err) != CategoryInternal {
		t.Fatalf("category = %v, want internal", CategoryOf(err))
	}
	if _, err := application.History(context.Background(), HistoryOptions{Limit: 5}); err == nil {
		t.Fatal("corrupt database must fail history, not report success")
	}
	// The failed open must not have deleted or repaired the file.
	content, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(content) != "corrupt" {
		t.Fatal("corrupt database was modified by a failed status query")
	}

	// A directory at the database path is a safe operational error.
	subdir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(filepath.Join(subdir, "data", "abouei.db"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dirConfig := writeHistoryConfig(t, subdir, "d.yaml", "", []historyBackendSpec{
		{id: "api", url: "http://127.0.0.1:9/health", failureThreshold: 1, recoveryThreshold: 1},
	})
	dirApp := loadDurableApp(t, dirConfig)
	if _, err := dirApp.Status(context.Background(), ""); err == nil {
		t.Fatal("directory database path must fail, not report success")
	} else if CategoryOf(err) != CategoryInternal {
		t.Fatalf("category = %v, want internal", CategoryOf(err))
	}
}

func TestStartupRetentionDeletesExpiredObservations(t *testing.T) {
	backend := healthyServer(t)
	dir := t.TempDir()
	path := writeHistoryConfig(t, dir, "abouei.yaml", "", []historyBackendSpec{
		{id: "api", url: backend.URL, failureThreshold: 100, recoveryThreshold: 1},
	})
	application := loadDurableApp(t, path)
	db, err := application.OpenStore()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	old := failingProbe(t, "api")
	old.ObservedAt = time.Now().UTC().Add(-800 * time.Hour)
	if _, err := db.ProcessResult(context.Background(), application.Config().Backends[0], old, old.ObservedAt); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	_ = db.Close()

	// check runs retention on startup with the default 720h observation
	// window, so the 800h-old row must disappear while the fresh probe stays.
	if _, _, err := application.Check(context.Background(), nil); err != nil {
		t.Fatalf("check: %v", err)
	}
	report, err := application.History(context.Background(), HistoryOptions{Limit: 50})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(report.Observations) != 1 || report.Observations[0].Outcome != "healthy" {
		t.Fatalf("expired row survived startup retention: %+v", report.Observations)
	}
}

func TestRunRetentionFailureIsBestEffort(t *testing.T) {
	dir := t.TempDir()
	path := writeHistoryConfig(t, dir, "abouei.yaml", "", []historyBackendSpec{
		{id: "api", url: "http://127.0.0.1:9/health", failureThreshold: 1, recoveryThreshold: 1},
	})
	application := loadDurableApp(t, path)
	db, err := application.OpenStore()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	_ = db.Close()
	// A closed handle makes retention fail; runRetention must swallow it.
	if got := application.runRetention(context.Background(), db); got.ObservationsDeleted+got.EventsDeleted+got.IncidentsDeleted != 0 {
		t.Fatalf("failed retention must report zero deletions: %+v", got)
	}
}

func failingProbe(t *testing.T, backendID string) probe.Result {
	t.Helper()
	return probe.Result{
		BackendID:     backendID,
		Name:          backendID,
		Outcome:       probe.OutcomeUnhealthy,
		HTTPStatus:    http.StatusServiceUnavailable,
		Duration:      42 * time.Millisecond,
		ObservedAt:    time.Now().UTC(),
		FailureReason: probe.ReasonStatusMismatch,
		Error:         "accepted status codes do not include 503",
	}
}
