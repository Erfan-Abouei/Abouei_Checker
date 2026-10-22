package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAppTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func environmentLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func TestLoadConfigReturnsSafeSummaryAndStoresConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeAppTestFile(t, dir, "config.yaml", `version: 1
logging:
  level: debug
  format: json
backends:
  - id: api
    url: https://example.com/health
    headers:
      Authorization: Bearer ${API_TOKEN}
  - id: web
    url: https://example.com/status
`)
	var logs bytes.Buffer
	application := newTestApp(t, Options{
		ConfigPath: path,
		LogWriter:  &logs,
		LookupEnv:  environmentLookup(map[string]string{"API_TOKEN": "resolved-secret"}),
	})

	summary, err := application.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if summary.Version != 1 || summary.SourcePath != path {
		t.Errorf("summary = %+v", summary)
	}
	if len(summary.BackendIDs) != 2 || summary.BackendIDs[0] != "api" || summary.BackendIDs[1] != "web" {
		t.Errorf("BackendIDs = %v", summary.BackendIDs)
	}
	if application.Config() == nil || len(application.Config().Backends) != 2 {
		t.Errorf("Config() = %+v, want loaded configuration", application.Config())
	}

	redacted := application.Redactor().RedactString("token is resolved-secret")
	if strings.Contains(redacted, "resolved-secret") {
		t.Errorf("resolved secret was not registered: %q", redacted)
	}

	// Configuration logging.level and format apply when --log-level is absent.
	application.Logger().Debug("configured debug record", "component", "app")
	if !strings.Contains(logs.String(), `"level":"DEBUG"`) {
		t.Errorf("logs = %q, want JSON debug output from configuration", logs.String())
	}
	if !strings.Contains(logs.String(), `"component":"app"`) {
		t.Errorf("logs = %q, want JSON fields", logs.String())
	}
}

func TestLoadConfigExplicitLogLevelWins(t *testing.T) {
	dir := t.TempDir()
	path := writeAppTestFile(t, dir, "config.yaml", `version: 1
logging:
  level: debug
backends:
  - id: api
    url: https://example.com/health
`)
	var logs bytes.Buffer
	application := newTestApp(t, Options{
		ConfigPath:  path,
		LogLevel:    "error",
		LogLevelSet: true,
		LogWriter:   &logs,
	})

	if _, err := application.LoadConfig(); err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	application.Logger().Debug("should be suppressed", "component", "app")
	application.Logger().Info("should be suppressed", "component", "app")
	application.Logger().Error("visible", "component", "app")

	if strings.Contains(logs.String(), "should be suppressed") {
		t.Errorf("logs = %q, explicit --log-level did not win", logs.String())
	}
	if !strings.Contains(logs.String(), "visible") {
		t.Errorf("logs = %q, want the error record", logs.String())
	}
}

func TestLoadConfigMissingFileIsConfigurationError(t *testing.T) {
	var logs bytes.Buffer
	application := newTestApp(t, Options{
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
		LogWriter:  &logs,
	})

	_, err := application.LoadConfig()
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want a configuration error")
	}
	if CategoryOf(err) != CategoryConfiguration {
		t.Errorf("CategoryOf(err) = %v, want configuration", CategoryOf(err))
	}
	if CodeOf(err) != CodeConfigNotFound {
		t.Errorf("CodeOf(err) = %q, want %q", CodeOf(err), CodeConfigNotFound)
	}
}

func TestLoadConfigInvalidFileCarriesSafeDetails(t *testing.T) {
	const secret = "header-secret-value"
	dir := t.TempDir()
	path := writeAppTestFile(t, dir, "config.yaml", `version: 1
backends:
  - id: api
    url: not-a-url
    timeout: 10s
    interval: 5s
    headers:
      Authorization: Bearer ${TOKEN}
`)

	var logs bytes.Buffer
	application := newTestApp(t, Options{
		ConfigPath: path,
		LogWriter:  &logs,
		LookupEnv:  environmentLookup(map[string]string{"TOKEN": secret}),
	})

	_, err := application.LoadConfig()
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want a validation error")
	}
	if CategoryOf(err) != CategoryConfiguration || CodeOf(err) != CodeConfigInvalid {
		t.Errorf("classification = %v/%q, want configuration/config_invalid", CategoryOf(err), CodeOf(err))
	}
	if strings.Contains(SafeMessage(err), secret) {
		t.Fatalf("safe message leaked a resolved secret: %q", SafeMessage(err))
	}

	var appErr *Error
	if !errors.As(err, &appErr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if len(appErr.Details) == 0 {
		t.Fatal("Details is empty, want field-level details")
	}
	for _, detail := range appErr.Details {
		if strings.Contains(detail.Rule, secret) {
			t.Errorf("detail leaked a secret: %+v", detail)
		}
	}
}

func TestWriteConfigTemplateClassification(t *testing.T) {
	dir := t.TempDir()
	var logs bytes.Buffer
	application := newTestApp(t, Options{LogWriter: &logs})

	path := filepath.Join(dir, "abouei.yaml")
	overwritten, err := application.WriteConfigTemplate(path, false)
	if err != nil || overwritten {
		t.Fatalf("WriteConfigTemplate() = %v, %v; want created", overwritten, err)
	}

	_, err = application.WriteConfigTemplate(path, false)
	if CategoryOf(err) != CategoryUsage {
		t.Errorf("existing target category = %v, want usage", CategoryOf(err))
	}

	_, err = application.WriteConfigTemplate(dir, false)
	if CategoryOf(err) != CategoryUsage {
		t.Errorf("directory target category = %v, want usage", CategoryOf(err))
	}

	overwritten, err = application.WriteConfigTemplate(path, true)
	if err != nil || !overwritten {
		t.Fatalf("force overwrite = %v, %v; want overwritten", overwritten, err)
	}
}

func TestConfigIsNilBeforeLoading(t *testing.T) {
	application := newTestApp(t, Options{LogWriter: &bytes.Buffer{}})
	if application.Config() != nil {
		t.Errorf("Config() = %+v, want nil before LoadConfig", application.Config())
	}

	var nilApp *App
	if nilApp.Config() != nil {
		t.Error("nil App Config() != nil")
	}
	if _, err := nilApp.LoadConfig(); CategoryOf(err) != CategoryInternal {
		t.Errorf("nil App LoadConfig() category = %v, want internal", CategoryOf(err))
	}
	if _, err := nilApp.WriteConfigTemplate("x", false); err == nil {
		t.Error("nil App WriteConfigTemplate() error = nil")
	}
}

func TestLoadConfigAppliesConfigurationFormat(t *testing.T) {
	dir := t.TempDir()
	path := writeAppTestFile(t, dir, "config.yaml", `version: 1
logging:
  format: json
backends:
  - id: api
    url: https://example.com/health
`)
	var logs bytes.Buffer
	application := newTestApp(t, Options{
		ConfigPath: path,
		LogLevel:   "info",
		LogWriter:  &logs,
	})

	if _, err := application.LoadConfig(); err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	application.Logger().Info("server started", "component", "app")
	if !strings.Contains(logs.String(), `"msg":"server started"`) {
		t.Errorf("logs = %q, want JSON format from configuration", logs.String())
	}
}
