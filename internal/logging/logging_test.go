package logging

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    slog.Level
		wantErr bool
	}{
		{name: "empty defaults to info", raw: "", want: slog.LevelInfo},
		{name: "lowercase debug", raw: "debug", want: slog.LevelDebug},
		{name: "uppercase debug", raw: "DEBUG", want: slog.LevelDebug},
		{name: "mixed case info", raw: "Info", want: slog.LevelInfo},
		{name: "uppercase warn", raw: "WARN", want: slog.LevelWarn},
		{name: "mixed case error", raw: "Error", want: slog.LevelError},
		{name: "invalid", raw: "verbose", wantErr: true},
		{name: "surrounding whitespace is invalid", raw: " info ", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseLevel(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseLevel(%q) error = nil, want an error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLevel(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("ParseLevel(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw     string
		want    Format
		wantErr bool
	}{
		{raw: "", want: FormatText},
		{raw: "text", want: FormatText},
		{raw: "TEXT", want: FormatText},
		{raw: "json", want: FormatJSON},
		{raw: "JSON", want: FormatJSON},
		{raw: "yaml", wantErr: true},
	}

	for _, tt := range tests {
		got, err := ParseFormat(tt.raw)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseFormat(%q) error = nil, want an error", tt.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseFormat(%q) error = %v", tt.raw, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseFormat(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestNewRequiresWriter(t *testing.T) {
	t.Parallel()

	if _, err := New(Config{}); err == nil {
		t.Fatal("New(Config{}) error = nil, want a writer error")
	}
}

func TestNewRejectsUnsupportedFormat(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if _, err := New(Config{Format: Format("yaml"), Writer: &output}); err == nil {
		t.Fatal("New with unsupported format error = nil, want an error")
	}
}

func TestTextAndJSONHandlersEmitAllLevels(t *testing.T) {
	t.Parallel()

	formats := []Format{FormatText, FormatJSON}
	for _, format := range formats {
		t.Run(string(format), func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			logger, err := New(Config{
				Level:  slog.LevelDebug,
				Format: format,
				Writer: &output,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			logger.Debug("debug message", "component", "test")
			logger.Info("info message", "component", "test")
			logger.Warn("warn message", "component", "test")
			logger.Error("error message", "component", "test")

			lines := nonEmptyLines(output.String())
			if len(lines) != 4 {
				t.Fatalf("log lines = %d, want 4\n%s", len(lines), output.String())
			}

			wantLevels := []string{"DEBUG", "INFO", "WARN", "ERROR"}
			wantMessages := []string{"debug message", "info message", "warn message", "error message"}

			for i, line := range lines {
				if !strings.Contains(line, wantMessages[i]) {
					t.Errorf("line %d = %q, want message %q", i, line, wantMessages[i])
				}

				if format == FormatJSON {
					var record map[string]any
					if err := json.Unmarshal([]byte(line), &record); err != nil {
						t.Fatalf("line %d is not valid JSON: %v\n%s", i, err, line)
					}
					assertTimestamp(t, record["time"])
					if record["level"] != wantLevels[i] {
						t.Errorf("level = %v, want %s", record["level"], wantLevels[i])
					}
					if record["component"] != "test" {
						t.Errorf("component = %v, want test", record["component"])
					}
					continue
				}

				if !strings.Contains(line, "level="+wantLevels[i]) {
					t.Errorf("line %d = %q, want level %s", i, line, wantLevels[i])
				}
				if !strings.Contains(line, "component=test") {
					t.Errorf("line %d = %q, want component field", i, line)
				}
				if !strings.Contains(line, "time=") {
					t.Errorf("line %d = %q, want timestamp field", i, line)
				}
			}
		})
	}
}

func TestLevelFiltersLowerRecords(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger, err := New(Config{
		Level:  slog.LevelInfo,
		Format: FormatJSON,
		Writer: &output,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	logger.Debug("suppressed")
	logger.Info("kept info")
	logger.Warn("kept warn")
	logger.Error("kept error")

	lines := nonEmptyLines(output.String())
	if len(lines) != 3 {
		t.Fatalf("log lines = %d, want 3\n%s", len(lines), output.String())
	}
	if strings.Contains(output.String(), "suppressed") {
		t.Errorf("output contains suppressed debug record:\n%s", output.String())
	}
}

func TestStableContextFields(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger, err := New(Config{Format: FormatJSON, Writer: &output})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	logger.Info(
		"probe completed",
		"component", "probe",
		"command", "check",
		"backend_id", "api",
		"operation", "probe",
		"duration_ms", 42,
		"http_status", 200,
		"outcome", "healthy",
		"failure_reason", "none",
		"attempt", 1,
	)

	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(output.String())), &record); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, output.String())
	}

	wantStrings := map[string]string{
		"component":      "probe",
		"command":        "check",
		"backend_id":     "api",
		"operation":      "probe",
		"outcome":        "healthy",
		"failure_reason": "none",
	}
	for key, want := range wantStrings {
		if record[key] != want {
			t.Errorf("%s = %v, want %q", key, record[key], want)
		}
	}
	if record["duration_ms"] != float64(42) {
		t.Errorf("duration_ms = %v, want 42", record["duration_ms"])
	}
	if record["http_status"] != float64(200) {
		t.Errorf("http_status = %v, want 200", record["http_status"])
	}
	if record["attempt"] != float64(1) {
		t.Errorf("attempt = %v, want 1", record["attempt"])
	}
}

func TestUnknownAttributesAreRedacted(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger, err := New(Config{Format: FormatJSON, Writer: &output})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	logger.Info(
		"request started",
		"authorization", "Bearer super-secret-token",
		"headers", map[string]string{"X-API-Key": "sk-live-secret"},
		"component", "probe",
	)

	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(output.String())), &record); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, output.String())
	}
	if record["authorization"] != redactedValue {
		t.Errorf("authorization = %v, want %q", record["authorization"], redactedValue)
	}
	if record["headers"] != redactedValue {
		t.Errorf("headers = %v, want %q", record["headers"], redactedValue)
	}
	if record["component"] != "probe" {
		t.Errorf("component = %v, want probe", record["component"])
	}
	for _, secret := range []string{"super-secret-token", "sk-live-secret"} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("output leaked %q:\n%s", secret, output.String())
		}
	}
}

func TestJSONLogsRemainOneObjectPerLine(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger, err := New(Config{Format: FormatJSON, Writer: &output})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for i := 0; i < 5; i++ {
		logger.Info("record", "component", "test", "attempt", i)
	}

	lines := nonEmptyLines(output.String())
	if len(lines) != 5 {
		t.Fatalf("log lines = %d, want 5", len(lines))
	}
	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i, err)
		}
	}
}

func TestConcurrentLoggerAndRedactorUse(t *testing.T) {
	t.Parallel()

	redactor := NewRedactor()
	logger, err := New(Config{Format: FormatJSON, Writer: io.Discard, Redactor: redactor})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	var wait sync.WaitGroup
	for i := 0; i < 16; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			redactor.AddSecrets("concurrent-secret")
			logger.Info("concurrent record", "component", "test", "attempt", index,
				"operation", "Bearer concurrent-secret")
		}(i)
	}
	wait.Wait()
}

func nonEmptyLines(value string) []string {
	raw := strings.Split(strings.TrimSpace(value), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func assertTimestamp(t *testing.T, value any) {
	t.Helper()

	text, ok := value.(string)
	if !ok {
		t.Fatalf("timestamp = %v, want a string", value)
	}
	if _, err := time.Parse(timestampFormat, text); err != nil {
		t.Errorf("timestamp %q is not in the expected UTC format: %v", text, err)
	}
	if !strings.HasSuffix(text, "Z") {
		t.Errorf("timestamp %q is not UTC", text)
	}
}
