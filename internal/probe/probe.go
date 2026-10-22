// Package probe performs safe HTTP health checks against configured backends.
//
// Probing is a pure network operation: it performs one GET request with a
// bounded response read, evaluates the configured status and body criteria,
// and returns a normalized Result. It never persists state, sends
// notifications, or logs secrets.
package probe

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/buildinfo"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
)

const (
	// maxResponseHeaderBytes bounds response headers to avoid memory exhaustion.
	maxResponseHeaderBytes = 64 << 10

	// maxRedirects is the maximum number of redirects followed when a backend
	// enables follow_redirects.
	maxRedirects = 5
)

// Outcome classifies a probe result.
type Outcome string

const (
	// OutcomeHealthy means the request satisfied the configured health criteria.
	OutcomeHealthy Outcome = "healthy"
	// OutcomeUnhealthy means the request completed but failed an approved
	// health criterion (unexpected status, body mismatch, oversized body
	// with a matcher, or rejected redirect).
	OutcomeUnhealthy Outcome = "unhealthy"
	// OutcomeError means the request could not be evaluated because of a
	// transport, DNS, TLS, timeout, or malformed-response failure.
	OutcomeError Outcome = "error"
	// OutcomeCanceled means execution was intentionally canceled before a
	// conclusive result could be produced.
	OutcomeCanceled Outcome = "canceled"
)

// Reason is a sanitized failure category. It never carries secrets, URLs, or
// response bodies.
type Reason string

const (
	// ReasonNone marks healthy results, which carry no failure category.
	ReasonNone Reason = ""
	// ReasonTimeout marks requests that exceeded the configured timeout.
	ReasonTimeout Reason = "timeout"
	// ReasonConnection marks DNS, dial, and transport failures.
	ReasonConnection Reason = "connection"
	// ReasonTLS marks certificate and handshake failures.
	ReasonTLS Reason = "tls"
	// ReasonStatusMismatch marks rejected final status codes.
	ReasonStatusMismatch Reason = "status_mismatch"
	// ReasonBodyMismatch marks configured body conditions that did not match.
	ReasonBodyMismatch Reason = "body_mismatch"
	// ReasonResponseTooLarge marks bodies beyond max_response_bytes with a matcher.
	ReasonResponseTooLarge Reason = "response_too_large"
	// ReasonMalformed marks malformed or truncated responses.
	ReasonMalformed Reason = "malformed_response"
	// ReasonRedirectRejected marks redirects rejected by policy.
	ReasonRedirectRejected Reason = "redirect_rejected"
	// ReasonInvalidResponse marks requests that could not be built.
	ReasonInvalidResponse Reason = "invalid_response"
	// ReasonCanceled marks intentionally canceled probes.
	ReasonCanceled Reason = "canceled"
)

// Result is the normalized outcome of one probe. It is suitable for later
// incident processing but carries no incident state itself.
type Result struct {
	// BackendID is the stable configured backend identity.
	BackendID string
	// Name is the display name.
	Name string
	// Outcome is the normalized probe outcome.
	Outcome Outcome
	// HTTPStatus is the received status code, or 0 when no response exists.
	HTTPStatus int
	// Duration is the wall-clock request duration.
	Duration time.Duration
	// ObservedAt is the UTC timestamp when the probe started.
	ObservedAt time.Time
	// FailureReason is the sanitized failure category (empty when healthy).
	FailureReason Reason
	// Error is a safe, sanitized diagnostic summary (empty when healthy).
	Error string
	// BodyMatched reports the body-condition outcome. Nil when no matcher is
	// configured or when evaluation did not reach the body stage.
	BodyMatched *bool
}

// Healthy reports whether the probe satisfied its health criteria.
func (r Result) Healthy() bool {
	return r.Outcome == OutcomeHealthy
}

// Prober performs HTTP health checks with a shared, securely configured
// transport. The zero value is not usable; construct with New.
type Prober struct {
	transport *http.Transport
	userAgent string
}

// New builds a Prober with a reusable transport. TLS verification is always
// enabled, the environment proxy is never honored implicitly, and response
// headers are bounded.
func New() *Prober {
	return &Prober{
		transport: newTransport(),
		userAgent: "abouei-checker/" + buildinfo.Current().Version,
	}
}

func newTransport() *http.Transport {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		transport = &http.Transport{}
	}
	cloned := transport.Clone()
	cloned.Proxy = nil
	cloned.DisableCompression = true
	cloned.MaxResponseHeaderBytes = maxResponseHeaderBytes
	cloned.ForceAttemptHTTP2 = true
	return cloned
}

// CloseIdleConnections releases pooled connections. Callers should invoke it
// when the owning process exits.
func (p *Prober) CloseIdleConnections() {
	if p == nil || p.transport == nil {
		return
	}
	p.transport.CloseIdleConnections()
}

var (
	errTooManyRedirects   = errors.New("too many redirects")
	errRedirectScheme     = errors.New("redirect target uses a disallowed scheme")
	errRedirectUserinfo   = errors.New("redirect target contains userinfo")
	errRedirectDowngrade  = errors.New("redirect downgrades https to http")
	errRedirectCrossHost  = errors.New("cross-origin redirect with custom headers is rejected")
	errRedirectStatusOnly = errors.New("redirect rejected by policy")
)

// Check performs one health check for backend. It honors the configured
// per-backend timeout, propagates parent cancellation, and always closes the
// response body. It never logs secrets and never mutates backend.
func (p *Prober) Check(ctx context.Context, backend config.Backend) Result {
	start := time.Now()
	observedAt := start.UTC()
	newResult := func(outcome Outcome, status int, reason Reason, errMsg string, matched *bool) Result {
		return Result{
			BackendID:     backend.ID,
			Name:          backend.Name,
			Outcome:       outcome,
			HTTPStatus:    status,
			Duration:      time.Since(start),
			ObservedAt:    observedAt,
			FailureReason: reason,
			Error:         errMsg,
			BodyMatched:   matched,
		}
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return newResult(OutcomeCanceled, 0, ReasonCanceled, "probe canceled before start", nil)
	}

	timeout := backend.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, backend.URL, nil)
	if err != nil {
		if isCanceled(ctx, reqCtx, err) {
			return newResult(OutcomeCanceled, 0, ReasonCanceled, "probe canceled", nil)
		}
		return newResult(OutcomeError, 0, ReasonInvalidResponse, "invalid backend request", nil)
	}

	hasCustomHeaders := len(backend.Headers) > 0
	userAgentSet := false
	for _, header := range backend.Headers {
		req.Header.Set(header.Name, header.Value)
		if strings.EqualFold(header.Name, "User-Agent") {
			userAgentSet = true
		}
	}
	if !userAgentSet {
		req.Header.Set("User-Agent", p.userAgent)
	}
	req.Header.Set("Accept-Encoding", "identity")

	client := &http.Client{
		Transport: p.transport,
		CheckRedirect: func(redirect *http.Request, via []*http.Request) error {
			return p.redirectPolicy(redirect, via, backend.FollowRedirects, hasCustomHeaders)
		},
	}

	resp, err := client.Do(req) //nolint:bodyclose // body is closed on every success path below.
	if err != nil {
		if isCanceled(ctx, reqCtx, err) {
			return newResult(OutcomeCanceled, 0, ReasonCanceled, "probe canceled", nil)
		}
		if redirectErr, ok := asRedirectPolicyError(err); ok {
			return newResult(OutcomeUnhealthy, 0, ReasonRedirectRejected, redirectErr, nil)
		}
		reason, message := classifyTransportError(err, timeout)
		return newResult(OutcomeError, 0, reason, message, nil)
	}

	status := 0
	if resp != nil {
		status = resp.StatusCode
	}

	if !backend.AcceptsStatus(status) {
		drainBounded(resp.Body, backend.MaxResponseBytes)
		_ = resp.Body.Close()
		return newResult(
			OutcomeUnhealthy,
			status,
			ReasonStatusMismatch,
			fmt.Sprintf("unexpected status code %d", status),
			nil,
		)
	}

	if backend.BodyMatch == nil {
		// No matcher: enforce a bounded drain so a huge body cannot exhaust
		// memory, then close. An oversized body without a matcher does not
		// fail the observation; matching was never requested.
		drainBounded(resp.Body, backend.MaxResponseBytes)
		_ = resp.Body.Close()
		return newResult(OutcomeHealthy, status, ReasonNone, "", nil)
	}

	body, tooLarge, readErr := readBounded(resp.Body, backend.MaxResponseBytes)
	_ = resp.Body.Close()
	if readErr != nil {
		if isCanceled(ctx, reqCtx, readErr) {
			return newResult(OutcomeCanceled, status, ReasonCanceled, "probe canceled", nil)
		}
		if errors.Is(readErr, reqCtx.Err()) && reqCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return newResult(OutcomeError, status, ReasonTimeout, fmt.Sprintf("request timed out after %s", timeout), nil)
		}
		return newResult(OutcomeError, status, ReasonMalformed, "malformed response body", nil)
	}
	if tooLarge {
		matched := false
		return newResult(
			OutcomeUnhealthy,
			status,
			ReasonResponseTooLarge,
			fmt.Sprintf("response body exceeds %d bytes", backend.MaxResponseBytes),
			&matched,
		)
	}

	matched := matchBody(body, backend.BodyMatch)
	if !matched {
		notMatched := false
		return newResult(OutcomeUnhealthy, status, ReasonBodyMismatch, "response body does not satisfy the configured condition", &notMatched)
	}
	ok := true
	return newResult(OutcomeHealthy, status, ReasonNone, "", &ok)
}

func (p *Prober) redirectPolicy(redirect *http.Request, via []*http.Request, follow bool, hasCustomHeaders bool) error {
	if !follow {
		return http.ErrUseLastResponse
	}
	if len(via) >= maxRedirects {
		return errTooManyRedirects
	}
	if redirect == nil || redirect.URL == nil {
		return errRedirectStatusOnly
	}
	scheme := strings.ToLower(redirect.URL.Scheme)
	if scheme != "http" && scheme != "https" {
		return errRedirectScheme
	}
	if redirect.URL.User != nil {
		return errRedirectUserinfo
	}
	if len(via) > 0 && via[0] != nil && via[0].URL != nil {
		previous := via[0].URL
		if strings.EqualFold(previous.Scheme, "https") && scheme == "http" {
			return errRedirectDowngrade
		}
		sameOrigin := strings.EqualFold(previous.Scheme, redirect.URL.Scheme) &&
			strings.EqualFold(previous.Host, redirect.URL.Host)
		if !sameOrigin && hasCustomHeaders {
			return errRedirectCrossHost
		}
	}
	return nil
}

func asRedirectPolicyError(err error) (string, bool) {
	for _, sentinel := range []error{
		errTooManyRedirects,
		errRedirectScheme,
		errRedirectUserinfo,
		errRedirectDowngrade,
		errRedirectCrossHost,
		errRedirectStatusOnly,
	} {
		if errors.Is(err, sentinel) {
			return sanitizeRedirectError(sentinel), true
		}
	}
	return "", false
}

func sanitizeRedirectError(err error) string {
	switch {
	case errors.Is(err, errTooManyRedirects):
		return fmt.Sprintf("redirect rejected: more than %d redirects", maxRedirects)
	case errors.Is(err, errRedirectScheme):
		return "redirect rejected: target uses a disallowed scheme"
	case errors.Is(err, errRedirectUserinfo):
		return "redirect rejected: target contains credentials"
	case errors.Is(err, errRedirectDowngrade):
		return "redirect rejected: https to http downgrade"
	case errors.Is(err, errRedirectCrossHost):
		return "redirect rejected: cross-origin redirect with custom headers"
	default:
		return "redirect rejected by policy"
	}
}

func isCanceled(parent, reqCtx context.Context, err error) bool {
	if parent != nil && parent.Err() == context.Canceled {
		return true
	}
	if reqCtx != nil && reqCtx.Err() == context.Canceled {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	return false
}

func classifyTransportError(err error, timeout time.Duration) (Reason, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return ReasonTimeout, fmt.Sprintf("request timed out after %s", timeout)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ReasonTimeout, fmt.Sprintf("request timed out after %s", timeout)
	}
	if isTLSError(err) {
		return ReasonTLS, "TLS verification failed"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "certificate"),
		strings.Contains(message, "tls:"),
		strings.Contains(message, "x509"),
		strings.Contains(message, "handshake"):
		return ReasonTLS, "TLS verification failed"
	case strings.Contains(message, "timeout"),
		strings.Contains(message, "deadline exceeded"):
		return ReasonTimeout, fmt.Sprintf("request timed out after %s", timeout)
	case strings.Contains(message, "connection refused"),
		strings.Contains(message, "no such host"),
		strings.Contains(message, "name resolution"),
		strings.Contains(message, "network is unreachable"),
		strings.Contains(message, "dial"),
		strings.Contains(message, "connect:"),
		strings.Contains(message, "connection reset"),
		strings.Contains(message, "connection timed out"),
		strings.Contains(message, "broken pipe"):
		return ReasonConnection, "connection failed"
	case strings.Contains(message, "malformed"),
		strings.Contains(message, "invalid status"),
		strings.Contains(message, "unexpected eof"),
		strings.Contains(message, "http:"):
		return ReasonMalformed, "malformed response"
	default:
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return ReasonConnection, "connection failed"
		}
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			return ReasonConnection, "connection failed"
		}
		return ReasonConnection, "connection failed"
	}
}

func isTLSError(err error) bool {
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return true
	}
	var x509Err x509.CertificateInvalidError
	if errors.As(err, &x509Err) {
		return true
	}
	var unknownErr x509.UnknownAuthorityError
	if errors.As(err, &unknownErr) {
		return true
	}
	var hostnameErr x509.HostnameError
	return errors.As(err, &hostnameErr)
}

func matchBody(body []byte, matcher *config.BodyMatch) bool {
	if matcher == nil {
		return true
	}
	switch matcher.Type {
	case "contains":
		return bytes.Contains(body, []byte(matcher.Value))
	case "regex":
		compiled := matcher.Regexp()
		if compiled == nil {
			return false
		}
		return compiled.Match(body)
	default:
		return false
	}
}

// readBounded reads up to limit+1 bytes. tooLarge is true when the body
// exceeds limit. A truncated body is never treated as a successful match.
func readBounded(body io.Reader, limit int) ([]byte, bool, error) {
	if limit <= 0 {
		limit = 1 << 20
	}
	data, err := io.ReadAll(io.LimitReader(body, int64(limit)+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > limit {
		return nil, true, nil
	}
	return data, false, nil
}

// drainBounded discards up to limit+1 bytes so connections can be reused
// without reading an unbounded body into memory.
func drainBounded(body io.Reader, limit int) {
	if body == nil {
		return
	}
	if limit <= 0 {
		limit = 1 << 20
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, int64(limit)+1))
}
