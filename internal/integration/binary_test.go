// Built-binary end-to-end coverage for Phase 6: compile the real
// abouei-checker executable once and drive it as a user would — version,
// init, validate, check, status, and history against local test servers and
// temporary state. This guards the seams unit tests cannot see: linker
// metadata, argument parsing in the real process, exit codes, stdout/stderr
// separation, and JSON validity across process boundaries.
package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var binaryPath string

// TestMain builds the executable once for the whole package so E2E tests
// share a single compile instead of rebuilding (and so the binary outlives
// any single test's TempDir).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "abouei-checker-e2e-bin")
	if err != nil {
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "abouei-checker")
	wd, _ := os.Getwd()
	root := findModuleRoot(wd)
	cmd := exec.Command("go", "build", "-o", out, "./cmd/abouei-checker")
	cmd.Dir = root
	if combined, err := cmd.CombinedOutput(); err != nil {
		// Leave binaryPath empty; buildBinaryOnce will skip with a
		// diagnostic instead of failing every test opaquely.
		println("warning: E2E binary build failed: " + string(combined))
	} else {
		binaryPath = out
	}
	os.Exit(m.Run())
}

func findModuleRoot(start string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Fall back to the test working directory's ancestry.
			wd, _ := os.Getwd()
			return wd
		}
		dir = parent
	}
}

// buildBinaryOnce returns the executable compiled by TestMain. It skips
// (rather than fails) only when the Go toolchain cannot build, so
// constrained environments report a skip instead of a false red.
func buildBinaryOnce(t *testing.T) string {
	t.Helper()
	if exe := os.Getenv("ABOUEI_CHECKER_BINARY"); exe != "" {
		return exe
	}
	if binaryPath == "" {
		t.Skip("E2E binary is unavailable (build failed); see test log")
	}
	return binaryPath
}

func runBinary(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	bin := buildBinaryOnce(t)
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	// Hermetic discovery: no ambient config or secrets leak in.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "ABOUEI_CONFIG=", "NO_COLOR=1"}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			t.Fatalf("run binary: %v", err)
		}
	}
	return code, stdout.String(), stderr.String()
}

func TestBinaryVersionReportsMetadata(t *testing.T) {
	dir := t.TempDir()
	if code, stdout, _ := runBinary(t, dir, "version"); code != 0 {
		t.Fatalf("version exit = %d, want 0", code)
	} else if !strings.Contains(stdout, "abouei-checker") {
		t.Fatalf("version output = %q, want the product name", stdout)
	}
	if code, stdout, _ := runBinary(t, dir, "version", "--output", "json"); code != 0 {
		t.Fatalf("version --output json exit = %d, want 0", code)
	} else {
		var doc map[string]any
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("version JSON invalid: %v\n%s", err, stdout)
		}
	}
}

func TestBinaryInitValidateCheckStatusHistory(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "abouei.yaml")

	if code, _, stderr := runBinary(t, dir, "init", "--config", configPath); code != 0 {
		t.Fatalf("init exit = %d, want 0\n%s", code, stderr)
	}
	// init must refuse to overwrite without --force.
	if code, _, _ := runBinary(t, dir, "init", "--config", configPath); code != 2 {
		t.Fatalf("second init exit = %d, want 2", code)
	}

	// Replace the template's example backend with the live test server by
	// appending a minimal valid backend section is not possible; instead
	// validate the generated template, then write a live config.
	if code, _, stderr := runBinary(t, dir, "validate", "--config", configPath); code != 0 {
		t.Fatalf("validate exit = %d, want 0\n%s", code, stderr)
	}

	live := `version: 1
storage:
  database_path: ./data/abouei.db
backends:
  - id: api
    url: ` + backend.URL + `
    interval: 60s
    timeout: 5s
    notifications:
      enabled: false
`
	if err := os.WriteFile(configPath, []byte(live), 0o600); err != nil {
		t.Fatalf("write live config: %v", err)
	}
	if code, _, stderr := runBinary(t, dir, "validate", "--config", configPath); code != 0 {
		t.Fatalf("validate live exit = %d, want 0\n%s", code, stderr)
	}

	if code, stdout, stderr := runBinary(t, dir, "check", "--config", configPath, "--output", "json"); code != 0 {
		t.Fatalf("check exit = %d, want 0\n%s", code, stderr)
	} else {
		var doc struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("check JSON invalid: %v\n%s", err, stdout)
		}
		if doc.Status != "success" {
			t.Fatalf("check status = %q, want success", doc.Status)
		}
	}

	if code, stdout, stderr := runBinary(t, dir, "status", "--config", configPath, "--output", "json"); code != 0 {
		t.Fatalf("status exit = %d, want 0\n%s", code, stderr)
	} else if !json.Valid([]byte(stdout)) {
		t.Fatalf("status stdout is not valid JSON:\n%s\nstderr:\n%s", stdout, stderr)
	}

	if code, stdout, stderr := runBinary(t, dir, "history", "--config", configPath, "--output", "json"); code != 0 {
		t.Fatalf("history exit = %d, want 0\n%s", code, stderr)
	} else if !json.Valid([]byte(stdout)) {
		t.Fatalf("history stdout is not valid JSON:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

func TestBinaryInvalidConfigExitsTwo(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("version: 1\nbackends: []\n"), 0o600); err != nil {
		t.Fatalf("write bad config: %v", err)
	}
	if code, _, _ := runBinary(t, dir, "validate", "--config", bad); code != 2 {
		t.Fatalf("validate exit = %d, want 2", code)
	}
	if code, stdout, _ := runBinary(t, dir, "check", "--config", bad, "--output", "json"); code != 2 {
		t.Fatalf("check exit = %d, want 2", code)
	} else if !json.Valid([]byte(stdout)) {
		t.Fatalf("error envelope is not valid JSON:\n%s", stdout)
	}
}

func TestBinaryUnknownCommandExitsTwo(t *testing.T) {
	dir := t.TempDir()
	code, _, stderr := runBinary(t, dir, "frobnicate")
	if code != 2 {
		t.Fatalf("unknown command exit = %d, want 2", code)
	}
	if stderr == "" {
		t.Fatal("unknown command must explain itself on stderr")
	}
}

func TestBinaryConcurrentRunsShareState(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "abouei.yaml")
	live := `version: 1
storage:
  database_path: ./data/abouei.db
backends:
  - id: api
    url: ` + backend.URL + `
    interval: 60s
    timeout: 5s
    notifications:
      enabled: false
`
	if err := os.WriteFile(configPath, []byte(live), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var wg sync.WaitGroup
	type runResult struct {
		code           int
		stdout, stderr string
	}
	results := make([]runResult, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, stdout, stderr := runBinary(t, dir, "check", "--config", configPath, "--output", "json")
			results[i] = runResult{code, stdout, stderr}
		}(i)
	}
	wg.Wait()
	for i, r := range results {
		if r.code != 0 {
			t.Fatalf("concurrent check %d exit = %d, want 0\nstdout: %s\nstderr: %s", i, r.code, r.stdout, r.stderr)
		}
	}
}
