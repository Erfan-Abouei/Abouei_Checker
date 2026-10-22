package app

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/buildinfo"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/logging"
)

func newTestApp(t *testing.T, options Options) *App {
	t.Helper()

	application, err := New(options)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return application
}

func TestNewUsesDefaultInfoLevelAndTextFormat(t *testing.T) {
	var output bytes.Buffer
	application := newTestApp(t, Options{LogWriter: &output})

	application.Logger().Debug("suppressed debug")
	application.Logger().Info("visible info", "component", "app")

	rendered := output.String()
	if strings.Contains(rendered, "suppressed debug") {
		t.Errorf("default logger emitted debug output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "visible info") || !strings.Contains(rendered, "level=INFO") {
		t.Errorf("logger output = %q, want an info text record", rendered)
	}
}

func TestNewWiresJSONFormatAndContextFields(t *testing.T) {
	var output bytes.Buffer
	application := newTestApp(t, Options{
		LogFormat: logging.FormatJSON,
		LogWriter: &output,
	})

	application.Logger().Info("started", "component", "app", "command", "version")

	rendered := output.String()
	if !strings.Contains(rendered, `"component":"app"`) {
		t.Errorf("logger output = %q, want JSON component field", rendered)
	}
	if !strings.Contains(rendered, `"level":"INFO"`) {
		t.Errorf("logger output = %q, want JSON level field", rendered)
	}
}

func TestNewRejectsInvalidLogLevel(t *testing.T) {
	var output bytes.Buffer
	_, err := New(Options{LogLevel: "verbose", LogWriter: &output})

	if err == nil {
		t.Fatal("New() error = nil, want an invalid log level error")
	}

	var appErr *Error
	if !errors.As(err, &appErr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if appErr.Category != CategoryUsage {
		t.Errorf("Category = %v, want usage", appErr.Category)
	}
	if appErr.Code != CodeUsageInvalid {
		t.Errorf("Code = %q, want %q", appErr.Code, CodeUsageInvalid)
	}
	if !strings.Contains(SafeMessage(err), "invalid log level") {
		t.Errorf("SafeMessage = %q, want invalid log level text", SafeMessage(err))
	}
	if appErr.Unwrap() == nil {
		t.Error("Unwrap() = nil, want the validation cause")
	}
}

func TestNewRejectsInvalidLogFormatSafely(t *testing.T) {
	var output bytes.Buffer
	_, err := New(Options{LogFormat: logging.Format("yaml"), LogWriter: &output})

	if err == nil {
		t.Fatal("New() error = nil, want a logging configuration error")
	}
	if CategoryOf(err) != CategoryInternal {
		t.Errorf("CategoryOf(err) = %v, want internal", CategoryOf(err))
	}
	if SafeMessage(err) != "failed to configure logging" {
		t.Errorf("SafeMessage(err) = %q, want the safe configuration message", SafeMessage(err))
	}
	if strings.Contains(SafeMessage(err), "yaml") {
		t.Errorf("SafeMessage(err) exposed internal cause: %q", SafeMessage(err))
	}
}

func TestNewRequiresLogWriter(t *testing.T) {
	_, err := New(Options{})
	if err == nil {
		t.Fatal("New() error = nil, want a writer error")
	}
	if CategoryOf(err) != CategoryInternal {
		t.Errorf("CategoryOf(err) = %v, want internal", CategoryOf(err))
	}
}

func TestConfigPathIsRecordedWithoutLoading(t *testing.T) {
	var output bytes.Buffer
	const path = "/definitely/not/a/real/config.yaml"

	application := newTestApp(t, Options{ConfigPath: path, LogWriter: &output})

	if got := application.ConfigPath(); got != path {
		t.Errorf("ConfigPath() = %q, want %q", got, path)
	}
	if output.Len() != 0 {
		t.Errorf("logger output = %q, want empty before any log call", output.String())
	}
}

func TestBuildInfoReturnsLinkerMetadata(t *testing.T) {
	originalVersion := buildinfo.Version
	originalCommit := buildinfo.Commit
	originalBuildDate := buildinfo.BuildDate
	t.Cleanup(func() {
		buildinfo.Version = originalVersion
		buildinfo.Commit = originalCommit
		buildinfo.BuildDate = originalBuildDate
	})

	buildinfo.Version = "9.9.9-test"
	buildinfo.Commit = "deadbee"
	buildinfo.BuildDate = "2026-10-22T00:00:00Z"

	var output bytes.Buffer
	application := newTestApp(t, Options{LogWriter: &output})
	info := application.BuildInfo()

	if info.Version != "9.9.9-test" || info.Commit != "deadbee" || info.BuildDate != "2026-10-22T00:00:00Z" {
		t.Errorf("BuildInfo() = %+v, want injected metadata", info)
	}
}

func TestLoggerUsesRegisteredRedaction(t *testing.T) {
	var output bytes.Buffer
	application := newTestApp(t, Options{
		LogFormat: logging.FormatJSON,
		LogLevel:  "debug",
		LogWriter: &output,
	})

	const secret = "app-registered-secret"
	application.Redactor().AddSecrets(secret)
	application.Logger().Error(
		"operation failed",
		"component", "app",
		"error", errors.New("Cookie: session="+secret),
	)

	if strings.Contains(output.String(), secret) {
		t.Errorf("logger leaked a registered secret:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "component") {
		t.Errorf("logger output = %q, want safe context fields", output.String())
	}
}

func TestLoggerNilSafety(t *testing.T) {
	var application *App

	logger := application.Logger()
	if logger == nil {
		t.Fatal("Logger() = nil")
	}
	logger.LogAttrs(t.Context(), slog.LevelInfo, "discarded")
	if application.ConfigPath() != "" {
		t.Errorf("ConfigPath() = %q, want empty", application.ConfigPath())
	}
	if application.Redactor() == nil {
		t.Fatal("Redactor() = nil")
	}
}
