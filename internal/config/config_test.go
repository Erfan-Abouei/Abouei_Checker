package config

import (
	"errors"
	"strings"
	"testing"
)

const minimalConfig = `version: 1
backends:
  - id: api
    url: https://example.com/health
`

func emptyEnvironment(string) (string, bool) {
	return "", false
}

func parseForTest(t *testing.T, document string) *Config {
	t.Helper()

	cfg, err := Parse([]byte(document), ParseOptions{
		Dir:       "/etc/abouei",
		LookupEnv: emptyEnvironment,
		Source:    Source{Path: "/etc/abouei/config.yaml", Origin: OriginFlag},
	})
	if err != nil {
		t.Fatalf("Parse() error = %v\ndocument:\n%s", err, document)
	}
	return cfg
}

func parseErrorForTest(t *testing.T, document string) *ValidationError {
	t.Helper()

	cfg, err := Parse([]byte(document), ParseOptions{
		Dir:       "/etc/abouei",
		LookupEnv: emptyEnvironment,
	})
	if err == nil {
		t.Fatalf("Parse() error = nil, want a validation error; config = %+v", cfg)
	}

	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("Parse() error type = %T, want *ValidationError; error = %v", err, err)
	}
	return validation
}

func errorStrings(validation *ValidationError) []string {
	messages := make([]string, len(validation.Errors))
	for i := range validation.Errors {
		messages[i] = validation.Errors[i].Error()
	}
	return messages
}

func requireFieldError(t *testing.T, validation *ValidationError, field, message string) {
	t.Helper()

	for _, fieldError := range validation.Errors {
		if fieldError.Field == field && strings.Contains(fieldError.Message, message) {
			return
		}
	}
	t.Fatalf("missing error field=%q message containing %q; got:\n%s",
		field, message, strings.Join(errorStrings(validation), "\n"))
}

func TestParseMinimalConfigurationAppliesDefaults(t *testing.T) {
	cfg := parseForTest(t, minimalConfig)

	if cfg.Version != 1 {
		t.Errorf("Version = %d, want 1", cfg.Version)
	}
	if cfg.Storage.DatabasePath != "/etc/abouei/data/abouei.db" {
		t.Errorf("DatabasePath = %q, want the default resolved against the config directory", cfg.Storage.DatabasePath)
	}
	if cfg.Runtime.MaxConcurrentProbes != 8 {
		t.Errorf("MaxConcurrentProbes = %d, want 8", cfg.Runtime.MaxConcurrentProbes)
	}
	if cfg.Runtime.ShutdownTimeout.String() != "30s" {
		t.Errorf("ShutdownTimeout = %s, want 30s", cfg.Runtime.ShutdownTimeout)
	}
	if cfg.Logging.Level != "info" || cfg.Logging.Format != "text" {
		t.Errorf("Logging = %+v, want info/text", cfg.Logging)
	}
	if cfg.Retention.Observations.String() != "720h0m0s" {
		t.Errorf("Observations retention = %s, want 720h", cfg.Retention.Observations)
	}
	if cfg.Retention.Incidents.String() != "4320h0m0s" {
		t.Errorf("Incidents retention = %s, want 4320h", cfg.Retention.Incidents)
	}
	if cfg.Retention.DeliveredEvents.String() != "168h0m0s" {
		t.Errorf("DeliveredEvents retention = %s, want 168h", cfg.Retention.DeliveredEvents)
	}
	if cfg.Notifications.Webhook.URL != "" {
		t.Errorf("Webhook.URL = %q, want empty", cfg.Notifications.Webhook.URL)
	}
	if cfg.Notifications.Webhook.Timeout.String() != "10s" {
		t.Errorf("Webhook.Timeout = %s, want 10s", cfg.Notifications.Webhook.Timeout)
	}
	if cfg.Notifications.Retry.MaxAttempts != 5 ||
		cfg.Notifications.Retry.InitialBackoff.String() != "5s" ||
		cfg.Notifications.Retry.MaxBackoff.String() != "5m0s" {
		t.Errorf("Retry = %+v, want 5 attempts, 5s..5m", cfg.Notifications.Retry)
	}

	if len(cfg.Backends) != 1 {
		t.Fatalf("Backends = %d, want 1", len(cfg.Backends))
	}
	backend := cfg.Backends[0]
	if backend.ID != "api" || backend.Name != "api" {
		t.Errorf("backend identity = %q/%q, want api/api", backend.ID, backend.Name)
	}
	if backend.Interval.String() != "1m0s" || backend.Timeout.String() != "10s" {
		t.Errorf("interval/timeout = %s/%s, want 60s/10s", backend.Interval, backend.Timeout)
	}
	if backend.FailureThreshold != 3 || backend.RecoveryThreshold != 1 {
		t.Errorf("thresholds = %d/%d, want 3/1", backend.FailureThreshold, backend.RecoveryThreshold)
	}
	if backend.MaxResponseBytes != 1048576 {
		t.Errorf("MaxResponseBytes = %d, want 1048576", backend.MaxResponseBytes)
	}
	if backend.FollowRedirects {
		t.Error("FollowRedirects = true, want false")
	}
	if backend.AcceptedStatusCodes != nil {
		t.Errorf("AcceptedStatusCodes = %v, want nil (2xx default)", backend.AcceptedStatusCodes)
	}
	if !backend.AcceptsStatus(200) || !backend.AcceptsStatus(299) || backend.AcceptsStatus(300) {
		t.Error("AcceptsStatus does not implement the 2xx default")
	}
	if backend.BodyMatch != nil {
		t.Errorf("BodyMatch = %+v, want nil", backend.BodyMatch)
	}
	if backend.Notifications.Enabled ||
		!backend.Notifications.NotifyOnDown ||
		!backend.Notifications.NotifyOnRecovery ||
		backend.Notifications.ReminderInterval != 0 ||
		backend.Notifications.ReminderMax != 0 {
		t.Errorf("Notifications policy = %+v, want documented defaults", backend.Notifications)
	}
	if cfg.Source.Path != "/etc/abouei/config.yaml" || cfg.Source.Origin != OriginFlag {
		t.Errorf("Source = %+v, want the parse source", cfg.Source)
	}
	if got := cfg.BackendIDs(); len(got) != 1 || got[0] != "api" {
		t.Errorf("BackendIDs() = %v, want [api]", got)
	}
}

func TestTemplateIsValid(t *testing.T) {
	cfg, err := Parse(Template(), ParseOptions{LookupEnv: emptyEnvironment})
	if err != nil {
		t.Fatalf("Parse(Template()) error = %v", err)
	}
	if cfg.Version != 1 || len(cfg.Backends) != 1 {
		t.Errorf("template config = version %d, %d backends", cfg.Version, len(cfg.Backends))
	}
}
