package config

import (
	"strings"
	"testing"
)

func TestParseRejectsEmptyInput(t *testing.T) {
	for name, document := range map[string]string{
		"empty":         "",
		"comments only": "# nothing here\n",
	} {
		t.Run(name, func(t *testing.T) {
			validation := parseErrorForTest(t, document)
			if len(validation.Errors) != 1 || !strings.Contains(validation.Errors[0].Message, "file is empty") {
				t.Fatalf("errors = %v, want one empty-file error", errorStrings(validation))
			}
		})
	}
}

func TestParseRejectsEmptyDocument(t *testing.T) {
	for _, document := range []string{"---\n", "--- # empty\n"} {
		validation := parseErrorForTest(t, document)
		if !strings.Contains(validation.Error(), "document is empty") {
			t.Errorf("error = %q, want an empty-document message", validation.Error())
		}
	}
}

func TestParseRejectsMultipleDocuments(t *testing.T) {
	validation := parseErrorForTest(t, minimalConfig+"---\nversion: 1\n")
	if !strings.Contains(validation.Error(), "exactly one YAML document") {
		t.Errorf("error = %q, want a single-document error", validation.Error())
	}
}

func TestParseRejectsWrongRootType(t *testing.T) {
	tests := map[string]string{
		"sequence": "- version: 1\n",
		"scalar":   "hello\n",
		"null":     "null\n",
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			validation := parseErrorForTest(t, document)
			if !strings.Contains(validation.Error(), "root must be a mapping") &&
				!strings.Contains(validation.Error(), "document is empty") {
				t.Errorf("error = %q, want a root-type error", validation.Error())
			}
		})
	}
}

func TestParseRejectsInvalidYAMLSyntax(t *testing.T) {
	for name, document := range map[string]string{
		"mapping":      "version: 1\nbackends: [\n",
		"bad indent":   "version: 1\n backends: []\n",
		"tab indent":   "version: 1\n\tbackends: []\n",
		"unterminated": "version: 1\nbackends:\n  - id: \"api\n",
	} {
		t.Run(name, func(t *testing.T) {
			validation := parseErrorForTest(t, document)
			if !strings.Contains(validation.Error(), "invalid YAML") &&
				!strings.Contains(validation.Error(), "invalid configuration") {
				t.Errorf("error = %q, want a YAML syntax message", validation.Error())
			}
		})
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	tests := []struct {
		name     string
		document string
		field    string
	}{
		{
			name:     "top level",
			document: minimalConfig + "unknown: true\n",
			field:    "unknown",
		},
		{
			name: "nested section",
			document: `version: 1
storage:
  database_path: ./data/abouei.db
  retention: 30d
backends:
  - id: api
    url: https://example.com/health
`,
			field: "retention",
		},
		{
			name: "backend policy",
			document: `version: 1
backends:
  - id: api
    url: https://example.com/health
    retries: 3
`,
			field: "retries",
		},
		{
			name: "webhook",
			document: `version: 1
notifications:
  webhook:
    url: https://hooks.example.com/abouei
    method: POST
backends:
  - id: api
    url: https://example.com/health
`,
			field: "method",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validation := parseErrorForTest(t, test.document)
			message := validation.Error()
			if !strings.Contains(message, "field "+test.field+" is not a recognized field") {
				t.Errorf("error = %q, want an unknown-field message for %q", message, test.field)
			}
			if strings.Contains(message, "config.raw") || strings.Contains(message, "config.rawConfig") {
				t.Errorf("error = %q, leaked an internal type name", message)
			}
		})
	}
}

func TestParseRejectsDuplicateMappingKeys(t *testing.T) {
	tests := map[string]string{
		"top level": `version: 1
version: 2
backends:
  - id: api
    url: https://example.com/health
`,
		"backend": `version: 1
backends:
  - id: api
    url: https://example.com/health
    interval: 30s
    interval: 60s
`,
		"headers": `version: 1
backends:
  - id: api
    url: https://example.com/health
    headers:
      Authorization: one
      Authorization: two
`,
	}

	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			validation := parseErrorForTest(t, document)
			if !strings.Contains(validation.Error(), "already defined at line") {
				t.Errorf("error = %q, want a duplicate-key message", validation.Error())
			}
		})
	}
}

func TestParseRejectsWrongFieldTypes(t *testing.T) {
	tests := []struct {
		name     string
		document string
		contains string
	}{
		{
			name:     "version string",
			document: "version: \"1\"\nbackends:\n  - id: api\n    url: https://example.com/health\n",
			contains: "cannot unmarshal",
		},
		{
			name:     "backends mapping",
			document: "version: 1\nbackends:\n  id: api\n",
			contains: "cannot unmarshal",
		},
		{
			name:     "interval integer",
			document: "version: 1\nbackends:\n  - id: api\n    url: https://example.com/health\n    interval: 30\n",
			contains: "must be a duration string",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validation := parseErrorForTest(t, test.document)
			if !strings.Contains(validation.Error(), test.contains) {
				t.Errorf("error = %q, want it to contain %q", validation.Error(), test.contains)
			}
			if strings.Contains(validation.Error(), "config.raw") {
				t.Errorf("error = %q, leaked an internal type name", validation.Error())
			}
		})
	}
}

func TestParseWrongTypeDoesNotEchoRawValues(t *testing.T) {
	const planted = "planted-secret-value"
	validation := parseErrorForTest(t, "version: \""+planted+"\"\nbackends:\n  - id: api\n    url: https://example.com/health\n")
	if strings.Contains(validation.Error(), planted) {
		t.Fatalf("error echoed a raw scalar value: %q", validation.Error())
	}
	if !strings.Contains(validation.Error(), "cannot unmarshal") {
		t.Errorf("error = %q, want a type-mismatch message", validation.Error())
	}
}

func TestParseEnforcesFileSizeLimitForBytes(t *testing.T) {
	document := make([]byte, MaxFileBytes+1)
	for i := range document {
		document[i] = ' '
	}
	validation := parseErrorForTest(t, string(document))
	if !strings.Contains(validation.Error(), "1 MiB") {
		t.Errorf("error = %q, want a size-limit message", validation.Error())
	}
}

func TestParseAllowsYAMLAnchorsAndAliases(t *testing.T) {
	document := `version: 1
defaults:
  interval: 30s
  timeout: 5s
backends:
  - id: first
    url: https://example.com/health
    headers: &common
      X-Monitor: abouei-checker
  - id: second
    url: https://example.com/status
    headers: *common
`
	cfg := parseForTest(t, document)
	if len(cfg.Backends) != 2 {
		t.Fatalf("backends = %d, want 2", len(cfg.Backends))
	}
	for _, backend := range cfg.Backends {
		if len(backend.Headers) != 1 || backend.Headers[0].Name != "X-Monitor" {
			t.Errorf("backend %q headers = %+v, want inherited alias header", backend.ID, backend.Headers)
		}
	}
}

func TestValidationErrorAggregatesAndCapsDetails(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("version: 1\nbackends:\n")
	for i := 0; i < 25; i++ {
		builder.WriteString("  - id: backend-")
		builder.WriteString(string(rune('a' + i)))
		builder.WriteString("\n    url: not-a-url\n")
	}

	validation := parseErrorForTest(t, builder.String())
	if len(validation.Errors) < 25 {
		t.Fatalf("errors = %d, want at least 25", len(validation.Errors))
	}
	if got := len(validation.Details()); got != 20 {
		t.Errorf("Details() length = %d, want 20", got)
	}
	if !strings.Contains(validation.Error(), "more errors not shown") {
		t.Errorf("error = %q, want an omitted-error count", validation.Error())
	}
}

func TestValidationErrorsAreDeterministic(t *testing.T) {
	document := `version: 2
runtime:
  max_concurrent_probes: 0
  shutdown_timeout: 1s
logging:
  level: verbose
  format: yaml
retention:
  observations: 1m
  incidents: 1m
  delivered_events: 1m
notifications:
  webhook:
    url: ftp://example.com/hook
    timeout: 1s
    headers:
      Content-Type: application/json
  retry:
    max_attempts: 100
    initial_backoff: 2m
    max_backoff: 1m
backends:
  - id: API
    name: ""
    url: https://example.com/health
    headers:
      Host: bad
      X-Test: "line\nbreak"
    interval: 5s
    timeout: 10s
    failure_threshold: 0
    recovery_threshold: 101
    max_response_bytes: 0
    accepted_status_codes: [99, 99, 600]
    body_match:
      type: regex
      value: "["
    notifications:
      enabled: true
      notify_on_down: false
      notify_on_recovery: false
      reminder_interval: 0s
`
	first := errorStrings(parseErrorForTest(t, document))
	for attempt := 0; attempt < 10; attempt++ {
		next := errorStrings(parseErrorForTest(t, document))
		if len(next) != len(first) {
			t.Fatalf("attempt %d produced %d errors, first produced %d", attempt, len(next), len(first))
		}
		for i := range next {
			if next[i] != first[i] {
				t.Fatalf("attempt %d error %d = %q, want %q", attempt, i, next[i], first[i])
			}
		}
	}
}
