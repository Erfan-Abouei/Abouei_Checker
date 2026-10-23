package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/app"
)

// forceInteractive stubs TTY detection so the test simulates an interactive
// terminal. It returns a restore function for the caller to defer.
func forceInteractive(t *testing.T) func() {
	t.Helper()
	original := isInteractive
	isInteractive = func(_ io.Writer) bool { return true }
	return func() { isInteractive = original }
}

func TestTUIRejectsJSONOutput(t *testing.T) {
	code, stdout, stderr := executeForTest(t, "tui", "--output", "json")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	// JSON mode keeps the machine contract: one error envelope on stdout,
	// one Error line on stderr, no dashboard.
	var got struct {
		Command string `json:"command"`
		Status  string `json:"status"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if got.Command != "tui" || got.Status != "error" || got.Error.Code != "usage_invalid" {
		t.Errorf("envelope = %+v", got)
	}
	if !strings.Contains(stderr, "json") {
		t.Errorf("stderr = %q, want a JSON rejection", stderr)
	}
}

func TestTUIRequiresInteractiveTerminal(t *testing.T) {
	// Test streams are buffers, never TTYs, so the guard must refuse to
	// launch instead of hanging.
	code, stdout, stderr := executeForTest(t, "tui")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "interactive terminal") {
		t.Errorf("stderr = %q, want an interactive-terminal error", stderr)
	}
}

func TestTUIRejectsArguments(t *testing.T) {
	code, _, stderr := executeForTest(t, "tui", "extra")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "accepts no arguments") {
		t.Errorf("stderr = %q, want an argument validation error", stderr)
	}
}

func TestTUIHelpShowsUsage(t *testing.T) {
	code, stdout, _ := executeForTest(t, "tui", "--help")
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d", code, ExitSuccess)
	}
	for _, want := range []string{"interactive", "--config", "enter", "q"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q", want)
		}
	}
}

func TestDefaultNoArgsShowsHelpWhenNonInteractive(t *testing.T) {
	// Buffers are never TTYs, so the bare command must fall back to help
	// with dashboard guidance instead of launching the fullscreen TUI.
	code, stdout, _ := executeForTest(t)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d", code, ExitSuccess)
	}
	for _, want := range []string{"Usage:", "tui"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q", want)
		}
	}
}

func TestDefaultNoArgsLaunchesDashboardWhenInteractive(t *testing.T) {
	// The dashboard entry point is stubbed so this test never takes over
	// the terminal; it proves the no-args path dispatches to the TUI.
	original := runDashboard
	runDashboard = func(_ context.Context, _ *app.App) error { return nil }
	t.Cleanup(func() { runDashboard = original })

	restore := forceInteractive(t)
	defer restore()

	dir := t.TempDir()
	path := writeCLIConfig(t, dir, "abouei.yaml", minimalBackendConfig())
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--config", path}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d\nstdout: %s\nstderr: %s", code, ExitSuccess, stdout.String(), stderr.String())
	}
}

func TestDefaultNoArgsPropagatesDashboardError(t *testing.T) {
	want := app.NewError(app.CategoryInternal, app.CodeInternal, "dashboard boom")
	original := runDashboard
	runDashboard = func(_ context.Context, _ *app.App) error { return want }
	t.Cleanup(func() { runDashboard = original })

	restore := forceInteractive(t)
	defer restore()

	dir := t.TempDir()
	path := writeCLIConfig(t, dir, "abouei.yaml", minimalBackendConfig())
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--config", path}, &stdout, &stderr)
	if code != ExitInternal {
		t.Fatalf("exit code = %d, want %d\nstdout: %s\nstderr: %s", code, ExitInternal, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "dashboard boom") {
		t.Errorf("stderr = %q, want the dashboard error", stderr.String())
	}
}

// minimalBackendConfig is the smallest valid configuration that loads
// without network access.
func minimalBackendConfig() string {
	return `version: 1
storage:
  database_path: ./data/abouei.db
backends:
  - id: api
    url: http://127.0.0.1:1/health
    interval: 60s
    timeout: 5s
`
}
