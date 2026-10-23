package logging

import (
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	redactedValue = "[REDACTED]"
	redactedURL   = "[REDACTED_URL]"

	// timestampFormat is a fixed-width RFC3339 representation in UTC.
	timestampFormat = "2006-01-02T15:04:05.000Z07:00"
)

// safeAttributeKeys is the primary redaction control: only explicitly safe
// fields are emitted. Any other field name keeps its name but loses its value.
// This is stronger than blocking a list of known-secret names because newly
// introduced fields fail closed instead of leaking by default.
var safeAttributeKeys = map[string]struct{}{
	slog.TimeKey:            {},
	slog.LevelKey:           {},
	slog.MessageKey:         {},
	"attempt":               {},
	"backend_count":         {},
	"backend_id":            {},
	"command":               {},
	"component":             {},
	"config_schema_version": {},
	"consecutive_failures":  {},
	"duration_ms":           {},
	"error":                 {},
	"error_category":        {},
	"error_code":            {},
	"event_count":           {},
	"event_id":              {},
	"failure_reason":        {},
	"failure_threshold":     {},
	"events_deleted":        {},
	"http_status":           {},
	"incident_id":           {},
	"incident_status":       {},
	"incidents_deleted":     {},
	"observation_count":     {},
	"observations_deleted":  {},
	"open_incidents":        {},
	"operation":             {},
	"outcome":               {},
	"threshold_reached":     {},
	"version":               {},
}

var (
	urlPattern = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	// Secrets in query strings are sensitive even when the surrounding string
	// is not a parseable URL.
	querySecretPattern = regexp.MustCompile(
		`(?i)([?&](?:token|access_token|api_key|apikey|key|secret|password|signature|sig|auth|session|sessionid)=)[^&\s#]+`,
	)
	cookiePattern = regexp.MustCompile(`(?i)\b(set-cookie|cookie)\s*[:=]\s*[^\r\n]*`)
	// Authorization schemes are redacted as a backstop when they appear in a
	// free-form message rather than a structured field.
	authSchemePattern       = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[a-z0-9._~+/=-]+`)
	secretAssignmentPattern = regexp.MustCompile(
		`(?i)\b(authorization|proxy-authorization|api[-_]?key|x-api-key|access[-_]?token|refresh[-_]?token|token|secret|client[-_]?secret|password|passwd|session[-_]?id|sessionid)\b(\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`,
	)
)

// Redactor removes registered secret values and recognizable credential
// shapes from strings. A shared Redactor is safe for concurrent use.
//
// Register resolved secret values from configuration with AddSecrets. String
// patterns are a defense-in-depth backstop, not a claim that arbitrary secrets
// embedded in arbitrary text can be detected.
type Redactor struct {
	mu      sync.RWMutex
	secrets map[string]struct{}
}

// NewRedactor returns an empty Redactor.
func NewRedactor() *Redactor {
	return &Redactor{secrets: make(map[string]struct{})}
}

// AddSecrets registers values that must never appear in log output. Empty
// values are ignored. Values are matched literally and in query-escaped form.
func (r *Redactor) AddSecrets(values ...string) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.secrets == nil {
		r.secrets = make(map[string]struct{})
	}
	for _, value := range values {
		if value != "" {
			r.secrets[value] = struct{}{}
		}
	}
}

// RedactString removes registered secrets and recognized sensitive patterns
// from value.
func (r *Redactor) RedactString(value string) string {
	if value == "" {
		return value
	}

	for _, secret := range r.snapshotSecrets() {
		value = strings.ReplaceAll(value, secret, redactedValue)
		if escaped := url.QueryEscape(secret); escaped != secret {
			value = strings.ReplaceAll(value, escaped, redactedValue)
		}
	}

	value = urlPattern.ReplaceAllStringFunc(value, SafeURL)
	value = cookiePattern.ReplaceAllString(value, "$1: "+redactedValue)
	value = authSchemePattern.ReplaceAllString(value, "$1 "+redactedValue)
	value = querySecretPattern.ReplaceAllString(value, "$1"+redactedValue)
	value = secretAssignmentPattern.ReplaceAllString(value, "$1$2"+redactedValue)

	return value
}

// SafeURL renders a URL without userinfo, query string, fragment, or path.
// Only http and https URLs retain their scheme and host; other URL-like values
// are fully redacted.
func SafeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return redactedURL
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
	default:
		return redactedURL
	}
}

// ReplaceAttr implements slog.HandlerOptions.ReplaceAttr. It normalizes time,
// enforces the safe-field allowlist, redacts values, and converts errors to
// safe messages.
func (r *Redactor) ReplaceAttr(groups []string, attr slog.Attr) slog.Attr {
	// Groups are not part of the safe logging contract. Redacting the whole
	// group prevents nested header maps or request structs from leaking.
	if len(groups) > 0 {
		return slog.String(r.RedactString(attr.Key), redactedValue)
	}

	switch attr.Key {
	case slog.TimeKey:
		if attr.Value.Kind() == slog.KindTime {
			return slog.String(slog.TimeKey, attr.Value.Time().UTC().Format(timestampFormat))
		}
		return slog.String(slog.TimeKey, redactedValue)
	case slog.LevelKey:
		if attr.Value.Kind() == slog.KindAny {
			if level, ok := attr.Value.Any().(slog.Level); ok {
				return slog.String(slog.LevelKey, level.String())
			}
			return slog.String(slog.LevelKey, redactedValue)
		}
		if attr.Value.Kind() == slog.KindString {
			return slog.String(slog.LevelKey, r.RedactString(attr.Value.String()))
		}
		return slog.String(slog.LevelKey, redactedValue)
	case slog.MessageKey:
		return slog.String(slog.MessageKey, r.RedactString(attr.Value.String()))
	}

	if _, ok := safeAttributeKeys[attr.Key]; !ok {
		return slog.String(r.RedactString(attr.Key), redactedValue)
	}

	switch attr.Value.Kind() {
	case slog.KindString:
		return slog.String(attr.Key, r.RedactString(attr.Value.String()))
	case slog.KindTime:
		return slog.String(attr.Key, attr.Value.Time().UTC().Format(timestampFormat))
	case slog.KindDuration:
		return slog.Duration(attr.Key, attr.Value.Duration())
	case slog.KindBool:
		return slog.Bool(attr.Key, attr.Value.Bool())
	case slog.KindInt64:
		return slog.Int64(attr.Key, attr.Value.Int64())
	case slog.KindUint64:
		return slog.Uint64(attr.Key, attr.Value.Uint64())
	case slog.KindFloat64:
		return slog.Float64(attr.Key, attr.Value.Float64())
	case slog.KindAny:
		if err, ok := attr.Value.Any().(error); ok {
			return slog.String(attr.Key, r.RedactError(err))
		}
		return slog.String(attr.Key, redactedValue)
	default:
		return slog.String(attr.Key, redactedValue)
	}
}

// RedactError renders an error safely. Errors that implement SafeMessage are
// treated as already safe by construction. Other errors are reduced to their
// string form and passed through the redactor as a defense-in-depth backstop.
func (r *Redactor) RedactError(err error) string {
	if err == nil {
		return ""
	}

	var safe safeMessenger
	if errors.As(err, &safe) {
		return r.RedactString(safe.SafeMessage())
	}
	return r.RedactString(err.Error())
}

// safeMessenger is implemented by application errors whose Error text is safe
// for user display.
type safeMessenger interface {
	SafeMessage() string
}

func (r *Redactor) snapshotSecrets() []string {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	secrets := make([]string, 0, len(r.secrets))
	for secret := range r.secrets {
		secrets = append(secrets, secret)
	}
	r.mu.RUnlock()

	// Longer values first so a secret that contains another registered value
	// is replaced before the shorter substring.
	sort.Slice(secrets, func(i, j int) bool {
		return len(secrets[i]) > len(secrets[j])
	})
	return secrets
}
