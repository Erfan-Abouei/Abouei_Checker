// Package notify implements generic webhook notification delivery for
// AboueiChecker: versioned payload encoding, HTTP delivery with response
// classification, bounded exponential-backoff retries, and a durable outbox
// worker.
//
// Delivery is at-least-once, never exactly-once: a crash between remote
// success and local status commit retries with the same event ID, and
// consumers must deduplicate by event_id (also sent as Idempotency-Key).
// Payloads never contain secrets, headers, URLs, or response bodies.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/buildinfo"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
)

const (
	// PayloadSchemaVersion is the stable webhook payload version.
	PayloadSchemaVersion = "1"

	// maxWebhookResponseBytes bounds response reads; bodies are discarded and
	// never logged or persisted.
	maxWebhookResponseBytes = 64 << 10

	// workerIdlePoll bounds how long the worker sleeps when no event is due.
	workerIdlePoll = 5 * time.Second

	// maxLastErrorBytes bounds sanitized failure summaries in storage.
	maxLastErrorBytes = 512
)

// BackendRef identifies the monitored backend in a payload.
type BackendRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// IncidentSnapshot is the incident state embedded in a payload.
type IncidentSnapshot struct {
	ID             string  `json:"id"`
	Status         string  `json:"status"`
	OpenedAt       string  `json:"opened_at"`
	FirstFailureAt string  `json:"first_failure_at"`
	ResolvedAt     *string `json:"resolved_at"`
	FailureCount   int     `json:"failure_count"`
	RecoveryCount  int     `json:"recovery_count"`
	LastFailureAt  *string `json:"last_failure_at,omitempty"`
	LastObservedAt string  `json:"last_observed_at"`
	LastStatusCode *int    `json:"last_status_code,omitempty"`
	LastError      *string `json:"last_error,omitempty"`
	DurationMs     int64   `json:"duration_ms"`
}

// ReminderInfo is present only for reminder events.
type ReminderInfo struct {
	ScheduledAt string `json:"scheduled_at"`
}

// PayloadInput carries everything needed to encode one event.
type PayloadInput struct {
	EventID    string
	EventType  string
	Sequence   int
	OccurredAt time.Time
	Backend    BackendRef
	Incident   IncidentSnapshot
	ReminderAt *time.Time
}

// payload is the wire encoding of PayloadInput.
type payload struct {
	SchemaVersion string           `json:"schema_version"`
	EventID       string           `json:"event_id"`
	EventType     string           `json:"event_type"`
	Sequence      int              `json:"sequence"`
	OccurredAt    string           `json:"occurred_at"`
	Backend       BackendRef       `json:"backend"`
	Incident      IncidentSnapshot `json:"incident"`
	Reminder      *ReminderInfo    `json:"reminder"`
}

// BuildPayload encodes one versioned, compact JSON webhook payload. All
// inputs must already be sanitized; the encoder adds no secrets.
func BuildPayload(in PayloadInput) ([]byte, error) {
	document := payload{
		SchemaVersion: PayloadSchemaVersion,
		EventID:       in.EventID,
		EventType:     in.EventType,
		Sequence:      in.Sequence,
		OccurredAt:    in.OccurredAt.UTC().Format(time.RFC3339),
		Backend:       in.Backend,
		Incident:      in.Incident,
		Reminder:      nil,
	}
	if in.ReminderAt != nil {
		scheduled := in.ReminderAt.UTC().Format(time.RFC3339)
		document.Reminder = &ReminderInfo{ScheduledAt: scheduled}
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("notify: encode payload: %w", err)
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}

// FormatTime renders a timestamp for payloads.
func FormatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// FormatOptionalTime renders an optional timestamp, or nil for JSON null.
func FormatOptionalTime(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	formatted := t.UTC().Format(time.RFC3339)
	return &formatted
}

// BoundError truncates a sanitized summary to the storage bound on a UTF-8
// rune boundary. Callers must sanitize before bounding.
func BoundError(message string) string {
	if len(message) <= maxLastErrorBytes {
		return message
	}
	cut := maxLastErrorBytes
	for cut > 0 && message[cut]&0xC0 == 0x80 {
		cut--
	}
	return message[:cut]
}

// Verdict classifies a delivery attempt.
type Verdict int

const (
	// VerdictDelivered marks 2xx responses.
	VerdictDelivered Verdict = iota
	// VerdictRetryable marks transient failures worth retrying.
	VerdictRetryable
	// VerdictPermanent marks failures that must not be retried.
	VerdictPermanent
)

// Classify maps an HTTP status or transport error to a delivery verdict. A
// nil error with status 0 (e.g., a redirect blocked by policy) is permanent.
func Classify(statusCode int, err error) Verdict {
	if err != nil {
		if isRedirectBlocked(err) {
			return VerdictPermanent
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return VerdictRetryable
		}
		return VerdictRetryable
	}
	switch {
	case statusCode >= 200 && statusCode <= 299:
		return VerdictDelivered
	case statusCode == http.StatusRequestTimeout,
		statusCode == http.StatusTooEarly,
		statusCode == http.StatusTooManyRequests,
		statusCode >= 500 && statusCode <= 599:
		return VerdictRetryable
	default:
		return VerdictPermanent
	}
}

// BackoffDelay computes full-jitter exponential backoff: a uniform random
// delay in [0, min(maximum, initial*2^(attempt-1))], clamped to at least 1ms
// so retries never spin. attempt is 1-based. jitter supplies uniform values
// in [0, n); pass nil to use a locked global source.
func BackoffDelay(attempt int, initial, maximum time.Duration, jitter func(n int64) int64) time.Duration {
	if initial <= 0 {
		initial = time.Second
	}
	if maximum <= 0 {
		maximum = initial
	}
	if maximum < initial {
		maximum = initial
	}
	if attempt < 1 {
		attempt = 1
	}
	capDelay := initial
	for i := 1; i < attempt; i++ {
		capDelay *= 2
		if capDelay >= maximum || capDelay <= 0 {
			capDelay = maximum
			break
		}
	}
	if capDelay > maximum {
		capDelay = maximum
	}
	if jitter == nil {
		jitter = lockedIntn
	}
	if capDelay <= 0 {
		return time.Millisecond
	}
	delay := time.Duration(jitter(int64(capDelay)))
	if delay < time.Millisecond {
		delay = time.Millisecond
	}
	return delay
}

var (
	jitterMu sync.Mutex
	// jitterSource is a locked math/rand source used only for retry jitter;
	// it never generates secrets or identity values (those use crypto/rand).
	//
	//nolint:gosec // jitter timing is not security-sensitive.
	jitterSource = rand.New(rand.NewSource(time.Now().UnixNano()))
)

func lockedIntn(n int64) int64 {
	if n <= 0 {
		return 0
	}
	jitterMu.Lock()
	defer jitterMu.Unlock()
	return jitterSource.Int63n(n)
}

// ParseRetryAfter parses a Retry-After header as seconds or an HTTP date,
// returning the delay and whether the header was valid. Invalid values report
// false so callers fall back to backoff.
func ParseRetryAfter(header string, now time.Time) (time.Duration, bool) {
	trimmed := strings.TrimSpace(header)
	if trimmed == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	if date, err := http.ParseTime(trimmed); err == nil {
		delay := date.Sub(now)
		if delay < 0 {
			delay = 0
		}
		return delay, true
	}
	return 0, false
}

// WebhookConfig is the resolved delivery configuration held in memory.
type WebhookConfig struct {
	URL     string
	Headers []config.Header
	Timeout time.Duration
}

// RetryConfig bounds delivery attempts.
type RetryConfig struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// RetryConfigFrom converts validated configuration, guarding against zero
// values so the worker never spins or retries forever.
func RetryConfigFrom(retry config.Retry) RetryConfig {
	maxAttempts := retry.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	initial := retry.InitialBackoff
	if initial <= 0 {
		initial = 5 * time.Second
	}
	maximum := retry.MaxBackoff
	if maximum < initial {
		maximum = initial
	}
	return RetryConfig{MaxAttempts: maxAttempts, InitialBackoff: initial, MaxBackoff: maximum}
}

// Sender performs one webhook HTTP attempt. It never follows redirects and
// never logs payloads, URLs, or response bodies.
type Sender struct {
	client    *http.Client
	userAgent string
}

// NewSender builds a sender with a bounded, redirect-rejecting transport.
func NewSender() *Sender {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		transport = &http.Transport{}
	}
	cloned := transport.Clone()
	cloned.Proxy = nil
	cloned.DisableCompression = true
	cloned.MaxResponseHeaderBytes = maxWebhookResponseBytes
	return &Sender{
		client: &http.Client{
			Transport: cloned,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errWebhookRedirect
			},
		},
		userAgent: "abouei-checker/" + buildinfo.Current().Version,
	}
}

var errWebhookRedirect = errors.New("webhook redirects are not followed")

// AttemptResult reports one HTTP attempt.
type AttemptResult struct {
	StatusCode int
	RetryAfter string
	Err        error
}

// Send POSTs one payload with the attempt timeout. Custom headers are copied
// per request; Content-Type, User-Agent, and Idempotency-Key are set by the
// sender and cannot be overridden.
func (s *Sender) Send(
	ctx context.Context,
	cfg WebhookConfig,
	eventID string,
	payload []byte,
) AttemptResult {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(
		attemptCtx, http.MethodPost, cfg.URL, bytes.NewReader(payload),
	)
	if err != nil {
		return AttemptResult{Err: err}
	}
	for _, header := range cfg.Headers {
		request.Header.Set(header.Name, header.Value)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", s.userAgent)
	request.Header.Set("Idempotency-Key", eventID)

	response, err := s.client.Do(request) //nolint:bodyclose // body is drained and closed below on every path.
	if err != nil {
		return AttemptResult{Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxWebhookResponseBytes+1))
		_ = response.Body.Close()
	}()
	return AttemptResult{
		StatusCode: response.StatusCode,
		RetryAfter: response.Header.Get("Retry-After"),
	}
}

// SafeSummary renders a redaction-safe one-line delivery failure summary. It
// contains only the outcome class and timing, never URLs, headers, payloads,
// or response bodies.
func SafeSummary(statusCode int, err error, timeout time.Duration) string {
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Sprintf("webhook request timed out after %s", timeout)
		}
		lowered := strings.ToLower(err.Error())
		switch {
		case strings.Contains(lowered, "certificate"),
			strings.Contains(lowered, "tls:"),
			strings.Contains(lowered, "x509"),
			strings.Contains(lowered, "handshake"):
			return "webhook TLS verification failed"
		case strings.Contains(lowered, "connection refused"),
			strings.Contains(lowered, "no such host"),
			strings.Contains(lowered, "network is unreachable"),
			strings.Contains(lowered, "dial"):
			return "webhook connection failed"
		case isRedirectBlocked(err):
			return "webhook redirect rejected"
		case errors.Is(err, context.Canceled):
			return "webhook delivery canceled"
		default:
			return "webhook delivery failed"
		}
	}
	if statusCode >= 200 && statusCode <= 299 {
		return "webhook delivered"
	}
	return fmt.Sprintf("webhook returned status %d", statusCode)
}

func isRedirectBlocked(err error) bool {
	return errors.Is(err, errWebhookRedirect)
}
