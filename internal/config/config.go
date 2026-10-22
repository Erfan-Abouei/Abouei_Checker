// Package config discovers, reads, expands, validates, and normalizes
// AboueiChecker YAML configuration.
//
// Loading is a pure operation: it performs no network access, opens no
// database, and delivers no notifications. A Config returned by Load or Parse
// is fully validated and has all documented defaults and per-backend
// inheritance applied. Partial or unvalidated configuration is never returned.
package config

import (
	"regexp"
	"time"
)

// DefaultPath is the configuration filename searched in the working directory
// when no explicit path is supplied. init also uses it as the default target.
const DefaultPath = "abouei.yaml"

// SchemaVersion is the only configuration schema version accepted by this
// binary. Unsupported versions fail closed.
const SchemaVersion = 1

// MaxFileBytes is the maximum accepted configuration file size: 1 MiB.
const MaxFileBytes = 1 << 20

// SecretRegistrar receives resolved secret values so an output boundary can
// redact them. logging.Redactor implements this interface.
type SecretRegistrar interface {
	AddSecrets(values ...string)
}

// Config is a validated, normalized configuration. It is read-only after
// construction; callers must not mutate its fields or slices.
type Config struct {
	// Version is the validated configuration schema version (always 1).
	Version int
	// Storage contains resolved persistence settings.
	Storage Storage
	// Runtime contains process-wide limits.
	Runtime Runtime
	// Logging contains validated log level and format names.
	Logging Logging
	// Notifications contains the normalized webhook transport settings.
	Notifications Notifications
	// Retention contains data-retention windows.
	Retention Retention
	// Backends contains every backend with its effective monitoring policy.
	Backends []Backend
	// Source describes where the configuration was loaded from.
	Source Source

	secrets []string
}

// Storage contains persistence settings. The database is not opened by this
// package.
type Storage struct {
	// DatabasePath is the cleaned database path. Relative paths are resolved
	// against the directory containing the configuration file.
	DatabasePath string
}

// Runtime contains process-wide runtime limits.
type Runtime struct {
	// MaxConcurrentProbes bounds concurrent probes (default 8, range 1..64).
	MaxConcurrentProbes int
	// ShutdownTimeout bounds graceful shutdown (default 30s, range 1s..5m).
	ShutdownTimeout time.Duration
}

// Logging contains validated logging settings. Values are lowercase.
type Logging struct {
	// Level is one of debug, info, warn, or error. Default: info.
	Level string
	// Format is either text or json. Default: text.
	Format string
}

// Notifications contains the global webhook transport. Per-backend policy is
// stored on each Backend.
type Notifications struct {
	Webhook Webhook
	Retry   Retry
}

// Webhook describes the notification transport endpoint.
type Webhook struct {
	// URL is the validated webhook URL, or empty when none was configured.
	URL string
	// Timeout is the per-attempt delivery timeout.
	Timeout time.Duration
	// Headers are validated custom headers sorted by canonical name. Values are
	// sensitive and must never be logged or serialized.
	Headers []Header
}

// Retry describes webhook delivery retry settings.
type Retry struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// Retention describes how long terminal data is kept.
type Retention struct {
	Observations    time.Duration
	Incidents       time.Duration
	DeliveredEvents time.Duration
}

// Backend is a validated backend definition with its effective monitoring
// policy. Inheritance from defaults has already been applied.
type Backend struct {
	// ID is the stable, unique backend identity.
	ID string
	// Name is the display name. It defaults to ID.
	Name string
	// URL is the validated health endpoint URL.
	URL string
	// Headers are validated custom request headers sorted by canonical name.
	// Values are sensitive and must never be logged or serialized.
	Headers []Header

	// Interval is the effective probe interval (default 60s, range 1s..24h).
	Interval time.Duration
	// Timeout is the effective per-probe timeout and is always less than
	// Interval.
	Timeout time.Duration
	// FailureThreshold is the consecutive failure count that opens an
	// incident (default 3, range 1..100).
	FailureThreshold int
	// RecoveryThreshold is the consecutive success count that closes an
	// incident (default 1, range 1..100).
	RecoveryThreshold int
	// MaxResponseBytes bounds response-body reads (default 1 MiB, maximum
	// 16 MiB).
	MaxResponseBytes int
	// FollowRedirects controls whether redirects are followed. It defaults to
	// false and is enforced by the Phase 3+ probe engine.
	FollowRedirects bool
	// AcceptedStatusCodes is the explicit ascending, unique status-code list.
	// Nil means the documented default: every code in 200..299.
	AcceptedStatusCodes []int
	// BodyMatch is the optional response-body matcher. Nil means no matcher.
	BodyMatch *BodyMatch
	// Notifications is the effective notification policy for this backend.
	Notifications Policy
}

// AcceptsStatus reports whether code satisfies the effective status policy.
func (b Backend) AcceptsStatus(code int) bool {
	if b.AcceptedStatusCodes == nil {
		return code >= 200 && code <= 299
	}
	for _, accepted := range b.AcceptedStatusCodes {
		if accepted == code {
			return true
		}
	}
	return false
}

// BodyMatch is a validated response-body condition.
//
// Type is contains or regex. For regex, the expression has already been
// compiled with Go's RE2 implementation and is available through Regexp.
type BodyMatch struct {
	Type  string
	Value string

	compiled *regexp.Regexp
}

// Regexp returns the compiled expression for a regex matcher. It returns nil
// for contains matchers and for a nil BodyMatch.
func (m *BodyMatch) Regexp() *regexp.Regexp {
	if m == nil {
		return nil
	}
	return m.compiled
}

// Policy is the effective notification policy for one backend.
type Policy struct {
	// Enabled turns notifications on for this backend.
	Enabled bool
	// NotifyOnDown emits an alert when an incident opens.
	NotifyOnDown bool
	// NotifyOnRecovery emits an alert when an incident closes.
	NotifyOnRecovery bool
	// ReminderInterval is the reminder cadence. Zero disables reminders.
	ReminderInterval time.Duration
	// ReminderMax caps reminders per incident. Zero means unlimited.
	ReminderMax int
}

// Header is one validated custom HTTP header. Values are sensitive.
type Header struct {
	Name  string
	Value string
}

// Source describes the configuration file that was loaded.
type Source struct {
	// Path is the path used to open the file. It may be relative to the working
	// directory.
	Path string
	// Origin identifies which discovery rule selected the file.
	Origin Origin
}

// Origin identifies a configuration discovery source.
type Origin string

const (
	// OriginFlag means the path came from --config.
	OriginFlag Origin = "flag"
	// OriginEnvironment means the path came from ABOUEI_CONFIG.
	OriginEnvironment Origin = "environment"
	// OriginWorkingDirectory means ./abouei.yaml was used.
	OriginWorkingDirectory Origin = "working-directory"
	// OriginUserConfigDir means the platform user configuration directory was
	// used.
	OriginUserConfigDir Origin = "user-config-dir"
)

// BackendIDs returns backend IDs in configuration order.
func (c *Config) BackendIDs() []string {
	if c == nil {
		return nil
	}
	ids := make([]string, len(c.Backends))
	for i := range c.Backends {
		ids[i] = c.Backends[i].ID
	}
	return ids
}

// RegisterSecrets registers every value resolved from an environment
// reference so that log, error, and diagnostic output can redact it.
func (c *Config) RegisterSecrets(registrar SecretRegistrar) {
	if c == nil || registrar == nil || len(c.secrets) == 0 {
		return
	}
	registrar.AddSecrets(c.secrets...)
}
