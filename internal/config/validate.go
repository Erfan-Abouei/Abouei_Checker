package config

import (
	"errors"
	"fmt"
	"net/textproto"
	"net/url"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.yaml.in/yaml/v3"
)

const (
	defaultDatabasePath          = "./data/abouei.db"
	defaultMaxConcurrentProbes   = 8
	defaultShutdownTimeout       = 30 * time.Second
	defaultLogLevel              = "info"
	defaultLogFormat             = "text"
	defaultInterval              = time.Minute
	defaultTimeout               = 10 * time.Second
	defaultFailureThreshold      = 3
	defaultRecoveryThreshold     = 1
	defaultMaxResponseBytes      = 1 << 20
	defaultWebhookTimeout        = 10 * time.Second
	defaultRetryMaxAttempts      = 5
	defaultInitialBackoff        = 5 * time.Second
	defaultMaxBackoff            = 5 * time.Minute
	defaultObservationsRetain    = 720 * time.Hour
	defaultIncidentsRetain       = 4320 * time.Hour
	defaultDeliveredEventsRetain = 168 * time.Hour

	maxBackends         = 100
	maxHeaders          = 50
	maxHeaderValueBytes = 8 << 10
	maxHeaderTotalBytes = 16 << 10
	maxURLBytes         = 2048
	maxNameBytes        = 128

	minInterval        = time.Second
	maxInterval        = 24 * time.Hour
	minTimeout         = 100 * time.Millisecond
	maxTimeout         = 5 * time.Minute
	minShutdownTimeout = time.Second
	maxShutdownTimeout = 5 * time.Minute
	minWebhookTimeout  = 100 * time.Millisecond
	maxWebhookTimeout  = 2 * time.Minute
	minBackoff         = 100 * time.Millisecond
	maxBackoff         = time.Hour
	minReminder        = time.Minute
	maxReminder        = 168 * time.Hour

	minObservationsRetain    = time.Hour
	maxObservationsRetain    = 8760 * time.Hour
	minIncidentsRetain       = 24 * time.Hour
	maxIncidentsRetain       = 87600 * time.Hour
	minDeliveredEventsRetain = time.Hour
	maxDeliveredEventsRetain = 8760 * time.Hour
)

var (
	backendIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	headerNamePattern = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")
)

var reservedBackendHeaders = map[string]struct{}{
	"host":                {},
	"content-length":      {},
	"transfer-encoding":   {},
	"connection":          {},
	"proxy-authorization": {},
	"keep-alive":          {},
	"te":                  {},
	"trailer":             {},
	"upgrade":             {},
}

var reservedWebhookHeaders = map[string]struct{}{
	"host":              {},
	"content-length":    {},
	"transfer-encoding": {},
	"connection":        {},
	"content-type":      {},
	"idempotency-key":   {},
}

type validator struct {
	opts ParseOptions
	errs []FieldError
}

type bodyMatchSpec struct {
	kind     string
	value    string
	compiled *regexp.Regexp
}

type notificationPolicyValues struct {
	enabled          bool
	notifyOnDown     bool
	notifyOnRecovery bool
	reminderInterval time.Duration
	reminderMax      int
}

type policyValues struct {
	interval            time.Duration
	intervalOK          bool
	timeout             time.Duration
	timeoutOK           bool
	failureThreshold    int
	recoveryThreshold   int
	maxResponseBytes    int
	followRedirects     bool
	acceptedStatusCodes []int
	bodyMatch           *bodyMatchSpec
	notifications       notificationPolicyValues
	notificationsOK     bool
}

func defaultPolicyValues() policyValues {
	return policyValues{
		interval:            defaultInterval,
		intervalOK:          true,
		timeout:             defaultTimeout,
		timeoutOK:           true,
		failureThreshold:    defaultFailureThreshold,
		recoveryThreshold:   defaultRecoveryThreshold,
		maxResponseBytes:    defaultMaxResponseBytes,
		acceptedStatusCodes: nil,
		notifications: notificationPolicyValues{
			enabled:          false,
			notifyOnDown:     true,
			notifyOnRecovery: true,
			reminderInterval: 0,
			reminderMax:      0,
		},
		notificationsOK: true,
	}
}

func (v *validator) build(raw *rawConfig) *Config {
	cfg := &Config{
		Storage: Storage{
			DatabasePath: resolveDatabasePath(v.opts.Dir, defaultDatabasePath),
		},
		Runtime: Runtime{
			MaxConcurrentProbes: defaultMaxConcurrentProbes,
			ShutdownTimeout:     defaultShutdownTimeout,
		},
		Logging: Logging{
			Level:  defaultLogLevel,
			Format: defaultLogFormat,
		},
		Notifications: Notifications{
			Webhook: Webhook{Timeout: defaultWebhookTimeout},
			Retry: Retry{
				MaxAttempts:    defaultRetryMaxAttempts,
				InitialBackoff: defaultInitialBackoff,
				MaxBackoff:     defaultMaxBackoff,
			},
		},
		Retention: Retention{
			Observations:    defaultObservationsRetain,
			Incidents:       defaultIncidentsRetain,
			DeliveredEvents: defaultDeliveredEventsRetain,
		},
	}

	v.validateVersion(raw, cfg)
	v.validateStorage(raw, cfg)
	v.validateRuntime(raw, cfg)
	v.validateLogging(raw, cfg)
	v.validateRetention(raw, cfg)
	cfg.Notifications = v.validateNotifications(raw.Notifications)

	defaults := v.applyPolicy(defaultPolicyValues(), raw.Defaults, "defaults")
	anyNotificationsEnabled := false
	seenIDs := make(map[string]int, len(raw.Backends))

	if len(raw.Backends) == 0 {
		v.add("backends", "at least one backend is required")
	}
	if len(raw.Backends) > maxBackends {
		v.add("backends", "at most %d backends are supported", maxBackends)
	}

	for i := range raw.Backends {
		backend := v.validateBackend(i, &raw.Backends[i], defaults)
		if backend.ID != "" {
			if previous, duplicate := seenIDs[backend.ID]; duplicate {
				v.add(fmt.Sprintf("backends[%d].id", i), "duplicates backends[%d].id %q", previous, backend.ID)
			} else {
				seenIDs[backend.ID] = i
			}
		}
		cfg.Backends = append(cfg.Backends, backend)
		if backend.Notifications.Enabled {
			anyNotificationsEnabled = true
		}
	}

	if anyNotificationsEnabled && cfg.Notifications.Webhook.URL == "" {
		v.add("notifications.webhook.url", "is required when a backend notification policy is enabled")
	}
	return cfg
}

func (v *validator) validateVersion(raw *rawConfig, cfg *Config) {
	switch {
	case raw.Version == nil:
		v.add("version", "is required")
	case *raw.Version != SchemaVersion:
		v.add("version", "unsupported schema version %d (supported: %d)", *raw.Version, SchemaVersion)
	default:
		cfg.Version = *raw.Version
	}
}

func (v *validator) validateStorage(raw *rawConfig, cfg *Config) {
	if raw.Storage == nil || raw.Storage.DatabasePath == nil {
		return
	}

	path := *raw.Storage.DatabasePath
	switch {
	case path == "":
		v.add("storage.database_path", "must not be empty")
	case strings.ContainsRune(path, 0):
		v.add("storage.database_path", "must not contain NUL bytes")
	case path == ":memory:":
		v.add("storage.database_path", "must be a file path; :memory: is not accepted in user configuration")
	default:
		cfg.Storage.DatabasePath = resolveDatabasePath(v.opts.Dir, path)
	}
}

func (v *validator) validateRuntime(raw *rawConfig, cfg *Config) {
	if raw.Runtime == nil {
		return
	}
	if value := raw.Runtime.MaxConcurrentProbes; value != nil {
		if v.intRange("runtime.max_concurrent_probes", *value, 1, 64) {
			cfg.Runtime.MaxConcurrentProbes = *value
		}
	}
	if value := raw.Runtime.ShutdownTimeout; value != nil {
		if duration, ok := v.durationRange("runtime.shutdown_timeout", value, minShutdownTimeout, maxShutdownTimeout); ok {
			cfg.Runtime.ShutdownTimeout = duration
		}
	}
}

func (v *validator) validateLogging(raw *rawConfig, cfg *Config) {
	if raw.Logging == nil {
		return
	}
	if value := raw.Logging.Level; value != nil {
		switch normalized := strings.ToLower(*value); normalized {
		case "debug", "info", "warn", "error":
			cfg.Logging.Level = normalized
		default:
			v.add("logging.level", `must be "debug", "info", "warn", or "error"`)
		}
	}
	if value := raw.Logging.Format; value != nil {
		switch normalized := strings.ToLower(*value); normalized {
		case "text", "json":
			cfg.Logging.Format = normalized
		default:
			v.add("logging.format", `must be "text" or "json"`)
		}
	}
}

func (v *validator) validateRetention(raw *rawConfig, cfg *Config) {
	if raw.Retention == nil {
		return
	}
	if value := raw.Retention.Observations; value != nil {
		if duration, ok := v.durationRange("retention.observations", value, minObservationsRetain, maxObservationsRetain); ok {
			cfg.Retention.Observations = duration
		}
	}
	if value := raw.Retention.Incidents; value != nil {
		if duration, ok := v.durationRange("retention.incidents", value, minIncidentsRetain, maxIncidentsRetain); ok {
			cfg.Retention.Incidents = duration
		}
	}
	if value := raw.Retention.DeliveredEvents; value != nil {
		if duration, ok := v.durationRange("retention.delivered_events", value, minDeliveredEventsRetain, maxDeliveredEventsRetain); ok {
			cfg.Retention.DeliveredEvents = duration
		}
	}
}

func (v *validator) validateNotifications(raw *rawNotifications) Notifications {
	notifications := Notifications{
		Webhook: Webhook{Timeout: defaultWebhookTimeout},
		Retry: Retry{
			MaxAttempts:    defaultRetryMaxAttempts,
			InitialBackoff: defaultInitialBackoff,
			MaxBackoff:     defaultMaxBackoff,
		},
	}
	if raw == nil {
		return notifications
	}

	if raw.Webhook != nil {
		if raw.Webhook.URL != nil && *raw.Webhook.URL != "" {
			if validated, ok := v.url("notifications.webhook.url", *raw.Webhook.URL); ok {
				notifications.Webhook.URL = validated
			} else {
				notifications.Webhook.URL = *raw.Webhook.URL
			}
		}
		if raw.Webhook.Timeout != nil {
			if duration, ok := v.durationRange("notifications.webhook.timeout", raw.Webhook.Timeout, minWebhookTimeout, maxWebhookTimeout); ok {
				notifications.Webhook.Timeout = duration
			}
		}
		notifications.Webhook.Headers = v.headers(
			"notifications.webhook.headers",
			raw.Webhook.Headers,
			reservedWebhookHeaders,
		)
	}

	initialOK, maxOK := true, true
	if raw.Retry != nil {
		if value := raw.Retry.MaxAttempts; value != nil {
			if v.intRange("notifications.retry.max_attempts", *value, 1, 20) {
				notifications.Retry.MaxAttempts = *value
			}
		}
		if value := raw.Retry.InitialBackoff; value != nil {
			duration, ok := v.durationRange("notifications.retry.initial_backoff", value, minBackoff, maxBackoff)
			if ok {
				notifications.Retry.InitialBackoff = duration
			}
			initialOK = ok
		}
		if value := raw.Retry.MaxBackoff; value != nil {
			duration, ok := v.durationRange("notifications.retry.max_backoff", value, minBackoff, maxBackoff)
			if ok {
				notifications.Retry.MaxBackoff = duration
			}
			maxOK = ok
		}
	}
	if initialOK && maxOK && notifications.Retry.MaxBackoff < notifications.Retry.InitialBackoff {
		v.add(
			"notifications.retry.max_backoff",
			"must be greater than or equal to notifications.retry.initial_backoff",
		)
	}

	return notifications
}

func (v *validator) validateBackend(index int, raw *rawBackend, defaults policyValues) Backend {
	prefix := fmt.Sprintf("backends[%d]", index)
	var backend Backend

	switch {
	case raw.ID == nil || *raw.ID == "":
		v.add(prefix+".id", "is required")
	case !backendIDPattern.MatchString(*raw.ID):
		v.add(prefix+".id", "must match ^[a-z0-9][a-z0-9_-]{0,62}$")
	default:
		backend.ID = *raw.ID
	}

	switch {
	case raw.Name == nil:
		backend.Name = backend.ID
	case *raw.Name == "":
		v.add(prefix+".name", "must not be empty")
	case len(*raw.Name) > maxNameBytes:
		v.add(prefix+".name", "must be at most %d UTF-8 bytes", maxNameBytes)
	default:
		backend.Name = *raw.Name
	}

	switch {
	case raw.URL == nil || *raw.URL == "":
		v.add(prefix+".url", "is required")
	default:
		if validated, ok := v.url(prefix+".url", *raw.URL); ok {
			backend.URL = validated
		} else {
			backend.URL = *raw.URL
		}
	}

	backend.Headers = v.headers(prefix+".headers", raw.Headers, reservedBackendHeaders)

	policy := v.applyPolicy(defaults, &raw.Policy, prefix)
	backend.Interval = policy.interval
	backend.Timeout = policy.timeout
	backend.FailureThreshold = policy.failureThreshold
	backend.RecoveryThreshold = policy.recoveryThreshold
	backend.MaxResponseBytes = policy.maxResponseBytes
	backend.FollowRedirects = policy.followRedirects
	backend.AcceptedStatusCodes = policy.acceptedStatusCodes
	if policy.bodyMatch != nil {
		backend.BodyMatch = &BodyMatch{
			Type:     policy.bodyMatch.kind,
			Value:    policy.bodyMatch.value,
			compiled: policy.bodyMatch.compiled,
		}
	}
	backend.Notifications = Policy{
		Enabled:          policy.notifications.enabled,
		NotifyOnDown:     policy.notifications.notifyOnDown,
		NotifyOnRecovery: policy.notifications.notifyOnRecovery,
		ReminderInterval: policy.notifications.reminderInterval,
		ReminderMax:      policy.notifications.reminderMax,
	}

	if policy.intervalOK && policy.timeoutOK && policy.timeout >= policy.interval {
		v.add(
			prefix+".timeout",
			"effective timeout %s must be less than interval %s",
			humanDuration(policy.timeout),
			humanDuration(policy.interval),
		)
	}

	if policy.notificationsOK && policy.notifications.enabled &&
		!policy.notifications.notifyOnDown &&
		!policy.notifications.notifyOnRecovery &&
		policy.notifications.reminderInterval == 0 {
		v.add(
			prefix+".notifications.enabled",
			"must enable notify_on_down, notify_on_recovery, or reminders",
		)
	}

	return backend
}

func (v *validator) applyPolicy(base policyValues, raw *rawPolicy, prefix string) policyValues {
	if raw == nil {
		return base
	}

	if value := raw.Interval; value != nil {
		duration, ok := v.durationRange(prefix+".interval", value, minInterval, maxInterval)
		if ok {
			base.interval = duration
		}
		base.intervalOK = ok
	}
	if value := raw.Timeout; value != nil {
		duration, ok := v.durationRange(prefix+".timeout", value, minTimeout, maxTimeout)
		if ok {
			base.timeout = duration
		}
		base.timeoutOK = ok
	}
	if value := raw.FailureThreshold; value != nil {
		if v.intRange(prefix+".failure_threshold", *value, 1, 100) {
			base.failureThreshold = *value
		}
	}
	if value := raw.RecoveryThreshold; value != nil {
		if v.intRange(prefix+".recovery_threshold", *value, 1, 100) {
			base.recoveryThreshold = *value
		}
	}
	if value := raw.MaxResponseBytes; value != nil {
		if v.intRange(prefix+".max_response_bytes", *value, 1, 16<<20) {
			base.maxResponseBytes = *value
		}
	}
	if value := raw.FollowRedirects; value != nil {
		base.followRedirects = *value
	}
	if value := raw.AcceptedStatusCodes; value != nil {
		if codes, ok := v.statusCodes(prefix+".accepted_status_codes", *value); ok {
			base.acceptedStatusCodes = codes
		}
	}
	if bodyMatch, present := v.bodyMatch(prefix+".body_match", &raw.BodyMatch); present {
		base.bodyMatch = bodyMatch
	}
	if raw.Notifications != nil {
		base.notifications, base.notificationsOK = v.applyNotificationPolicy(
			base.notifications,
			base.notificationsOK,
			raw.Notifications,
			prefix+".notifications",
		)
	}

	return base
}

func (v *validator) applyNotificationPolicy(
	base notificationPolicyValues,
	baseOK bool,
	raw *rawPolicyNotifications,
	prefix string,
) (notificationPolicyValues, bool) {
	ok := baseOK

	if raw.Enabled != nil {
		base.enabled = *raw.Enabled
	}
	if raw.NotifyOnDown != nil {
		base.notifyOnDown = *raw.NotifyOnDown
	}
	if raw.NotifyOnRecovery != nil {
		base.notifyOnRecovery = *raw.NotifyOnRecovery
	}
	if raw.ReminderInterval != nil {
		duration, valid := v.reminderInterval(prefix+".reminder_interval", raw.ReminderInterval)
		if valid {
			base.reminderInterval = duration
		}
		ok = ok && valid
	}
	if raw.ReminderMax != nil {
		valid := v.intRange(prefix+".reminder_max", *raw.ReminderMax, 0, 1000)
		if valid {
			base.reminderMax = *raw.ReminderMax
		}
		ok = ok && valid
	}

	return base, ok
}

func (v *validator) durationRange(field string, raw *rawDuration, minimum, maximum time.Duration) (time.Duration, bool) {
	duration, err := time.ParseDuration(raw.text)
	if err != nil {
		v.add(field, "must be a valid duration such as 30s, 5m, or 24h")
		return 0, false
	}
	if duration < minimum || duration > maximum {
		v.add(field, "must be between %s and %s", humanDuration(minimum), humanDuration(maximum))
		return 0, false
	}
	return duration, true
}

func (v *validator) reminderInterval(field string, raw *rawDuration) (time.Duration, bool) {
	duration, err := time.ParseDuration(raw.text)
	if err != nil {
		v.add(field, "must be a valid duration such as 0s, 30m, or 24h")
		return 0, false
	}
	if duration == 0 {
		return 0, true
	}
	if duration < minReminder || duration > maxReminder {
		v.add(field, "must be 0s to disable reminders or between %s and %s", humanDuration(minReminder), humanDuration(maxReminder))
		return 0, false
	}
	return duration, true
}

func (v *validator) intRange(field string, value, minimum, maximum int) bool {
	if value < minimum || value > maximum {
		v.add(field, "must be between %d and %d", minimum, maximum)
		return false
	}
	return true
}

func (v *validator) statusCodes(field string, codes []int) ([]int, bool) {
	if len(codes) == 0 {
		v.add(field, "must not be empty; omit the field for the 2xx default")
		return nil, false
	}

	sorted := append([]int(nil), codes...)
	sort.Ints(sorted)
	valid := true
	for i, code := range sorted {
		if code < 100 || code > 599 {
			v.add(field, "status code %d is outside 100..599", code)
			valid = false
		}
		if i > 0 && code == sorted[i-1] {
			v.add(field, "must contain unique status codes (duplicate %d)", code)
			valid = false
		}
	}
	if !valid {
		return nil, false
	}
	return sorted, true
}

func (v *validator) bodyMatch(field string, node *yaml.Node) (*bodyMatchSpec, bool) {
	node = resolveAlias(node)
	if node == nil || node.Kind == 0 {
		return nil, false
	}
	if node.Tag == "!!null" {
		return nil, true
	}
	if node.Kind != yaml.MappingNode {
		v.add(field, "must be a mapping with type and value, or null")
		return nil, true
	}

	var matchType, matchValue string
	var typeSet, valueSet bool
	valid := true
	seen := make(map[string]int)

	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		value := node.Content[i+1]
		if key.Kind != yaml.ScalarNode {
			v.add(field, "field names must be scalar values")
			valid = false
			continue
		}

		name := key.Value
		if firstLine, duplicate := seen[name]; duplicate {
			v.add(field+"."+name, "duplicate key (first defined at line %d)", firstLine)
			valid = false
			continue
		}
		seen[name] = key.Line

		switch name {
		case "type", "value":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				v.add(field+"."+name, "must be a string")
				valid = false
				continue
			}
			if name == "type" {
				matchType = value.Value
				typeSet = true
			} else {
				matchValue = value.Value
				valueSet = true
			}
		default:
			v.add(field+"."+name, "is not a recognized field")
			valid = false
		}
	}

	if !typeSet {
		v.add(field+".type", "is required")
		valid = false
	}
	if !valueSet {
		v.add(field+".value", "is required")
		valid = false
	}
	if !valid {
		return nil, true
	}
	if matchValue == "" {
		v.add(field+".value", "must not be empty")
		return nil, true
	}

	switch matchType {
	case "contains":
		return &bodyMatchSpec{kind: matchType, value: matchValue}, true
	case "regex":
		compiled, err := regexp.Compile(matchValue)
		if err != nil {
			message := "must be a valid RE2 regular expression"
			var syntaxErr *syntax.Error
			if errors.As(err, &syntaxErr) && syntaxErr.Code != "" {
				message += " (" + string(syntaxErr.Code) + ")"
			}
			v.add(field+".value", "%s", message)
			return nil, true
		}
		return &bodyMatchSpec{kind: matchType, value: matchValue, compiled: compiled}, true
	default:
		v.add(field+".type", `must be "contains" or "regex"`)
		return nil, true
	}
}

func (v *validator) url(field, value string) (string, bool) {
	if len(value) > maxURLBytes {
		v.add(field, "must be at most %d bytes", maxURLBytes)
		return value, false
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			v.add(field, "must not contain whitespace or control characters")
			return value, false
		}
	}

	parsed, err := url.Parse(value)
	if err != nil {
		v.add(field, "must be a valid absolute URL")
		return value, false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		v.add(field, "must use the http or https scheme")
		return value, false
	}
	if parsed.Host == "" {
		v.add(field, "must include a host")
		return value, false
	}
	if parsed.User != nil {
		v.add(field, "must not include userinfo")
		return value, false
	}
	if parsed.Fragment != "" || strings.Contains(value, "#") {
		v.add(field, "must not include a fragment")
		return value, false
	}
	return value, true
}

func (v *validator) headers(field string, headers map[string]string, reserved map[string]struct{}) []Header {
	if len(headers) > maxHeaders {
		v.add(field, "at most %d headers are allowed", maxHeaders)
	}

	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	validated := make([]Header, 0, len(headers))
	seen := make(map[string]string, len(headers))
	totalBytes := 0

	for _, name := range names {
		value := headers[name]
		if !headerNamePattern.MatchString(name) {
			v.add(field+"."+name, "is not a valid HTTP header name")
			continue
		}

		canonical := textproto.CanonicalMIMEHeaderKey(name)
		lower := strings.ToLower(canonical)
		if _, isReserved := reserved[lower]; isReserved {
			v.add(field+"."+canonical, "is reserved and cannot be configured")
			continue
		}
		if previous, duplicate := seen[lower]; duplicate {
			v.add(field+"."+canonical, "duplicates header %q (header names are case-insensitive)", previous)
			continue
		}
		seen[lower] = canonical

		if strings.ContainsAny(value, "\r\n\x00") {
			v.add(field+"."+canonical, "value must not contain CR, LF, or NUL")
			continue
		}
		if len(value) > maxHeaderValueBytes {
			v.add(field+"."+canonical, "value exceeds the 8 KiB limit")
			continue
		}

		totalBytes += len(name) + len(value)
		validated = append(validated, Header{Name: canonical, Value: value})
	}

	if totalBytes > maxHeaderTotalBytes {
		v.add(field, "total header size exceeds the 16 KiB limit")
	}

	sort.Slice(validated, func(i, j int) bool {
		return validated[i].Name < validated[j].Name
	})
	return validated
}

func (v *validator) add(field, format string, args ...any) {
	v.errs = append(v.errs, FieldError{
		Field:   field,
		Message: fmt.Sprintf(format, args...),
	})
}

func resolveDatabasePath(dir, path string) string {
	if filepath.IsAbs(path) || dir == "" {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(dir, path))
}

func humanDuration(duration time.Duration) string {
	switch {
	case duration == 0:
		return "0s"
	case duration%time.Hour == 0:
		return fmt.Sprintf("%dh", duration/time.Hour)
	case duration%time.Minute == 0:
		return fmt.Sprintf("%dm", duration/time.Minute)
	case duration%time.Second == 0:
		return fmt.Sprintf("%ds", duration/time.Second)
	case duration%time.Millisecond == 0:
		return fmt.Sprintf("%dms", duration/time.Millisecond)
	default:
		return duration.String()
	}
}
