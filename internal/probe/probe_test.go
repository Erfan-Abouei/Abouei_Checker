package probe

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
)

func testBackend(testURL string) config.Backend {
	return config.Backend{
		ID:               "api",
		Name:             "API",
		URL:              testURL,
		Interval:         60 * time.Second,
		Timeout:          5 * time.Second,
		FailureThreshold: 3,
		MaxResponseBytes: 1 << 20,
	}
}

func TestCheckHealthyDefault2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	result := prober.Check(context.Background(), testBackend(server.URL))
	if result.Outcome != OutcomeHealthy {
		t.Fatalf("outcome = %q, want healthy (%+v)", result.Outcome, result)
	}
	if result.HTTPStatus != http.StatusOK {
		t.Fatalf("status = %d, want 200", result.HTTPStatus)
	}
	if result.FailureReason != ReasonNone || result.Error != "" {
		t.Fatalf("healthy result must not carry reason/error: %+v", result)
	}
	if result.ObservedAt.IsZero() {
		t.Fatal("ObservedAt must be set")
	}
}

func TestCheckAcceptedAndRejectedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer server.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	backend := testBackend(server.URL)
	backend.AcceptedStatusCodes = []int{200, 418}
	if got := prober.Check(context.Background(), backend); got.Outcome != OutcomeHealthy {
		t.Fatalf("explicit 418 should be healthy, got %+v", got)
	}

	backend.AcceptedStatusCodes = []int{200}
	rejected := prober.Check(context.Background(), backend)
	if rejected.Outcome != OutcomeUnhealthy {
		t.Fatalf("outcome = %q, want unhealthy", rejected.Outcome)
	}
	if rejected.FailureReason != ReasonStatusMismatch {
		t.Fatalf("reason = %q, want status_mismatch", rejected.FailureReason)
	}
	if rejected.HTTPStatus != http.StatusTeapot {
		t.Fatalf("status = %d, want 418", rejected.HTTPStatus)
	}
	if !strings.Contains(rejected.Error, "418") {
		t.Fatalf("safe error should mention status, got %q", rejected.Error)
	}
}

func TestCheckRedirectDisabledByDefault(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	backend := testBackend(redirector.URL)
	result := prober.Check(context.Background(), backend)
	if result.Outcome != OutcomeUnhealthy {
		t.Fatalf("3xx with redirects disabled should be unhealthy, got %+v", result)
	}
	if result.HTTPStatus != http.StatusFound {
		t.Fatalf("status = %d, want 302 final", result.HTTPStatus)
	}
	if result.FailureReason != ReasonStatusMismatch {
		t.Fatalf("reason = %q, want status_mismatch", result.FailureReason)
	}
}

func TestCheckRedirectFollowedSameOrigin(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	backend := testBackend(server.URL + "/redirect")
	backend.FollowRedirects = true
	result := prober.Check(context.Background(), backend)
	if result.Outcome != OutcomeHealthy || result.HTTPStatus != http.StatusOK {
		t.Fatalf("same-origin redirect should be followed, got %+v", result)
	}
}

func TestCheckRedirectCrossOriginWithHeadersRejected(t *testing.T) {
	first := httptest.NewServer(nil)
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()
	first.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL, http.StatusFound)
	})

	prober := New()
	defer prober.CloseIdleConnections()

	backend := testBackend(first.URL)
	backend.FollowRedirects = true
	backend.Headers = []config.Header{{Name: "X-Api-Key", Value: "secret-value"}}
	result := prober.Check(context.Background(), backend)
	if result.Outcome != OutcomeUnhealthy {
		t.Fatalf("cross-origin redirect with custom headers should be unhealthy, got %+v", result)
	}
	if result.FailureReason != ReasonRedirectRejected {
		t.Fatalf("reason = %q, want redirect_rejected", result.FailureReason)
	}
	if strings.Contains(result.Error, "secret-value") {
		t.Fatal("error must not leak header values")
	}
}

func TestCheckTransportFailureIsError(t *testing.T) {
	prober := New()
	defer prober.CloseIdleConnections()

	backend := testBackend("http://127.0.0.1:1/health")
	backend.Timeout = 2 * time.Second
	result := prober.Check(context.Background(), backend)
	if result.Outcome != OutcomeError {
		t.Fatalf("outcome = %q, want error", result.Outcome)
	}
	if result.FailureReason != ReasonConnection {
		t.Fatalf("reason = %q, want connection", result.FailureReason)
	}
	if result.HTTPStatus != 0 {
		t.Fatalf("status should be 0 without a response, got %d", result.HTTPStatus)
	}
}

func TestCheckTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	backend := testBackend(server.URL)
	backend.Timeout = 100 * time.Millisecond
	start := time.Now()
	result := prober.Check(context.Background(), backend)
	elapsed := time.Since(start)
	if result.Outcome != OutcomeError || result.FailureReason != ReasonTimeout {
		t.Fatalf("want timeout error, got %+v", result)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("timeout did not cancel promptly: %s", elapsed)
	}
	if !strings.Contains(result.Error, "timed out") {
		t.Fatalf("safe timeout message expected, got %q", result.Error)
	}
}

func TestCheckParentCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	ctx, cancel := context.WithCancel(context.Background())
	backend := testBackend(server.URL)
	backend.Timeout = 10 * time.Second
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	result := prober.Check(ctx, backend)
	if result.Outcome != OutcomeCanceled {
		t.Fatalf("outcome = %q, want canceled", result.Outcome)
	}
	if result.FailureReason != ReasonCanceled {
		t.Fatalf("reason = %q, want canceled", result.FailureReason)
	}
}

func TestCheckCanceledBeforeStart(t *testing.T) {
	prober := New()
	defer prober.CloseIdleConnections()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := prober.Check(ctx, testBackend("http://127.0.0.1:1"))
	if result.Outcome != OutcomeCanceled {
		t.Fatalf("outcome = %q, want canceled", result.Outcome)
	}
}

func TestCheckTLSFailure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	// The default prober trusts only the system roots, so the httptest
	// self-signed certificate must fail verification. Trusting it would
	// require weakening verification, which the product forbids.
	backend := testBackend(server.URL)
	backend.Timeout = 5 * time.Second
	result := prober.Check(context.Background(), backend)
	if result.Outcome != OutcomeError {
		t.Fatalf("outcome = %q, want error", result.Outcome)
	}
	if result.FailureReason != ReasonTLS {
		t.Fatalf("reason = %q, want tls", result.FailureReason)
	}
	var _ = tls.VersionTLS12
}

func TestCheckBodyContains(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	backend := testBackend(server.URL)
	backend.BodyMatch = &config.BodyMatch{Type: "contains", Value: `"status":"ok"`}
	if got := prober.Check(context.Background(), backend); got.Outcome != OutcomeHealthy {
		t.Fatalf("contains match should be healthy, got %+v", got)
	}
	if got := prober.Check(context.Background(), backend); got.BodyMatched == nil || !*got.BodyMatched {
		t.Fatalf("BodyMatched should be true, got %+v", got)
	}

	backend.BodyMatch = &config.BodyMatch{Type: "contains", Value: "missing-marker"}
	mismatch := prober.Check(context.Background(), backend)
	if mismatch.Outcome != OutcomeUnhealthy || mismatch.FailureReason != ReasonBodyMismatch {
		t.Fatalf("want body_mismatch unhealthy, got %+v", mismatch)
	}
	if mismatch.BodyMatched == nil || *mismatch.BodyMatched {
		t.Fatalf("BodyMatched should be false, got %+v", mismatch)
	}
}

func TestCheckBodyMismatchOnlyAfterAcceptedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("no match here"))
	}))
	defer server.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	backend := testBackend(server.URL)
	backend.BodyMatch = &config.BodyMatch{Type: "contains", Value: "no match here"}
	result := prober.Check(context.Background(), backend)
	if result.FailureReason != ReasonStatusMismatch {
		t.Fatalf("status must be evaluated before body, got %+v", result)
	}
	if result.BodyMatched != nil {
		t.Fatalf("body stage must not run after status rejection: %+v", result)
	}
}

func TestCheckEmptyBodyWithMatcherIsMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	backend := testBackend(server.URL)
	backend.BodyMatch = &config.BodyMatch{Type: "contains", Value: "anything"}
	result := prober.Check(context.Background(), backend)
	if result.Outcome != OutcomeUnhealthy || result.FailureReason != ReasonBodyMismatch {
		t.Fatalf("empty body must not match, got %+v", result)
	}
}

func TestCheckOversizedBodyWithMatcher(t *testing.T) {
	big := strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer server.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	backend := testBackend(server.URL)
	backend.MaxResponseBytes = 8
	backend.BodyMatch = &config.BodyMatch{Type: "contains", Value: "a"}
	result := prober.Check(context.Background(), backend)
	if result.Outcome != OutcomeUnhealthy || result.FailureReason != ReasonResponseTooLarge {
		t.Fatalf("want response_too_large, got %+v", result)
	}
	if strings.Contains(result.Error, big) {
		t.Fatal("error must not include response body")
	}
}

func TestCheckOversizedBodyWithoutMatcherStaysHealthy(t *testing.T) {
	big := strings.Repeat("b", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer server.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	backend := testBackend(server.URL)
	backend.MaxResponseBytes = 8
	result := prober.Check(context.Background(), backend)
	if result.Outcome != OutcomeHealthy {
		t.Fatalf("oversized body without matcher must stay healthy, got %+v", result)
	}
}

func TestCheckCustomHeadersSentAndRedacted(t *testing.T) {
	const secret = "test-secret-token-abc123"
	var gotAuth, gotAPI string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPI = r.Header.Get("X-Api-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	backend := testBackend(server.URL)
	backend.Headers = []config.Header{
		{Name: "Authorization", Value: "Bearer " + secret},
		{Name: "X-Api-Key", Value: secret},
	}
	result := prober.Check(context.Background(), backend)
	if result.Outcome != OutcomeHealthy {
		t.Fatalf("want healthy, got %+v", result)
	}
	if gotAuth != "Bearer "+secret || gotAPI != secret {
		t.Fatalf("custom headers not forwarded: auth=%q api=%q", gotAuth, gotAPI)
	}
	if strings.Contains(result.Error, secret) {
		t.Fatal("result error must not contain secrets")
	}
}

func TestCheckNoRetriesOnFailure(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	backend := testBackend(server.URL)
	result := prober.Check(context.Background(), backend)
	if result.Outcome != OutcomeUnhealthy {
		t.Fatalf("want unhealthy, got %+v", result)
	}
	if hits != 1 {
		t.Fatalf("probe must not retry: hits = %d", hits)
	}
}

func TestCheckDoesNotMutateSharedHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	backend := testBackend(server.URL)
	backend.Headers = []config.Header{{Name: "X-Api-Key", Value: "v"}}
	before := append([]config.Header(nil), backend.Headers...)
	_ = prober.Check(context.Background(), backend)
	if len(backend.Headers) != len(before) || backend.Headers[0] != before[0] {
		t.Fatal("Check must not mutate the configured header slice")
	}
}

func TestCheckUserAgentDefaultAndOverride(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	if result := prober.Check(context.Background(), testBackend(server.URL)); result.Outcome != OutcomeHealthy {
		t.Fatalf("want healthy, got %+v", result)
	}
	if !strings.HasPrefix(got, "abouei-checker/") {
		t.Fatalf("default User-Agent = %q, want abouei-checker/ prefix", got)
	}

	backend := testBackend(server.URL)
	backend.Headers = []config.Header{{Name: "User-Agent", Value: "custom-agent"}}
	if result := prober.Check(context.Background(), backend); result.Outcome != OutcomeHealthy {
		t.Fatalf("want healthy, got %+v", result)
	}
	if got != "custom-agent" {
		t.Fatalf("User-Agent override = %q", got)
	}
}

func TestCheckConcurrentUse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	prober := New()
	defer prober.CloseIdleConnections()

	backend := testBackend(server.URL)
	done := make(chan Result, 16)
	for i := 0; i < 16; i++ {
		go func() {
			done <- prober.Check(context.Background(), backend)
		}()
	}
	for i := 0; i < 16; i++ {
		if result := <-done; result.Outcome != OutcomeHealthy {
			t.Fatalf("concurrent check failed: %+v", result)
		}
	}
}
