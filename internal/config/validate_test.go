package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseFullConfigurationAndNormalize(t *testing.T) {
	document := `version: 1
storage:
  database_path: /var/lib/abouei/monitor.db
runtime:
  max_concurrent_probes: 4
  shutdown_timeout: 45s
logging:
  level: WARN
  format: JSON
defaults:
  interval: 30s
  timeout: 5s
  failure_threshold: 4
  recovery_threshold: 2
  max_response_bytes: 2048
  follow_redirects: true
  accepted_status_codes: [204, 200]
  body_match:
    type: contains
    value: ready
  notifications:
    enabled: true
    notify_on_down: true
    notify_on_recovery: false
    reminder_interval: 30m
    reminder_max: 6
notifications:
  webhook:
    url: https://hooks.example.com/abouei
    timeout: 15s
    headers:
      x-monitor: abouei-checker
      Authorization: Bearer token
  retry:
    max_attempts: 3
    initial_backoff: 2s
    max_backoff: 1m
retention:
  observations: 48h
  incidents: 720h
  delivered_events: 24h
backends:
  - id: api
    name: API
    url: https://api.example.com/health
    headers:
      X-API-Key: secret
    interval: 10s
    timeout: 2s
    failure_threshold: 2
    recovery_threshold: 3
    max_response_bytes: 1024
    follow_redirects: false
    accepted_status_codes: [201]
    body_match:
      type: regex
      value: '"ok":true'
    notifications:
      enabled: true
      notify_on_recovery: true
      reminder_interval: 1h
  - id: web
    url: http://example.com/status
    body_match: null
    notifications:
      enabled: false
`
	cfg := parseForTest(t, document)

	if cfg.Storage.DatabasePath != "/var/lib/abouei/monitor.db" {
		t.Errorf("DatabasePath = %q", cfg.Storage.DatabasePath)
	}
	if cfg.Runtime.MaxConcurrentProbes != 4 || cfg.Runtime.ShutdownTimeout.String() != "45s" {
		t.Errorf("Runtime = %+v", cfg.Runtime)
	}
	if cfg.Logging.Level != "warn" || cfg.Logging.Format != "json" {
		t.Errorf("Logging = %+v, want normalized warn/json", cfg.Logging)
	}
	if cfg.Retention.Observations.String() != "48h0m0s" ||
		cfg.Retention.Incidents.String() != "720h0m0s" ||
		cfg.Retention.DeliveredEvents.String() != "24h0m0s" {
		t.Errorf("Retention = %+v", cfg.Retention)
	}
	if cfg.Notifications.Webhook.URL != "https://hooks.example.com/abouei" ||
		cfg.Notifications.Webhook.Timeout.String() != "15s" {
		t.Errorf("Webhook = %+v", cfg.Notifications.Webhook)
	}
	if len(cfg.Notifications.Webhook.Headers) != 2 {
		t.Fatalf("webhook headers = %+v, want two", cfg.Notifications.Webhook.Headers)
	}
	if cfg.Notifications.Webhook.Headers[0].Name != "Authorization" ||
		cfg.Notifications.Webhook.Headers[1].Name != "X-Monitor" {
		t.Errorf("webhook headers = %+v, want canonical sorted names", cfg.Notifications.Webhook.Headers)
	}
	if cfg.Notifications.Retry.MaxAttempts != 3 ||
		cfg.Notifications.Retry.InitialBackoff.String() != "2s" ||
		cfg.Notifications.Retry.MaxBackoff.String() != "1m0s" {
		t.Errorf("Retry = %+v", cfg.Notifications.Retry)
	}

	if len(cfg.Backends) != 2 {
		t.Fatalf("backends = %d, want 2", len(cfg.Backends))
	}
	api := cfg.Backends[0]
	if api.Name != "API" || api.Interval.String() != "10s" || api.Timeout.String() != "2s" {
		t.Errorf("api = %+v", api)
	}
	if api.FailureThreshold != 2 || api.RecoveryThreshold != 3 || api.MaxResponseBytes != 1024 || api.FollowRedirects {
		t.Errorf("api policy = %+v", api)
	}
	if got := fmt.Sprint(api.AcceptedStatusCodes); got != "[201]" {
		t.Errorf("api status codes = %s, want [201]", got)
	}
	if api.BodyMatch == nil || api.BodyMatch.Type != "regex" || api.BodyMatch.Regexp() == nil {
		t.Fatalf("api body matcher = %+v, want a compiled regex", api.BodyMatch)
	}
	if api.Notifications.Enabled != true ||
		api.Notifications.NotifyOnDown != true ||
		api.Notifications.NotifyOnRecovery != true ||
		api.Notifications.ReminderInterval.String() != "1h0m0s" ||
		api.Notifications.ReminderMax != 6 {
		t.Errorf("api notifications = %+v", api.Notifications)
	}
	if len(api.Headers) != 1 || api.Headers[0].Name != "X-Api-Key" || api.Headers[0].Value != "secret" {
		t.Errorf("api headers = %+v", api.Headers)
	}

	web := cfg.Backends[1]
	if web.Interval.String() != "30s" || web.Timeout.String() != "5s" {
		t.Errorf("web did not inherit interval/timeout: %+v", web)
	}
	if got := fmt.Sprint(web.AcceptedStatusCodes); got != "[200 204]" {
		t.Errorf("web status codes = %s, want inherited sorted [200 204]", got)
	}
	if web.BodyMatch != nil {
		t.Errorf("web BodyMatch = %+v, want cleared by null", web.BodyMatch)
	}
	if web.Notifications.Enabled != false ||
		web.Notifications.NotifyOnDown != true ||
		web.Notifications.NotifyOnRecovery != false ||
		web.Notifications.ReminderInterval.String() != "30m0s" ||
		web.Notifications.ReminderMax != 6 {
		t.Errorf("web notifications = %+v, want field-level merge", web.Notifications)
	}
	if !web.AcceptsStatus(204) || web.AcceptsStatus(201) {
		t.Error("web AcceptsStatus does not use the inherited explicit list")
	}
}

func TestParseVersionValidation(t *testing.T) {
	tests := []struct {
		name    string
		version string
		message string
	}{
		{name: "missing", version: "", message: "is required"},
		{name: "zero", version: "version: 0\n", message: "unsupported schema version 0"},
		{name: "two", version: "version: 2\n", message: "unsupported schema version 2"},
		{name: "negative", version: "version: -1\n", message: "unsupported schema version -1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := test.version + `backends:
  - id: api
    url: https://example.com/health
`
			requireFieldError(t, parseErrorForTest(t, document), "version", test.message)
		})
	}
}

func TestParseBackendsRequired(t *testing.T) {
	for name, document := range map[string]string{
		"missing": "version: 1\n",
		"empty":   "version: 1\nbackends: []\n",
		"null":    "version: 1\nbackends: null\n",
	} {
		t.Run(name, func(t *testing.T) {
			requireFieldError(t, parseErrorForTest(t, document), "backends", "at least one backend is required")
		})
	}
}

func TestParseBackendIdentityValidation(t *testing.T) {
	tests := []struct {
		name     string
		document string
		field    string
		message  string
	}{
		{
			name: "missing id",
			document: `version: 1
backends:
  - url: https://example.com/health
`,
			field:   "backends[0].id",
			message: "is required",
		},
		{
			name: "uppercase id",
			document: `version: 1
backends:
  - id: API
    url: https://example.com/health
`,
			field:   "backends[0].id",
			message: "must match",
		},
		{
			name: "leading dash id",
			document: `version: 1
backends:
  - id: -api
    url: https://example.com/health
`,
			field:   "backends[0].id",
			message: "must match",
		},
		{
			name:     "id too long",
			document: "version: 1\nbackends:\n  - id: " + strings.Repeat("a", 64) + "\n    url: https://example.com/health\n",
			field:    "backends[0].id",
			message:  "must match",
		},
		{
			name: "empty name",
			document: `version: 1
backends:
  - id: api
    name: ""
    url: https://example.com/health
`,
			field:   "backends[0].name",
			message: "must not be empty",
		},
		{
			name:     "name too long",
			document: "version: 1\nbackends:\n  - id: api\n    name: " + strings.Repeat("n", 129) + "\n    url: https://example.com/health\n",
			field:    "backends[0].name",
			message:  "at most 128",
		},
		{
			name: "missing url",
			document: `version: 1
backends:
  - id: api
`,
			field:   "backends[0].url",
			message: "is required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireFieldError(t, parseErrorForTest(t, test.document), test.field, test.message)
		})
	}
}

func TestParseDuplicateBackendIDs(t *testing.T) {
	document := `version: 1
backends:
  - id: api
    url: https://example.com/health
  - id: api
    url: https://example.com/status
`
	requireFieldError(t, parseErrorForTest(t, document), "backends[1].id", `duplicates backends[0].id "api"`)
}

func TestParseBackendLimit(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("version: 1\nbackends:\n")
	for i := 0; i < 101; i++ {
		fmt.Fprintf(&builder, "  - id: backend-%d\n    url: https://example.com/health\n", i)
	}
	requireFieldError(t, parseErrorForTest(t, builder.String()), "backends", "at most 100 backends")
}

func TestParseURLValidation(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		message string
	}{
		{name: "ftp scheme", url: "ftp://example.com/health", message: "http or https"},
		{name: "missing scheme", url: "example.com/health", message: "http or https"},
		{name: "missing host", url: "https:///health", message: "must include a host"},
		{name: "userinfo", url: "https://user:" + "pass@example.com/health", message: "must not include userinfo"},
		{name: "fragment", url: "https://example.com/health#section", message: "must not include a fragment"},
		{name: "whitespace", url: "\"https://example.com/health path\"", message: "whitespace"},
		{name: "too long", url: "https://example.com/" + strings.Repeat("a", maxURLBytes), message: "at most 2048 bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := "version: 1\nbackends:\n  - id: api\n    url: " + test.url + "\n"
			requireFieldError(t, parseErrorForTest(t, document), "backends[0].url", test.message)
		})
	}
}

func TestParseHeaderValidation(t *testing.T) {
	tests := []struct {
		name    string
		headers string
		field   string
		message string
	}{
		{
			name:    "invalid name",
			headers: "      \"Bad Header\": value\n",
			field:   "backends[0].headers.Bad Header",
			message: "not a valid HTTP header name",
		},
		{
			name:    "reserved host",
			headers: "      Host: example.com\n",
			field:   "backends[0].headers.Host",
			message: "reserved",
		},
		{
			name:    "reserved proxy authorization",
			headers: "      Proxy-Authorization: secret\n",
			field:   "backends[0].headers.Proxy-Authorization",
			message: "reserved",
		},
		{
			name:    "crlf injection",
			headers: "      X-Test: \"one\\r\\ntwo\"\n",
			field:   "backends[0].headers.X-Test",
			message: "CR, LF, or NUL",
		},
		{
			name:    "value too large",
			headers: "      X-Test: " + strings.Repeat("x", maxHeaderValueBytes+1) + "\n",
			field:   "backends[0].headers.X-Test",
			message: "8 KiB",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := "version: 1\nbackends:\n  - id: api\n    url: https://example.com/health\n    headers:\n" + test.headers
			requireFieldError(t, parseErrorForTest(t, document), test.field, test.message)
		})
	}
}

func TestParseHeaderLimitsAndCanonicalization(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("version: 1\nbackends:\n  - id: api\n    url: https://example.com/health\n    headers:\n")
	for i := 0; i < 51; i++ {
		fmt.Fprintf(&builder, "      X-Custom-%d: value\n", i)
	}
	requireFieldError(t, parseErrorForTest(t, builder.String()), "backends[0].headers", "at most 50 headers")

	duplicateDocument := `version: 1
backends:
  - id: api
    url: https://example.com/health
    headers:
      Authorization: one
      authorization: two
`
	requireFieldError(t, parseErrorForTest(t, duplicateDocument), "backends[0].headers.Authorization", "case-insensitive")

	largeTotalDocument := `version: 1
backends:
  - id: api
    url: https://example.com/health
    headers:
      X-One: ` + strings.Repeat("a", 6000) + `
      X-Two: ` + strings.Repeat("b", 6000) + `
      X-Three: ` + strings.Repeat("c", 6000) + `
`
	requireFieldError(t, parseErrorForTest(t, largeTotalDocument), "backends[0].headers", "16 KiB")
}

func TestParsePolicyRanges(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   string
		message string
	}{
		{name: "interval zero", field: "interval", value: "0s", message: "between 1s and 24h"},
		{name: "interval too small", field: "interval", value: "999ms", message: "between 1s and 24h"},
		{name: "interval too large", field: "interval", value: "25h", message: "between 1s and 24h"},
		{name: "timeout zero", field: "timeout", value: "0s", message: "between 100ms and 5m"},
		{name: "timeout too small", field: "timeout", value: "99ms", message: "between 100ms and 5m"},
		{name: "timeout too large", field: "timeout", value: "6m", message: "between 100ms and 5m"},
		{name: "failure threshold zero", field: "failure_threshold", value: "0", message: "between 1 and 100"},
		{name: "failure threshold too large", field: "failure_threshold", value: "101", message: "between 1 and 100"},
		{name: "recovery threshold zero", field: "recovery_threshold", value: "0", message: "between 1 and 100"},
		{name: "recovery threshold too large", field: "recovery_threshold", value: "101", message: "between 1 and 100"},
		{name: "response bytes zero", field: "max_response_bytes", value: "0", message: "between 1 and 16777216"},
		{name: "response bytes too large", field: "max_response_bytes", value: "16777217", message: "between 1 and 16777216"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := fmt.Sprintf(`version: 1
backends:
  - id: api
    url: https://example.com/health
    %s: %s
`, test.field, test.value)
			requireFieldError(t, parseErrorForTest(t, document), "backends[0]."+test.field, test.message)
		})
	}
}

func TestParsePolicyBoundariesAreAccepted(t *testing.T) {
	document := `version: 1
defaults:
  interval: 1s
  timeout: 100ms
  failure_threshold: 1
  recovery_threshold: 100
  max_response_bytes: 16777216
backends:
  - id: api
    url: https://example.com/health
    interval: 24h
    timeout: 5m
    failure_threshold: 100
    recovery_threshold: 1
    max_response_bytes: 1
`
	cfg := parseForTest(t, document)
	if cfg.Backends[0].Interval != 24*time.Hour || cfg.Backends[0].Timeout != 5*time.Minute {
		t.Errorf("backend policy = %+v", cfg.Backends[0])
	}
}

func TestParseTimeoutMustBeLessThanInterval(t *testing.T) {
	document := `version: 1
defaults:
  interval: 10s
  timeout: 5s
backends:
  - id: inherits-too-long-timeout
    url: https://example.com/health
    interval: 5s
`
	requireFieldError(
		t,
		parseErrorForTest(t, document),
		"backends[0].timeout",
		"effective timeout 5s must be less than interval 5s",
	)
}

func TestParseStatusCodes(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		message string
	}{
		{name: "empty list", value: "[]", message: "must not be empty"},
		{name: "below range", value: "[99]", message: "outside 100..599"},
		{name: "above range", value: "[600]", message: "outside 100..599"},
		{name: "duplicate", value: "[200, 200]", message: "unique status codes"},
		{name: "zero", value: "[0]", message: "outside 100..599"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := fmt.Sprintf(`version: 1
backends:
  - id: api
    url: https://example.com/health
    accepted_status_codes: %s
`, test.value)
			requireFieldError(t, parseErrorForTest(t, document), "backends[0].accepted_status_codes", test.message)
		})
	}
}

func TestParseStatusCodesAreSortedAndExplicit(t *testing.T) {
	document := `version: 1
backends:
  - id: api
    url: https://example.com/health
    accepted_status_codes: [404, 200, 301]
`
	cfg := parseForTest(t, document)
	backend := cfg.Backends[0]
	if got := fmt.Sprint(backend.AcceptedStatusCodes); got != "[200 301 404]" {
		t.Errorf("status codes = %s, want sorted", got)
	}
	if backend.AcceptsStatus(201) {
		t.Error("explicit status list accepted an unlisted code")
	}
	if !backend.AcceptsStatus(301) {
		t.Error("explicit status list rejected a listed code")
	}
}

func TestParseBodyMatchValidation(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		field   string
		message string
	}{
		{name: "missing type", body: "      value: ok\n", field: "backends[0].body_match.type", message: "is required"},
		{name: "missing value", body: "      type: contains\n", field: "backends[0].body_match.value", message: "is required"},
		{name: "empty value", body: "      type: contains\n      value: \"\"\n", field: "backends[0].body_match.value", message: "must not be empty"},
		{name: "invalid type", body: "      type: jsonpath\n      value: ok\n", field: "backends[0].body_match.type", message: "contains"},
		{name: "invalid regex", body: "      type: regex\n      value: \"[unclosed\"\n", field: "backends[0].body_match.value", message: "RE2"},
		{name: "unknown field", body: "      type: contains\n      value: ok\n      case_sensitive: true\n", field: "backends[0].body_match.case_sensitive", message: "not a recognized field"},
		{name: "duplicate field", body: "      type: contains\n      value: ok\n      value: other\n", field: "backends[0].body_match.value", message: "duplicate key"},
		{name: "non-mapping", body: "    body_match: contains\n", field: "backends[0].body_match", message: "must be a mapping"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var document string
			if test.name == "non-mapping" {
				document = "version: 1\nbackends:\n  - id: api\n    url: https://example.com/health\n" + test.body
			} else {
				document = "version: 1\nbackends:\n  - id: api\n    url: https://example.com/health\n    body_match:\n" + test.body
			}
			requireFieldError(t, parseErrorForTest(t, document), test.field, test.message)
		})
	}
}

func TestParseBodyMatchValid(t *testing.T) {
	cfg := parseForTest(t, `version: 1
backends:
  - id: contains
    url: https://example.com/health
    body_match:
      type: contains
      value: ready
  - id: regex
    url: https://example.com/health
    body_match:
      type: regex
      value: '"status"\s*:\s*"ok"'
`)
	if cfg.Backends[0].BodyMatch == nil || cfg.Backends[0].BodyMatch.Regexp() != nil {
		t.Errorf("contains matcher = %+v, want no compiled regex", cfg.Backends[0].BodyMatch)
	}
	if cfg.Backends[1].BodyMatch == nil || cfg.Backends[1].BodyMatch.Regexp() == nil {
		t.Errorf("regex matcher = %+v, want a compiled regex", cfg.Backends[1].BodyMatch)
	}
	var nilMatch *BodyMatch
	if nilMatch.Regexp() != nil {
		t.Error("nil BodyMatch.Regexp() != nil")
	}
}

func TestParseRegexErrorDoesNotEchoPattern(t *testing.T) {
	const pattern = "secret-token-["
	validation := parseErrorForTest(t, `version: 1
backends:
  - id: api
    url: https://example.com/health
    body_match:
      type: regex
      value: "`+pattern+`"
`)
	if strings.Contains(validation.Error(), pattern) {
		t.Errorf("error leaked the regular expression: %q", validation.Error())
	}
}

func TestParseRuntimeAndLoggingValidation(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		section string
		message string
	}{
		{name: "concurrency zero", field: "runtime.max_concurrent_probes", section: "runtime:\n  max_concurrent_probes: 0", message: "between 1 and 64"},
		{name: "concurrency high", field: "runtime.max_concurrent_probes", section: "runtime:\n  max_concurrent_probes: 65", message: "between 1 and 64"},
		{name: "shutdown zero", field: "runtime.shutdown_timeout", section: "runtime:\n  shutdown_timeout: 0s", message: "between 1s and 5m"},
		{name: "shutdown high", field: "runtime.shutdown_timeout", section: "runtime:\n  shutdown_timeout: 6m", message: "between 1s and 5m"},
		{name: "invalid level", field: "logging.level", section: "logging:\n  level: verbose", message: "debug"},
		{name: "invalid format", field: "logging.format", section: "logging:\n  format: yaml", message: "text"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := "version: 1\n" + test.section + "\nbackends:\n  - id: api\n    url: https://example.com/health\n"
			requireFieldError(t, parseErrorForTest(t, document), test.field, test.message)
		})
	}
}

func TestParseStorageValidation(t *testing.T) {
	document := `version: 1
storage:
  database_path: ":memory:"
backends:
  - id: api
    url: https://example.com/health
`
	requireFieldError(t, parseErrorForTest(t, document), "storage.database_path", ":memory:")

	empty := `version: 1
storage:
  database_path: ""
backends:
  - id: api
    url: https://example.com/health
`
	requireFieldError(t, parseErrorForTest(t, empty), "storage.database_path", "must not be empty")
}

func TestParseRelativeDatabasePathResolution(t *testing.T) {
	document := `version: 1
storage:
  database_path: ./data/abouei.db
backends:
  - id: api
    url: https://example.com/health
`
	cfg := parseForTest(t, document)
	if cfg.Storage.DatabasePath != "/etc/abouei/data/abouei.db" {
		t.Errorf("DatabasePath = %q, want resolved against /etc/abouei", cfg.Storage.DatabasePath)
	}

	noDir, err := Parse([]byte(document), ParseOptions{LookupEnv: emptyEnvironment})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if noDir.Storage.DatabasePath != "data/abouei.db" {
		t.Errorf("DatabasePath without a directory = %q, want cleaned relative path", noDir.Storage.DatabasePath)
	}

	tilde := strings.Replace(document, "./data/abouei.db", "~/data/abouei.db", 1)
	cfg = parseForTest(t, tilde)
	if cfg.Storage.DatabasePath != "/etc/abouei/~/data/abouei.db" {
		t.Errorf("DatabasePath = %q, want literal ~ not expanded", cfg.Storage.DatabasePath)
	}
}

func TestParseRetentionValidation(t *testing.T) {
	tests := []struct {
		field   string
		value   string
		message string
	}{
		{field: "retention.observations", value: "observations: 59m", message: "between 1h and 8760h"},
		{field: "retention.observations", value: "observations: 8761h", message: "between 1h and 8760h"},
		{field: "retention.incidents", value: "incidents: 23h", message: "between 24h and 87600h"},
		{field: "retention.incidents", value: "incidents: 87601h", message: "between 24h and 87600h"},
		{field: "retention.delivered_events", value: "delivered_events: 30m", message: "between 1h and 8760h"},
		{field: "retention.delivered_events", value: "delivered_events: 8761h", message: "between 1h and 8760h"},
	}
	for _, test := range tests {
		t.Run(test.field+" "+test.value, func(t *testing.T) {
			document := "version: 1\nretention:\n  " + test.value + "\nbackends:\n  - id: api\n    url: https://example.com/health\n"
			requireFieldError(t, parseErrorForTest(t, document), test.field, test.message)
		})
	}
}

func TestParseNotificationTransportValidation(t *testing.T) {
	tests := []struct {
		name    string
		section string
		field   string
		message string
	}{
		{
			name:    "invalid scheme",
			section: "    url: ftp://example.com/hook\n",
			field:   "notifications.webhook.url",
			message: "http or https",
		},
		{
			name:    "userinfo",
			section: "    url: https://user:" + "pass@example.com/hook\n",
			field:   "notifications.webhook.url",
			message: "userinfo",
		},
		{
			name:    "fragment",
			section: "    url: https://example.com/hook#x\n",
			field:   "notifications.webhook.url",
			message: "fragment",
		},
		{
			name:    "timeout too small",
			section: "    timeout: 50ms\n",
			field:   "notifications.webhook.timeout",
			message: "between 100ms and 2m",
		},
		{
			name:    "timeout too large",
			section: "    timeout: 3m\n",
			field:   "notifications.webhook.timeout",
			message: "between 100ms and 2m",
		},
		{
			name:    "content type reserved",
			section: "    headers:\n      Content-Type: application/json\n",
			field:   "notifications.webhook.headers.Content-Type",
			message: "reserved",
		},
		{
			name:    "idempotency key reserved",
			section: "    headers:\n      Idempotency-Key: fixed\n",
			field:   "notifications.webhook.headers.Idempotency-Key",
			message: "reserved",
		},
		{
			name:    "retry attempts zero",
			section: "  retry:\n    max_attempts: 0\n",
			field:   "notifications.retry.max_attempts",
			message: "between 1 and 20",
		},
		{
			name:    "retry attempts high",
			section: "  retry:\n    max_attempts: 21\n",
			field:   "notifications.retry.max_attempts",
			message: "between 1 and 20",
		},
		{
			name:    "initial backoff low",
			section: "  retry:\n    initial_backoff: 99ms\n",
			field:   "notifications.retry.initial_backoff",
			message: "between 100ms and 1h",
		},
		{
			name:    "max backoff high",
			section: "  retry:\n    max_backoff: 2h\n",
			field:   "notifications.retry.max_backoff",
			message: "between 100ms and 1h",
		},
		{
			name:    "max backoff below initial",
			section: "  retry:\n    initial_backoff: 10m\n    max_backoff: 1m\n",
			field:   "notifications.retry.max_backoff",
			message: "greater than or equal",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := "version: 1\nnotifications:\n  webhook:\n" + test.section +
				"backends:\n  - id: api\n    url: https://example.com/health\n"
			if strings.HasPrefix(test.section, "  retry:") {
				document = "version: 1\nnotifications:\n" + test.section +
					"backends:\n  - id: api\n    url: https://example.com/health\n"
			}
			requireFieldError(t, parseErrorForTest(t, document), test.field, test.message)
		})
	}
}

func TestParseNotificationPolicyValidation(t *testing.T) {
	tests := []struct {
		name    string
		policy  string
		field   string
		message string
	}{
		{
			name: "enabled but silent",
			policy: `      enabled: true
      notify_on_down: false
      notify_on_recovery: false
      reminder_interval: 0s
`,
			field:   "backends[0].notifications.enabled",
			message: "must enable notify_on_down, notify_on_recovery, or reminders",
		},
		{
			name: "reminder below minimum",
			policy: `      enabled: true
      reminder_interval: 30s
`,
			field:   "backends[0].notifications.reminder_interval",
			message: "0s to disable reminders or between 1m and 168h",
		},
		{
			name: "reminder above maximum",
			policy: `      enabled: true
      reminder_interval: 169h
`,
			field:   "backends[0].notifications.reminder_interval",
			message: "0s to disable reminders or between 1m and 168h",
		},
		{
			name: "reminder max negative",
			policy: `      reminder_max: -1
`,
			field:   "backends[0].notifications.reminder_max",
			message: "between 0 and 1000",
		},
		{
			name: "reminder max high",
			policy: `      reminder_max: 1001
`,
			field:   "backends[0].notifications.reminder_max",
			message: "between 0 and 1000",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := `version: 1
notifications:
  webhook:
    url: https://hooks.example.com/abouei
backends:
  - id: api
    url: https://example.com/health
    notifications:
` + test.policy
			requireFieldError(t, parseErrorForTest(t, document), test.field, test.message)
		})
	}
}

func TestParseNotificationsRequireWebhookURL(t *testing.T) {
	document := `version: 1
backends:
  - id: api
    url: https://example.com/health
    notifications:
      enabled: true
`
	requireFieldError(t, parseErrorForTest(t, document), "notifications.webhook.url", "is required")

	disabled := `version: 1
backends:
  - id: api
    url: https://example.com/health
    notifications:
      enabled: false
`
	parseForTest(t, disabled)
}

func TestParseNotificationPolicyMergeAcrossDefaults(t *testing.T) {
	document := `version: 1
notifications:
  webhook:
    url: https://hooks.example.com/abouei
defaults:
  notifications:
    enabled: true
    notify_on_down: false
    reminder_interval: 0s
backends:
  - id: api
    url: https://example.com/health
    notifications:
      notify_on_down: true
`
	cfg := parseForTest(t, document)
	policy := cfg.Backends[0].Notifications
	if !policy.Enabled || !policy.NotifyOnDown || !policy.NotifyOnRecovery {
		t.Errorf("policy = %+v, want merged enabled/down/recovery", policy)
	}
	if policy.ReminderInterval != 0 || policy.ReminderMax != 0 {
		t.Errorf("policy reminders = %s/%d, want disabled", policy.ReminderInterval, policy.ReminderMax)
	}
}

func TestParseDecodesEscapedWebhookHeaders(t *testing.T) {
	document := `version: 1
notifications:
  webhook:
    url: https://hooks.example.com/abouei
    headers:
      Authorization: "Bearer literal"
backends:
  - id: api
    url: https://example.com/health
`
	cfg := parseForTest(t, document)
	if len(cfg.Notifications.Webhook.Headers) != 1 {
		t.Fatalf("headers = %+v", cfg.Notifications.Webhook.Headers)
	}
	if cfg.Notifications.Webhook.Headers[0].Value != "Bearer literal" {
		t.Errorf("header value = %q, want the literal value", cfg.Notifications.Webhook.Headers[0].Value)
	}
}
