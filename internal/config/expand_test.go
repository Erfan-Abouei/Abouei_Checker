package config

import (
	"errors"
	"strings"
	"testing"
)

type recordingRegistrar struct {
	values []string
}

func (r *recordingRegistrar) AddSecrets(values ...string) {
	r.values = append(r.values, values...)
}

func TestExpandEnvironmentReferences(t *testing.T) {
	tests := []struct {
		name     string
		document string
		env      map[string]string
		wantURL  string
	}{
		{
			name: "literal value",
			document: `version: 1
backends:
  - id: api
    url: https://example.com/health
`,
			env:     nil,
			wantURL: "https://example.com/health",
		},
		{
			name: "embedded reference",
			document: `version: 1
backends:
  - id: api
    url: https://${HOST}/health
`,
			env:     map[string]string{"HOST": "example.com"},
			wantURL: "https://example.com/health",
		},
		{
			name: "multiple references",
			document: `version: 1
backends:
  - id: api
    url: ${SCHEME}://${HOST}/${PATH}
`,
			env:     map[string]string{"SCHEME": "https", "HOST": "example.com", "PATH": "health"},
			wantURL: "https://example.com/health",
		},
		{
			name: "dollar escape",
			document: `version: 1
backends:
  - id: api
    url: https://example.com/health?price=$$5
`,
			env:     nil,
			wantURL: "https://example.com/health?price=$5",
		},
		{
			name: "escaped reference stays literal",
			document: `version: 1
backends:
  - id: api
    url: https://example.com/$${PATH}
`,
			env:     nil,
			wantURL: "https://example.com/${PATH}",
		},
		{
			name: "bare dollar stays literal",
			document: `version: 1
backends:
  - id: api
    url: https://example.com/$PATH
`,
			env:     nil,
			wantURL: "https://example.com/$PATH",
		},
		{
			name: "resolved value is not rescanned",
			document: `version: 1
backends:
  - id: api
    url: https://example.com/${FIRST}
`,
			env:     map[string]string{"FIRST": "$${SECOND}", "SECOND": "wrong"},
			wantURL: "https://example.com/$${SECOND}",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := Parse([]byte(test.document), ParseOptions{
				LookupEnv: environment(test.env),
			})
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got := cfg.Backends[0].URL; got != test.wantURL {
				t.Errorf("URL = %q, want %q", got, test.wantURL)
			}
		})
	}
}

func TestExpandAppliesToStringFieldsOnly(t *testing.T) {
	document := `version: 1
storage:
  database_path: ${DATA_DIR}/abouei.db
logging:
  level: ${LOG_LEVEL}
  format: json
defaults:
  interval: ${INTERVAL}
  timeout: 5s
  body_match:
    type: contains
    value: ${BODY_MARKER}
backends:
  - id: ${BACKEND_ID}
    name: ${BACKEND_NAME}
    url: ${BACKEND_URL}
    headers:
      X-Token: ${TOKEN}
`
	cfg, err := Parse([]byte(document), ParseOptions{
		Dir: "/srv/abouei",
		LookupEnv: environment(map[string]string{
			"DATA_DIR":     "/var/lib/abouei",
			"LOG_LEVEL":    "DEBUG",
			"INTERVAL":     "45s",
			"BODY_MARKER":  "service=ready",
			"BACKEND_ID":   "api",
			"BACKEND_NAME": "API",
			"BACKEND_URL":  "https://example.com/health",
			"TOKEN":        "token-value",
		}),
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if cfg.Storage.DatabasePath != "/var/lib/abouei/abouei.db" {
		t.Errorf("DatabasePath = %q", cfg.Storage.DatabasePath)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level = %q, want debug after normalization", cfg.Logging.Level)
	}
	backend := cfg.Backends[0]
	if backend.ID != "api" || backend.Name != "API" || backend.URL != "https://example.com/health" {
		t.Errorf("backend = %+v", backend)
	}
	if backend.Interval.String() != "45s" {
		t.Errorf("Interval = %s, want 45s", backend.Interval)
	}
	if backend.BodyMatch == nil || backend.BodyMatch.Value != "service=ready" {
		t.Errorf("BodyMatch = %+v, want the expanded value", backend.BodyMatch)
	}
	if len(backend.Headers) != 1 || backend.Headers[0].Value != "token-value" {
		t.Errorf("Headers = %+v, want the expanded header value", backend.Headers)
	}
}

func TestExpandDoesNotApplyToMappingKeys(t *testing.T) {
	document := `version: 1
backends:
  - id: api
    url: https://example.com/health
    headers:
      ${HEADER_NAME}: value
`
	validation := parseErrorForTest(t, document)
	requireFieldError(t, validation, "backends[0].headers.${HEADER_NAME}", "not a valid HTTP header name")
}

func TestExpandErrors(t *testing.T) {
	tests := []struct {
		name     string
		document string
		env      map[string]string
		field    string
		message  string
	}{
		{
			name: "missing variable",
			document: `version: 1
backends:
  - id: api
    url: https://example.com/health
    headers:
      Authorization: Bearer ${MISSING_TOKEN}
`,
			field:   "backends[0].headers.Authorization",
			message: "environment variable MISSING_TOKEN is not set",
		},
		{
			name: "empty variable",
			document: `version: 1
backends:
  - id: api
    url: https://example.com/health
    headers:
      Authorization: Bearer ${EMPTY_TOKEN}
`,
			env:     map[string]string{"EMPTY_TOKEN": ""},
			field:   "backends[0].headers.Authorization",
			message: "environment variable EMPTY_TOKEN is empty",
		},
		{
			name: "empty name",
			document: `version: 1
backends:
  - id: api
    url: https://example.com/health
    headers:
      Authorization: "${}"
`,
			field:   "backends[0].headers.Authorization",
			message: "malformed environment reference",
		},
		{
			name: "invalid name start",
			document: `version: 1
backends:
  - id: api
    url: https://example.com/health
    headers:
      Authorization: "${1TOKEN}"
`,
			field:   "backends[0].headers.Authorization",
			message: "malformed environment reference",
		},
		{
			name: "invalid name character",
			document: `version: 1
backends:
  - id: api
    url: https://example.com/health
    headers:
      Authorization: "${TO-KEN}"
`,
			field:   "backends[0].headers.Authorization",
			message: "malformed environment reference",
		},
		{
			name: "missing closing brace",
			document: `version: 1
backends:
  - id: api
    url: https://example.com/health
    headers:
      Authorization: "${TOKEN"
`,
			field:   "backends[0].headers.Authorization",
			message: "missing closing brace",
		},
		{
			name: "duration reference",
			document: `version: 1
backends:
  - id: api
    url: https://example.com/health
    interval: ${INTERVAL}
`,
			field:   "backends[0].interval",
			message: "environment variable INTERVAL is not set",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.document), ParseOptions{LookupEnv: environment(test.env)})
			if err == nil {
				t.Fatal("Parse() error = nil, want an expansion error")
			}
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error type = %T, want *ValidationError", err)
			}
			requireFieldError(t, validation, test.field, test.message)
		})
	}
}

func TestExpandErrorsDoNotLeakOtherResolvedValues(t *testing.T) {
	const secret = "super-secret-value"
	document := `version: 1
backends:
  - id: api
    url: https://example.com/health
    headers:
      Authorization: Bearer ${PRESENT_SECRET}
      X-Missing: ${MISSING_SECRET}
`
	validation := parseErrorForTestWithEnv(t, document, map[string]string{"PRESENT_SECRET": secret})
	rendered := validation.Error()
	if strings.Contains(rendered, secret) {
		t.Fatalf("validation error leaked a resolved value: %q", rendered)
	}
	if !strings.Contains(rendered, "MISSING_SECRET") {
		t.Errorf("error = %q, want the missing variable name", rendered)
	}
}

func parseErrorForTestWithEnv(t *testing.T, document string, env map[string]string) *ValidationError {
	t.Helper()
	_, err := Parse([]byte(document), ParseOptions{LookupEnv: environment(env)})
	if err == nil {
		t.Fatal("Parse() error = nil, want a validation error")
	}
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error type = %T, want *ValidationError", err)
	}
	return validation
}

func TestRegisterSecretsUsesResolvedEnvironmentValuesOnly(t *testing.T) {
	document := `version: 1
backends:
  - id: api
    url: https://example.com/health
    headers:
      Authorization: Bearer ${FIRST_TOKEN}
      X-API-Key: ${SECOND_TOKEN}
      X-Literal: literal-value
defaults:
  body_match:
    type: contains
    value: ${BODY_MARKER}
`
	cfg, err := Parse([]byte(document), ParseOptions{
		LookupEnv: environment(map[string]string{
			"FIRST_TOKEN":  "first-secret",
			"SECOND_TOKEN": "second-secret",
			"BODY_MARKER":  "ready",
		}),
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	registrar := &recordingRegistrar{}
	cfg.RegisterSecrets(registrar)

	want := []string{"first-secret", "ready", "second-secret"}
	if len(registrar.values) != len(want) {
		t.Fatalf("registered %v, want %v", registrar.values, want)
	}
	for i := range want {
		if registrar.values[i] != want[i] {
			t.Fatalf("registered %v, want sorted %v", registrar.values, want)
		}
	}
	for _, value := range registrar.values {
		if value == "literal-value" {
			t.Error("literal header value must not be registered as an environment secret")
		}
	}

	var nilConfig *Config
	nilConfig.RegisterSecrets(registrar)
	cfg.RegisterSecrets(nil)
}

func TestExpandAliasedBodyMatchOnlyOnce(t *testing.T) {
	document := `version: 1
defaults:
  body_match: &shared
    type: contains
    value: ${TOKEN}
backends:
  - id: first
    url: https://example.com/health
    body_match: *shared
  - id: second
    url: https://example.com/status
    body_match: *shared
`
	cfg, err := Parse([]byte(document), ParseOptions{
		LookupEnv: environment(map[string]string{"TOKEN": "pa$$word"}),
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	for _, backend := range cfg.Backends {
		if backend.BodyMatch == nil || backend.BodyMatch.Value != "pa$$word" {
			t.Errorf("backend %q BodyMatch = %+v, want the once-expanded literal", backend.ID, backend.BodyMatch)
		}
	}
}
