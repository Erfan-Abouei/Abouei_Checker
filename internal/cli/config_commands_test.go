package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

const validCLIConfig = `version: 1
logging:
  level: info
  format: text
backends:
  - id: api
    url: https://example.com/health
    headers:
      Authorization: Bearer ${CLI_TEST_TOKEN}
  - id: web
    url: https://example.com/status
`

func writeCLIConfig(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func isolateConfigDiscovery(t *testing.T) {
	t.Helper()
	t.Setenv("ABOUEI_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if runtime.GOOS == "windows" {
		t.Setenv("AppData", t.TempDir())
	}
}

func TestValidateTextSuccess(t *testing.T) {
	isolateConfigDiscovery(t)
	t.Setenv("CLI_TEST_TOKEN", "token-value")
	dir := t.TempDir()
	path := writeCLIConfig(t, dir, "abouei.yaml", validCLIConfig)

	code, stdout, stderr := executeForTest(t, "validate", "--config", path)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{"configuration is valid", "schema version 1", "2 backends", "api", "web"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
	if strings.Contains(stdout, "token-value") {
		t.Errorf("stdout leaked a resolved secret: %q", stdout)
	}
}

func TestValidateJSONSuccess(t *testing.T) {
	isolateConfigDiscovery(t)
	t.Setenv("CLI_TEST_TOKEN", "token-value")
	dir := t.TempDir()
	path := writeCLIConfig(t, dir, "abouei.yaml", validCLIConfig)

	code, stdout, stderr := executeForTest(t, "validate", "--config", path, "--output", "json")
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	var got struct {
		SchemaVersion string `json:"schema_version"`
		Command       string `json:"command"`
		Status        string `json:"status"`
		Data          struct {
			Valid         bool     `json:"valid"`
			ConfigVersion int      `json:"config_version"`
			BackendCount  int      `json:"backend_count"`
			BackendIDs    []string `json:"backend_ids"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if got.SchemaVersion != "1" || got.Command != "validate" || got.Status != "success" {
		t.Errorf("envelope = %+v", got)
	}
	if !got.Data.Valid || got.Data.ConfigVersion != 1 || got.Data.BackendCount != 2 {
		t.Errorf("data = %+v", got.Data)
	}
	if len(got.Data.BackendIDs) != 2 || got.Data.BackendIDs[0] != "api" || got.Data.BackendIDs[1] != "web" {
		t.Errorf("backend_ids = %v", got.Data.BackendIDs)
	}
	if got.Error != nil {
		t.Errorf("error = %v, want null", got.Error)
	}
	if strings.Contains(stdout, "token-value") {
		t.Errorf("stdout leaked a resolved secret: %q", stdout)
	}
}

func TestValidateInvalidTextFailsSafely(t *testing.T) {
	isolateConfigDiscovery(t)
	dir := t.TempDir()
	path := writeCLIConfig(t, dir, "abouei.yaml", `version: 1
backends:
  - id: api
    url: not-a-url
    timeout: 10s
    interval: 5s
`)

	code, stdout, stderr := executeForTest(t, "validate", "--config", path)
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty in text mode", stdout)
	}
	if got := strings.Count(stderr, "Error:"); got != 1 {
		t.Errorf("stderr = %q, want exactly one Error line", stderr)
	}
	for _, want := range []string{"configuration is invalid", "backends[0].url", "backends[0].timeout"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	}
}

func TestValidateInvalidJSONEmitsErrorEnvelope(t *testing.T) {
	isolateConfigDiscovery(t)
	dir := t.TempDir()
	path := writeCLIConfig(t, dir, "abouei.yaml", `version: 1
backends:
  - id: api
    url: not-a-url
`)

	code, stdout, stderr := executeForTest(t, "validate", "--config", path, "--output", "json")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if got := strings.Count(stderr, "Error:"); got != 1 {
		t.Errorf("stderr = %q, want exactly one Error line", stderr)
	}

	var got struct {
		SchemaVersion string `json:"schema_version"`
		Command       string `json:"command"`
		Status        string `json:"status"`
		Data          any    `json:"data"`
		Error         struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details []struct {
				Field string `json:"field"`
				Rule  string `json:"rule"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if got.SchemaVersion != "1" || got.Command != "validate" || got.Status != "error" {
		t.Errorf("envelope = %+v", got)
	}
	if got.Data != nil {
		t.Errorf("data = %v, want null", got.Data)
	}
	if got.Error.Code != "config_invalid" {
		t.Errorf("error code = %q, want config_invalid", got.Error.Code)
	}
	if len(got.Error.Details) == 0 || got.Error.Details[0].Field != "backends[0].url" {
		t.Errorf("details = %+v, want a backends[0].url detail", got.Error.Details)
	}
}

func TestValidateMissingExplicitPath(t *testing.T) {
	isolateConfigDiscovery(t)
	missing := filepath.Join(t.TempDir(), "missing.yaml")

	code, stdout, stderr := executeForTest(t, "validate", "--config", missing)
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "does not exist") || !strings.Contains(stderr, missing) {
		t.Errorf("stderr = %q, want a missing-path error", stderr)
	}
}

func TestValidateDiscovery(t *testing.T) {
	t.Run("working directory", func(t *testing.T) {
		isolateConfigDiscovery(t)
		dir := t.TempDir()
		t.Chdir(dir)
		writeCLIConfig(t, dir, "abouei.yaml", `version: 1
backends:
  - id: discovered
    url: https://example.com/health
`)

		code, stdout, stderr := executeForTest(t, "validate")
		if code != ExitSuccess {
			t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
		}
		if !strings.Contains(stdout, "discovered") {
			t.Errorf("stdout = %q, want the discovered backend", stdout)
		}
	})

	t.Run("missing default suggests init", func(t *testing.T) {
		isolateConfigDiscovery(t)
		t.Chdir(t.TempDir())

		code, _, stderr := executeForTest(t, "validate")
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d", code, ExitUsage)
		}
		if !strings.Contains(stderr, "abouei-checker init") {
			t.Errorf("stderr = %q, want an init hint", stderr)
		}
	})

	t.Run("environment override", func(t *testing.T) {
		isolateConfigDiscovery(t)
		dir := t.TempDir()
		path := writeCLIConfig(t, dir, "from-env.yaml", `version: 1
backends:
  - id: from-env
    url: https://example.com/health
`)
		t.Setenv("ABOUEI_CONFIG", path)
		t.Chdir(t.TempDir())

		code, stdout, stderr := executeForTest(t, "validate")
		if code != ExitSuccess {
			t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
		}
		if !strings.Contains(stdout, "from-env") {
			t.Errorf("stdout = %q, want the environment-selected backend", stdout)
		}
	})
}

func TestValidateNeverProbesOrCreatesDatabase(t *testing.T) {
	isolateConfigDiscovery(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dir := t.TempDir()
	databasePath := filepath.Join(dir, "data", "abouei.db")
	document := `version: 1
storage:
  database_path: ` + databasePath + `
backends:
  - id: local
    url: ` + server.URL + `/health
`
	path := writeCLIConfig(t, dir, "abouei.yaml", document)

	code, stdout, stderr := executeForTest(t, "validate", "--config", path)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	if strings.Contains(stdout, server.URL) {
		t.Errorf("stdout exposed a backend URL: %q", stdout)
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("validate sent %d HTTP requests, want 0", got)
	}
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Errorf("validate created a database at %s (stat error %v)", databasePath, err)
	}
}

func TestValidateResolvedSecretsAreRedactedFromLogs(t *testing.T) {
	isolateConfigDiscovery(t)
	t.Setenv("CLI_TEST_TOKEN", "very-secret-token")
	dir := t.TempDir()
	path := writeCLIConfig(t, dir, "abouei.yaml", validCLIConfig)

	code, stdout, stderr := executeForTest(t, "validate", "--config", path, "--log-level", "debug")
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	if !strings.Contains(stderr, "configuration validated") {
		t.Errorf("stderr = %q, want a debug log", stderr)
	}
	if strings.Contains(stdout, "very-secret-token") || strings.Contains(stderr, "very-secret-token") {
		t.Error("validate output leaked a resolved secret")
	}
}

func TestValidateRejectsExtraArguments(t *testing.T) {
	isolateConfigDiscovery(t)
	code, stdout, stderr := executeForTest(t, "validate", "extra")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "accepts no arguments") {
		t.Errorf("stderr = %q, want an argument error", stderr)
	}
}

func TestInitCreatesDefaultFileAndValidates(t *testing.T) {
	isolateConfigDiscovery(t)
	dir := t.TempDir()
	t.Chdir(dir)

	code, stdout, stderr := executeForTest(t, "init")
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	if !strings.Contains(stdout, "created abouei.yaml") {
		t.Errorf("stdout = %q, want a created message", stdout)
	}
	path := filepath.Join(dir, "abouei.yaml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("init did not create the file: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}

	code, stdout, stderr = executeForTest(t, "validate", "--config", path)
	if code != ExitSuccess {
		t.Fatalf("validate created template: exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "example") {
		t.Errorf("stdout = %q, want the template backend", stdout)
	}
}

func TestInitRefusesOverwriteWithoutForce(t *testing.T) {
	isolateConfigDiscovery(t)
	dir := t.TempDir()
	t.Chdir(dir)

	if code, _, stderr := executeForTest(t, "init"); code != ExitSuccess {
		t.Fatalf("first init exit = %d, stderr %q", code, stderr)
	}

	code, stdout, stderr := executeForTest(t, "init")
	if code != ExitUsage {
		t.Fatalf("second init exit = %d, want %d", code, ExitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--force") {
		t.Errorf("stderr = %q, want a --force hint", stderr)
	}

	code, stdout, stderr = executeForTest(t, "init", "--force")
	if code != ExitSuccess {
		t.Fatalf("forced init exit = %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "overwrote") {
		t.Errorf("stdout = %q, want an overwritten message", stdout)
	}
}

func TestInitCustomPathAndJSON(t *testing.T) {
	isolateConfigDiscovery(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config", "abouei.yaml")

	code, stdout, stderr := executeForTest(t, "init", path, "--output", "json")
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	var got struct {
		Command string `json:"command"`
		Status  string `json:"status"`
		Data    struct {
			Path        string `json:"path"`
			Overwritten bool   `json:"overwritten"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if got.Command != "init" || got.Status != "success" {
		t.Errorf("envelope = %+v", got)
	}
	if got.Data.Path != path || got.Data.Overwritten {
		t.Errorf("data = %+v", got.Data)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("init did not create the custom path: %v", err)
	}
}

func TestInitRejectsDirectoryTarget(t *testing.T) {
	isolateConfigDiscovery(t)
	dir := t.TempDir()

	code, _, stderr := executeForTest(t, "init", dir)
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "directory") {
		t.Errorf("stderr = %q, want a directory error", stderr)
	}
}

func TestInitDoesNotReadConfiguration(t *testing.T) {
	isolateConfigDiscovery(t)
	dir := t.TempDir()
	t.Chdir(dir)

	code, stdout, stderr := executeForTest(t, "init", "--config", "/definitely/not/a/config.yaml")
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	if !strings.Contains(stdout, "created") {
		t.Errorf("stdout = %q, want a created message", stdout)
	}
}

func TestJSONErrorEnvelopeForApplicationFailure(t *testing.T) {
	isolateConfigDiscovery(t)
	code, stdout, stderr := executeForTest(t, "version", "--output", "json", "--log-level", "verbose")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if got := strings.Count(stderr, "Error:"); got != 1 {
		t.Errorf("stderr = %q, want exactly one Error line", stderr)
	}

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
	if got.Command != "version" || got.Status != "error" || got.Error.Code != "usage_invalid" {
		t.Errorf("envelope = %+v", got)
	}
}

func TestValidateHelp(t *testing.T) {
	code, stdout, stderr := executeForTest(t, "validate", "--help")
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	for _, want := range []string{"Examples:", "--output json", "--config", "never probes"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
}

func TestInitHelp(t *testing.T) {
	code, stdout, stderr := executeForTest(t, "init", "--help")
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d; stderr = %q", code, ExitSuccess, stderr)
	}
	for _, want := range []string{"--force", "abouei.yaml", "Examples:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
}
