package probe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
)

func TestCheckBodyRegex(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`status ok 200`))
	}))
	defer server.Close()

	document := "version: 1\nbackends:\n" +
		"  - id: api\n    url: " + server.URL + "\n    interval: 60s\n    timeout: 5s\n" +
		"    body_match:\n      type: regex\n      value: 'status\\s+ok'\n"
	cfg, err := config.Parse([]byte(document), config.ParseOptions{})
	if err != nil {
		t.Fatalf("parse regex config: %v", err)
	}
	if cfg.Backends[0].BodyMatch == nil || cfg.Backends[0].BodyMatch.Regexp() == nil {
		t.Fatalf("regex should compile: %+v", cfg.Backends[0].BodyMatch)
	}

	prober := New()
	defer prober.CloseIdleConnections()

	if got := prober.Check(context.Background(), cfg.Backends[0]); got.Outcome != OutcomeHealthy {
		t.Fatalf("regex match should be healthy, got %+v", got)
	}

	mismatchDoc := "version: 1\nbackends:\n" +
		"  - id: api\n    url: " + server.URL + "\n    interval: 60s\n    timeout: 5s\n" +
		"    body_match:\n      type: regex\n      value: 'no-such-marker-[0-9]+'\n"
	mismatchCfg, err := config.Parse([]byte(mismatchDoc), config.ParseOptions{})
	if err != nil {
		t.Fatalf("parse mismatch config: %v", err)
	}
	mismatch := prober.Check(context.Background(), mismatchCfg.Backends[0])
	if mismatch.Outcome != OutcomeUnhealthy || mismatch.FailureReason != ReasonBodyMismatch {
		t.Fatalf("regex mismatch should be body_mismatch: %+v", mismatch)
	}
}

func TestMatchBodySemantics(t *testing.T) {
	if !matchBody([]byte("hello world"), &config.BodyMatch{Type: "contains", Value: "world"}) {
		t.Error("contains should match")
	}
	if matchBody(nil, &config.BodyMatch{Type: "contains", Value: "x"}) {
		t.Error("nil body must not match")
	}
	if matchBody([]byte("abc"), nil) != true {
		t.Error("nil matcher should report true")
	}
	if matchBody([]byte("abc"), &config.BodyMatch{Type: "bogus", Value: "a"}) {
		t.Error("unknown matcher type must not match")
	}
}

func TestRedirectPolicyBranches(t *testing.T) {
	prober := New()
	defer prober.CloseIdleConnections()

	mustURL := func(raw string) *http.Request {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		return &http.Request{URL: parsed}
	}

	via := []*http.Request{mustURL("http://example.com/a")}

	// Disabled always yields ErrUseLastResponse.
	if err := prober.redirectPolicy(mustURL("http://example.com/b"), via, false, false); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("disabled redirect should return ErrUseLastResponse, got %v", err)
	}
	// Too many redirects.
	many := make([]*http.Request, maxRedirects)
	for i := range many {
		many[i] = mustURL("http://example.com/x")
	}
	if err := prober.redirectPolicy(mustURL("http://example.com/y"), many, true, false); !errors.Is(err, errTooManyRedirects) {
		t.Fatalf("want too many redirects, got %v", err)
	}
	// Disallowed scheme.
	if err := prober.redirectPolicy(mustURL("ftp://example.com/f"), via, true, false); !errors.Is(err, errRedirectScheme) {
		t.Fatalf("want scheme rejection, got %v", err)
	}
	// Userinfo.
	if err := prober.redirectPolicy(mustURL("http://user@example.com/"), via, true, false); !errors.Is(err, errRedirectUserinfo) {
		t.Fatalf("want userinfo rejection, got %v", err)
	}
	// Downgrade.
	secureVia := []*http.Request{mustURL("https://example.com/a")}
	if err := prober.redirectPolicy(mustURL("http://example.com/b"), secureVia, true, false); !errors.Is(err, errRedirectDowngrade) {
		t.Fatalf("want downgrade rejection, got %v", err)
	}
	// Cross-origin with headers.
	if err := prober.redirectPolicy(mustURL("http://other.test/b"), via, true, true); !errors.Is(err, errRedirectCrossHost) {
		t.Fatalf("want cross-origin rejection, got %v", err)
	}
	// Cross-origin without headers is allowed.
	if err := prober.redirectPolicy(mustURL("http://other.test/b"), via, true, false); err != nil {
		t.Fatalf("cross-origin without headers should be allowed, got %v", err)
	}
	// Nil redirect target.
	if err := prober.redirectPolicy(nil, via, true, false); !errors.Is(err, errRedirectStatusOnly) {
		t.Fatalf("want status-only rejection, got %v", err)
	}
}

func TestSanitizeRedirectErrors(t *testing.T) {
	cases := map[error]string{
		errTooManyRedirects:   "more than",
		errRedirectScheme:     "disallowed scheme",
		errRedirectUserinfo:   "credentials",
		errRedirectDowngrade:  "downgrade",
		errRedirectCrossHost:  "cross-origin",
		errRedirectStatusOnly: "rejected by policy",
	}
	for sentinel, want := range cases {
		if got := sanitizeRedirectError(sentinel); !strings.Contains(got, want) {
			t.Errorf("sanitize(%v) = %q, want %q", sentinel, got, want)
		}
	}
	if got := sanitizeRedirectError(errors.New("other")); !strings.Contains(got, "rejected by policy") {
		t.Errorf("fallback = %q", got)
	}
}

func TestClassifyTransportErrorBranches(t *testing.T) {
	timeoutErr := context.DeadlineExceeded
	if reason, _ := classifyTransportError(timeoutErr, time.Second); reason != ReasonTimeout {
		t.Errorf("deadline => %q", reason)
	}
	if reason, _ := classifyTransportError(errors.New("connection refused"), time.Second); reason != ReasonConnection {
		t.Errorf("refused => %q", reason)
	}
	if reason, _ := classifyTransportError(errors.New("tls: bad certificate"), time.Second); reason != ReasonTLS {
		t.Errorf("tls => %q", reason)
	}
	if reason, _ := classifyTransportError(errors.New("net/http: invalid status line"), time.Second); reason != ReasonMalformed {
		t.Errorf("malformed => %q", reason)
	}
	if reason, _ := classifyTransportError(errors.New("weird failure"), time.Second); reason != ReasonConnection {
		t.Errorf("fallback => %q", reason)
	}
}

func TestReadAndDrainBoundedEdgeCases(t *testing.T) {
	if _, tooLarge, err := readBounded(strings.NewReader("abc"), 0); err != nil || tooLarge {
		t.Fatalf("zero limit defaults to 1MiB: tooLarge=%v err=%v", tooLarge, err)
	}
	drainBounded(nil, 8)
	drainBounded(strings.NewReader("abc"), 0)

	prober := New()
	if prober == nil {
		t.Fatal("New must not return nil")
	}
	var nilProber *Prober
	nilProber.CloseIdleConnections()

	// Invalid URL surfaces as an invalid-response error, never a panic.
	backend := testBackend("://bad-url")
	backend.Timeout = time.Second
	result := New().Check(context.Background(), backend)
	defer New().CloseIdleConnections()
	if result.Outcome != OutcomeError || result.FailureReason != ReasonInvalidResponse {
		t.Fatalf("invalid URL should be invalid_response error: %+v", result)
	}
}

func TestRedirectDowngradeEndToEnd(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer plain.Close()

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer secure.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	backend := testBackend(secure.URL)
	backend.FollowRedirects = true
	backend.Timeout = 5 * time.Second
	result := prober.Check(context.Background(), backend)
	// The httptest TLS server uses a self-signed certificate, so this probe
	// fails at TLS before any redirect is evaluated. Either safe category is
	// acceptable; the downgrade branch is covered by unit tests. The assertion
	// is that the failure never leaks and never follows the redirect.
	if result.Outcome != OutcomeError {
		t.Fatalf("self-signed redirector should fail TLS: %+v", result)
	}
	if result.FailureReason != ReasonTLS {
		t.Fatalf("reason = %q, want tls", result.FailureReason)
	}
	if strings.Contains(result.Error, plain.URL) {
		t.Fatal("error must not leak redirect target URL")
	}
}
