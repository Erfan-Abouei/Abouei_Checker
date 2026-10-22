package monitor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/probe"
)

func healthyResult(id string) probe.Result {
	return probe.Result{
		BackendID:     id,
		Name:          id,
		Outcome:       probe.OutcomeHealthy,
		HTTPStatus:    200,
		ObservedAt:    nowUTC(),
		FailureReason: probe.ReasonNone,
	}
}

func failedResult(id string) probe.Result {
	return probe.Result{
		BackendID:     id,
		Name:          id,
		Outcome:       probe.OutcomeUnhealthy,
		HTTPStatus:    503,
		ObservedAt:    nowUTC(),
		FailureReason: probe.ReasonStatusMismatch,
		Error:         "unexpected status code 503",
	}
}

func TestTrackerBelowThreshold(t *testing.T) {
	tracker := NewTracker(map[string]int{"api": 3}, map[string]string{"api": "API"})
	for i := 0; i < 2; i++ {
		state := tracker.Record(failedResult("api"))
		if state.ThresholdReached {
			t.Fatalf("threshold must not be reached after %d failures", i+1)
		}
		if state.ConsecutiveFailures != i+1 {
			t.Fatalf("streak = %d, want %d", state.ConsecutiveFailures, i+1)
		}
	}
}

func TestTrackerThresholdReached(t *testing.T) {
	tracker := NewTracker(map[string]int{"api": 2}, map[string]string{"api": "API"})
	tracker.Record(failedResult("api"))
	state := tracker.Record(failedResult("api"))
	if !state.ThresholdReached {
		t.Fatal("threshold should be reached at 2/2")
	}
	if state.ConsecutiveFailures != 2 {
		t.Fatalf("streak = %d, want 2", state.ConsecutiveFailures)
	}
}

func TestTrackerSuccessResetsStreak(t *testing.T) {
	tracker := NewTracker(map[string]int{"api": 3}, map[string]string{"api": "API"})
	tracker.Record(failedResult("api"))
	tracker.Record(failedResult("api"))
	state := tracker.Record(healthyResult("api"))
	if state.ConsecutiveFailures != 0 {
		t.Fatalf("success must reset failures, got %d", state.ConsecutiveFailures)
	}
	if state.ThresholdReached {
		t.Fatal("threshold must clear after success")
	}
	if state.ConsecutiveSuccesses != 1 {
		t.Fatalf("successes = %d, want 1", state.ConsecutiveSuccesses)
	}
}

func TestTrackerIndependentBackends(t *testing.T) {
	tracker := NewTracker(
		map[string]int{"a": 2, "b": 2},
		map[string]string{"a": "A", "b": "B"},
	)
	tracker.Record(failedResult("a"))
	tracker.Record(failedResult("a"))
	stateA, _ := tracker.Get("a")
	stateB, _ := tracker.Get("b")
	if !stateA.ThresholdReached {
		t.Fatal("backend a should reach threshold")
	}
	if stateB.ThresholdReached || stateB.ConsecutiveFailures != 0 {
		t.Fatalf("backend b must be independent: %+v", stateB)
	}
}

func TestTrackerCanceledLeavesState(t *testing.T) {
	tracker := NewTracker(map[string]int{"api": 2}, map[string]string{"api": "API"})
	tracker.Record(failedResult("api"))
	canceled := probe.Result{
		BackendID:     "api",
		Name:          "api",
		Outcome:       probe.OutcomeCanceled,
		FailureReason: probe.ReasonCanceled,
		Error:         "probe canceled",
		ObservedAt:    nowUTC(),
	}
	state := tracker.Record(canceled)
	if state.ConsecutiveFailures != 1 {
		t.Fatalf("canceled must not change streak, got %d", state.ConsecutiveFailures)
	}
	if state.LastOutcome != probe.OutcomeUnhealthy {
		t.Fatalf("canceled must not overwrite last conclusive outcome: %q", state.LastOutcome)
	}
}

func TestTrackerConcurrentRecord(t *testing.T) {
	tracker := NewTracker(map[string]int{"api": 1000}, map[string]string{"api": "API"})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tracker.Record(failedResult("api"))
		}()
	}
	wg.Wait()
	state, _ := tracker.Get("api")
	if state.ConsecutiveFailures != 50 {
		t.Fatalf("streak = %d, want 50", state.ConsecutiveFailures)
	}
}

type stubProber struct {
	handler func(ctx context.Context, backend config.Backend) probe.Result
	calls   atomic.Int64
}

func (s *stubProber) Check(ctx context.Context, backend config.Backend) probe.Result {
	s.calls.Add(1)
	return s.handler(ctx, backend)
}

func testBackends(ids ...string) []config.Backend {
	backends := make([]config.Backend, len(ids))
	for i, id := range ids {
		backends[i] = config.Backend{
			ID:               id,
			Name:             id,
			URL:              "http://127.0.0.1:1/" + id,
			Interval:         60 * time.Second,
			Timeout:          5 * time.Second,
			FailureThreshold: 2,
			MaxResponseBytes: 1 << 20,
		}
	}
	return backends
}

func TestRunOnceDeterministicOrder(t *testing.T) {
	backends := testBackends("c", "a", "b")
	stub := &stubProber{handler: func(_ context.Context, backend config.Backend) probe.Result {
		return healthyResult(backend.ID)
	}}
	tracker := NewTracker(
		map[string]int{"a": 1, "b": 1, "c": 1},
		map[string]string{"a": "a", "b": "b", "c": "c"},
	)
	outcomes := RunOnce(context.Background(), backends, stub, tracker, 2)
	if len(outcomes) != 3 {
		t.Fatalf("got %d outcomes", len(outcomes))
	}
	for i, want := range []string{"c", "a", "b"} {
		if outcomes[i].Backend.ID != want {
			t.Fatalf("order[%d] = %q, want %q (config order)", i, outcomes[i].Backend.ID, want)
		}
	}
}

func TestRunOnceOneFailureDoesNotBlockOthers(t *testing.T) {
	backends := testBackends("ok", "bad")
	stub := &stubProber{handler: func(_ context.Context, backend config.Backend) probe.Result {
		if backend.ID == "bad" {
			return failedResult(backend.ID)
		}
		return healthyResult(backend.ID)
	}}
	tracker := NewTracker(
		map[string]int{"ok": 1, "bad": 1},
		map[string]string{"ok": "ok", "bad": "bad"},
	)
	outcomes := RunOnce(context.Background(), backends, stub, tracker, 2)
	if outcomes[0].Result.Outcome != probe.OutcomeHealthy {
		t.Fatalf("ok backend: %+v", outcomes[0].Result)
	}
	if outcomes[1].Result.Outcome != probe.OutcomeUnhealthy {
		t.Fatalf("bad backend: %+v", outcomes[1].Result)
	}
	if !outcomes[1].State.ThresholdReached {
		t.Fatal("threshold 1 should be reached after one failure")
	}
}

func TestRunOnceRespectsConcurrencyLimit(t *testing.T) {
	backends := testBackends("a", "b", "c", "d")
	var current atomic.Int64
	var observedMax atomic.Int64
	stub := &stubProber{handler: func(ctx context.Context, backend config.Backend) probe.Result {
		now := current.Add(1)
		for {
			previous := observedMax.Load()
			if now <= previous || observedMax.CompareAndSwap(previous, now) {
				break
			}
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
		}
		current.Add(-1)
		return healthyResult(backend.ID)
	}}
	tracker := NewTracker(map[string]int{"a": 1, "b": 1, "c": 1, "d": 1}, map[string]string{"a": "a", "b": "b", "c": "c", "d": "d"})
	RunOnce(context.Background(), backends, stub, tracker, 2)
	if seenMax := observedMax.Load(); seenMax > 2 {
		t.Fatalf("max concurrency = %d, want <= 2", seenMax)
	}
}

func TestSchedulerNoOverlapPerBackend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(120 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	backend := config.Backend{
		ID: "slow", Name: "slow", URL: server.URL,
		Interval: 20 * time.Millisecond, Timeout: 5 * time.Second,
		FailureThreshold: 5, MaxResponseBytes: 1 << 20,
	}
	prober := probe.New()
	defer prober.CloseIdleConnections()
	tracker := NewTracker(map[string]int{"slow": 5}, map[string]string{"slow": "slow"})

	var mu sync.Mutex
	inFlight := 0
	maxInFlight := 0
	events := 0
	scheduler := NewScheduler(SchedulerOptions{
		Backends:       []config.Backend{backend},
		Prober:         &overlapDetectingProber{Prober: prober, mu: &mu, inFlight: &inFlight, maxInFlight: &maxInFlight},
		Tracker:        tracker,
		MaxConcurrency: 4,
		OnProbe: func(ProbeEvent) {
			mu.Lock()
			events++
			mu.Unlock()
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	scheduler.Run(ctx)

	mu.Lock()
	defer mu.Unlock()
	if maxInFlight > 1 {
		t.Fatalf("overlapping probes for same backend: max=%d", maxInFlight)
	}
	if events == 0 {
		t.Fatal("expected at least one probe cycle")
	}
}

type overlapDetectingProber struct {
	*probe.Prober
	mu          *sync.Mutex
	inFlight    *int
	maxInFlight *int
}

func (p *overlapDetectingProber) Check(ctx context.Context, backend config.Backend) probe.Result {
	p.mu.Lock()
	*p.inFlight++
	if *p.inFlight > *p.maxInFlight {
		*p.maxInFlight = *p.inFlight
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		*p.inFlight--
		p.mu.Unlock()
	}()
	return p.Prober.Check(ctx, backend)
}

func TestSchedulerIndependentBackendsProceed(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(400 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer fast.Close()

	backends := []config.Backend{
		{ID: "slow", Name: "slow", URL: slow.URL, Interval: time.Hour, Timeout: 5 * time.Second, FailureThreshold: 3, MaxResponseBytes: 1 << 20},
		{ID: "fast", Name: "fast", URL: fast.URL, Interval: time.Hour, Timeout: 5 * time.Second, FailureThreshold: 3, MaxResponseBytes: 1 << 20},
	}
	prober := probe.New()
	defer prober.CloseIdleConnections()
	tracker := NewTracker(map[string]int{"slow": 3, "fast": 3}, map[string]string{"slow": "slow", "fast": "fast"})

	var mu sync.Mutex
	seen := map[string]int{}
	scheduler := NewScheduler(SchedulerOptions{
		Backends: backends, Prober: prober, Tracker: tracker, MaxConcurrency: 2,
		OnProbe: func(event ProbeEvent) {
			mu.Lock()
			seen[event.Backend.ID]++
			mu.Unlock()
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	start := time.Now()
	scheduler.Run(ctx)
	elapsed := time.Since(start)

	mu.Lock()
	defer mu.Unlock()
	if seen["fast"] == 0 || seen["slow"] == 0 {
		t.Fatalf("both backends must be probed immediately: %v", seen)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("slow backend blocked fast backend: %s", elapsed)
	}
}

func TestSchedulerCancellationDuringProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	backend := config.Backend{
		ID: "slow", Name: "slow", URL: server.URL,
		Interval: time.Hour, Timeout: 10 * time.Second,
		FailureThreshold: 3, MaxResponseBytes: 1 << 20,
	}
	prober := probe.New()
	defer prober.CloseIdleConnections()
	tracker := NewTracker(map[string]int{"slow": 3}, map[string]string{"slow": "slow"})

	var outcome probe.Outcome
	scheduler := NewScheduler(SchedulerOptions{
		Backends: []config.Backend{backend}, Prober: prober, Tracker: tracker, MaxConcurrency: 1,
		OnProbe: func(event ProbeEvent) { outcome = event.Checked.Result.Outcome },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.Run(ctx)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not stop after cancellation")
	}
	if outcome != probe.OutcomeCanceled && outcome != probe.OutcomeError {
		t.Fatalf("canceled probe outcome = %q", outcome)
	}
}

func TestSchedulerRepeatedCycles(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	backend := config.Backend{
		ID: "api", Name: "api", URL: server.URL,
		Interval: 30 * time.Millisecond, Timeout: 5 * time.Second,
		FailureThreshold: 100, MaxResponseBytes: 1 << 20,
	}
	prober := probe.New()
	defer prober.CloseIdleConnections()
	tracker := NewTracker(map[string]int{"api": 100}, map[string]string{"api": "api"})
	scheduler := NewScheduler(SchedulerOptions{
		Backends: []config.Backend{backend}, Prober: prober, Tracker: tracker, MaxConcurrency: 1,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	scheduler.Run(ctx)
	if got := calls.Load(); got < 2 {
		t.Fatalf("expected repeated cycles, got %d calls", got)
	}
}

func TestSchedulerPerBackendIntervals(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	mkBackend := func(id string, interval time.Duration) config.Backend {
		return config.Backend{
			ID: id, Name: id, URL: server.URL,
			Interval: interval, Timeout: 5 * time.Second,
			FailureThreshold: 100, MaxResponseBytes: 1 << 20,
		}
	}
	backends := []config.Backend{mkBackend("fast", 30*time.Millisecond), mkBackend("slow", 5*time.Second)}
	prober := probe.New()
	defer prober.CloseIdleConnections()
	tracker := NewTracker(map[string]int{"fast": 100, "slow": 100}, map[string]string{"fast": "fast", "slow": "slow"})

	var mu sync.Mutex
	counts := map[string]int{}
	scheduler := NewScheduler(SchedulerOptions{
		Backends: backends, Prober: prober, Tracker: tracker, MaxConcurrency: 2,
		OnProbe: func(event ProbeEvent) {
			mu.Lock()
			counts[event.Backend.ID]++
			mu.Unlock()
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	scheduler.Run(ctx)

	mu.Lock()
	defer mu.Unlock()
	if counts["slow"] != 1 {
		t.Fatalf("slow backend should be probed once (immediate only): %v", counts)
	}
	if counts["fast"] < 2 {
		t.Fatalf("fast backend should cycle repeatedly: %v", counts)
	}
}

func ExampleNewTracker() {
	tracker := NewTracker(map[string]int{"api": 2}, map[string]string{"api": "API"})
	state := tracker.Record(healthyResult("api"))
	fmt.Println(state.ConsecutiveFailures, state.ThresholdReached)
	// Output: 0 false
}
