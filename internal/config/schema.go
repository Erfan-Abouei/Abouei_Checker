package config

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

// The raw* types mirror the YAML schema. Pointer fields distinguish an absent
// value from an explicit zero so that inheritance can be applied exactly once.
// They are internal input values and are never exposed to runtime consumers.

type rawConfig struct {
	Version       *int              `yaml:"version"`
	Storage       *rawStorage       `yaml:"storage"`
	Runtime       *rawRuntime       `yaml:"runtime"`
	Logging       *rawLogging       `yaml:"logging"`
	Defaults      *rawPolicy        `yaml:"defaults"`
	Notifications *rawNotifications `yaml:"notifications"`
	Retention     *rawRetention     `yaml:"retention"`
	Backends      []rawBackend      `yaml:"backends"`
}

type rawStorage struct {
	DatabasePath *string `yaml:"database_path"`
}

type rawRuntime struct {
	MaxConcurrentProbes *int         `yaml:"max_concurrent_probes"`
	ShutdownTimeout     *rawDuration `yaml:"shutdown_timeout"`
}

type rawLogging struct {
	Level  *string `yaml:"level"`
	Format *string `yaml:"format"`
}

// rawDuration accepts a YAML string scalar and defers parsing until after
// environment expansion. This lets a duration such as "${INTERVAL}" resolve
// like any other string value.
type rawDuration struct {
	text string
}

func (d *rawDuration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return fmt.Errorf("line %d: must be a duration string such as 30s, 5m, or 24h", node.Line)
	}
	d.text = node.Value
	return nil
}

type rawPolicy struct {
	Interval            *rawDuration            `yaml:"interval"`
	Timeout             *rawDuration            `yaml:"timeout"`
	FailureThreshold    *int                    `yaml:"failure_threshold"`
	RecoveryThreshold   *int                    `yaml:"recovery_threshold"`
	MaxResponseBytes    *int                    `yaml:"max_response_bytes"`
	FollowRedirects     *bool                   `yaml:"follow_redirects"`
	AcceptedStatusCodes *[]int                  `yaml:"accepted_status_codes"`
	BodyMatch           yaml.Node               `yaml:"body_match"`
	Notifications       *rawPolicyNotifications `yaml:"notifications"`
}

type rawPolicyNotifications struct {
	Enabled          *bool        `yaml:"enabled"`
	NotifyOnDown     *bool        `yaml:"notify_on_down"`
	NotifyOnRecovery *bool        `yaml:"notify_on_recovery"`
	ReminderInterval *rawDuration `yaml:"reminder_interval"`
	ReminderMax      *int         `yaml:"reminder_max"`
}

type rawBackend struct {
	ID      *string           `yaml:"id"`
	Name    *string           `yaml:"name"`
	URL     *string           `yaml:"url"`
	Headers map[string]string `yaml:"headers"`
	Policy  rawPolicy         `yaml:",inline"`
}

type rawNotifications struct {
	Webhook *rawWebhook `yaml:"webhook"`
	Retry   *rawRetry   `yaml:"retry"`
}

type rawWebhook struct {
	URL     *string           `yaml:"url"`
	Timeout *rawDuration      `yaml:"timeout"`
	Headers map[string]string `yaml:"headers"`
}

type rawRetry struct {
	MaxAttempts    *int         `yaml:"max_attempts"`
	InitialBackoff *rawDuration `yaml:"initial_backoff"`
	MaxBackoff     *rawDuration `yaml:"max_backoff"`
}

type rawRetention struct {
	Observations    *rawDuration `yaml:"observations"`
	Incidents       *rawDuration `yaml:"incidents"`
	DeliveredEvents *rawDuration `yaml:"delivered_events"`
}
