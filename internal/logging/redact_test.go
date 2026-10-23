package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"
)

func mustTime() time.Time {
	return time.Date(2026, 10, 22, 12, 34, 56, 789_000_000, time.UTC)
}

const (
	credentialURLFixture = "https://" + "alice" + ":" + "correct-horse" + "@example.com/health?token=abc123&x=1 failed"
	userinfoURLFixture   = "https://" + "user" + ":" + "pass" + "@example.com:8443/health?token=abc#frag"
	postgresURLFixture   = "postgres://" + "user" + ":" + "pass" + "@db:5432/app"
)

func TestRedactStringPatterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		secrets     []string
		wantContain string
	}{
		{
			name:        "bearer token",
			input:       "request failed with Authorization: Bearer sk-live-abc123",
			wantContain: redactedValue,
		},
		{
			name:        "basic credentials",
			input:       "upstream rejected Basic dXNlcjpwYXNzd29yZA==",
			wantContain: redactedValue,
		},
		{
			name:        "url userinfo and query",
			input:       "GET " + credentialURLFixture,
			wantContain: "https://example.com",
		},
		{
			name:        "cookie header",
			input:       "Cookie: session=abc123; theme=dark",
			wantContain: redactedValue,
		},
		{
			name:        "api key assignment",
			input:       "X-API-Key: sk-live-key-123",
			wantContain: redactedValue,
		},
		{
			name:        "bare query token",
			input:       "callback /notify?token=abc123 failed",
			wantContain: redactedValue,
		},
		{
			name:        "registered literal secret",
			input:       "credential=super-secret-value",
			secrets:     []string{"super-secret-value"},
			wantContain: redactedValue,
		},
		{
			name:        "registered query-escaped secret",
			input:       "value=" + url.QueryEscape("space value&more"),
			secrets:     []string{"space value&more"},
			wantContain: redactedValue,
		},
		{
			name:        "ordinary text",
			input:       "backend api completed with status healthy",
			wantContain: "backend api completed with status healthy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			redactor := NewRedactor()
			redactor.AddSecrets(tt.secrets...)
			got := redactor.RedactString(tt.input)

			if !strings.Contains(got, tt.wantContain) {
				t.Errorf("RedactString(%q) = %q, want it to contain %q", tt.input, got, tt.wantContain)
			}

			for _, forbidden := range []string{
				"sk-live-abc123",
				"dXNlcjpwYXNzd29yZA==",
				"alice",
				"correct-horse",
				"abc123",
				"session=",
				"theme=dark",
				"sk-live-key-123",
				"super-secret-value",
				"space value",
			} {
				if strings.Contains(got, forbidden) {
					t.Errorf("RedactString(%q) leaked %q: %q", tt.input, forbidden, got)
				}
			}
		})
	}
}

func TestSafeURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{input: userinfoURLFixture, want: "https://example.com:8443"},
		{input: "HTTP://Example.COM/path", want: "http://example.com"},
		{input: "https://example.com", want: "https://example.com"},
		{input: postgresURLFixture, want: redactedURL},
		{input: "not a url", want: redactedURL},
	}

	for _, tt := range tests {
		if got := SafeURL(tt.input); got != tt.want {
			t.Errorf("SafeURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

type safeTestError struct{}

func (safeTestError) Error() string {
	return "raw error contains raw-secret-value"
}

func (safeTestError) SafeMessage() string {
	return "safe failure message"
}

func TestErrorAttributesAreRedactedInTextAndJSON(t *testing.T) {
	t.Parallel()

	const registeredSecret = "registered-session-secret"

	for _, format := range []Format{FormatText, FormatJSON} {
		t.Run(string(format), func(t *testing.T) {
			t.Parallel()

			redactor := NewRedactor()
			redactor.AddSecrets(registeredSecret)

			var output bytes.Buffer
			logger, err := New(Config{Format: format, Writer: &output, Redactor: redactor})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			logger.Error(
				"request failed for https://user:pass@example.com/health?token=abc123",
				"component", "probe",
				"backend_id", "api",
				"operation", "probe https://user:pass@example.com/health?token=abc123",
				"error", fmt.Errorf(
					"Cookie: session=%s; Authorization: Bearer sk-live-secret-token",
					registeredSecret,
				),
			)

			rendered := output.String()
			for _, forbidden := range []string{
				registeredSecret,
				"sk-live-secret-token",
				"user:pass",
				"abc123",
				"session=",
				"Authorization: Bearer sk",
			} {
				if strings.Contains(rendered, forbidden) {
					t.Errorf("log output leaked %q:\n%s", forbidden, rendered)
				}
			}
			if !strings.Contains(rendered, "https://example.com") {
				t.Errorf("log output = %q, want safe URL host", rendered)
			}
			if !strings.Contains(rendered, "backend_id=api") && format == FormatText {
				t.Errorf("log output = %q, want safe backend field", rendered)
			}
			if format == FormatJSON {
				var record map[string]any
				if err := json.Unmarshal([]byte(strings.TrimSpace(rendered)), &record); err != nil {
					t.Fatalf("JSON log is invalid: %v\n%s", err, rendered)
				}
				if record["backend_id"] != "api" {
					t.Errorf("backend_id = %v, want api", record["backend_id"])
				}
				if record["error"] == nil {
					t.Error("error field is missing")
				}
			}
		})
	}
}

func TestSafeMessageErrorInterfaceIsUsed(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger, err := New(Config{Format: FormatJSON, Writer: &output})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	logger.Error("command failed", "error", safeTestError{})

	if strings.Contains(output.String(), "raw-secret-value") {
		t.Errorf("log output leaked the raw error:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "safe failure message") {
		t.Errorf("log output = %q, want the safe message", output.String())
	}
}

func TestReplaceAttrNormalizesTimeAndRedactsGroups(t *testing.T) {
	t.Parallel()

	redactor := NewRedactor()
	attr := redactor.ReplaceAttr(nil, slog.Time(slog.TimeKey, mustTime()))
	if attr.Value.Kind() != slog.KindString || attr.Value.String() != "2026-10-22T12:34:56.789Z" {
		t.Errorf("normalized time = %v, want 2026-10-22T12:34:56.789Z", attr.Value)
	}

	forgedTime := redactor.ReplaceAttr(nil, slog.String(slog.TimeKey, "secret-time-value"))
	if forgedTime.Value.String() != redactedValue {
		t.Errorf("forged time attribute = %v, want %q", forgedTime.Value, redactedValue)
	}

	forgedLevel := redactor.ReplaceAttr(nil, slog.String(slog.LevelKey, "Bearer secret-level"))
	if strings.Contains(forgedLevel.Value.String(), "secret-level") {
		t.Errorf("forged level attribute leaked a value: %v", forgedLevel.Value)
	}

	group := redactor.ReplaceAttr([]string{"request"}, slog.String("header", "secret-value"))
	if group.Value.String() != redactedValue {
		t.Errorf("group attribute = %v, want %q", group.Value, redactedValue)
	}
}

func TestRedactErrorNil(t *testing.T) {
	t.Parallel()

	if got := NewRedactor().RedactError(nil); got != "" {
		t.Errorf("RedactError(nil) = %q, want empty", got)
	}
}

func TestRedactErrorUsesCauseTextWhenUnclassified(t *testing.T) {
	t.Parallel()

	redactor := NewRedactor()
	err := errors.New("token=hidden-value failed")
	got := redactor.RedactError(err)
	if strings.Contains(got, "hidden-value") {
		t.Errorf("RedactError() = %q, leaked secret", got)
	}
}

func TestOperationalCountFieldsAreEmitted(t *testing.T) {
	t.Parallel()

	// Phase 5 status/history/retention logs use numeric count fields. They
	// must be emitted, not redacted, while unknown fields still fail closed.
	redactor := NewRedactor()
	for _, key := range []string{
		"open_incidents", "observation_count", "event_count",
		"observations_deleted", "events_deleted", "incidents_deleted",
	} {
		attr := redactor.ReplaceAttr(nil, slog.Int(key, 3))
		if attr.Value.Kind() == slog.KindString && attr.Value.String() == redactedValue {
			t.Errorf("%s was redacted, want the count emitted", key)
		}
	}
	unknown := redactor.ReplaceAttr(nil, slog.String("webhook_url", "https://example.com/hook"))
	if unknown.Value.String() != redactedValue {
		t.Errorf("unknown field = %v, want %q", unknown.Value, redactedValue)
	}
}
