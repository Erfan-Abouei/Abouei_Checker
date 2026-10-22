package monitor

import (
	"context"
	"sync"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
)

// ProbeEvent is one completed scheduled probe with its threshold snapshot.
type ProbeEvent struct {
	// Backend is the configured backend that was probed.
	Backend config.Backend
	// Checked is the probe result with its updated streak state.
	Checked CheckedBackend
	// Cycle is the per-backend sequence number starting at 1. The initial
	// immediate probe is cycle 1.
	Cycle int
}

// Scheduler runs continuous per-backend schedules. Each backend has at most
// one probe in flight; independent backends proceed independently under a
// global concurrency bound.
type Scheduler struct {
	backends       []config.Backend
	prober         Prober
	tracker        *Tracker
	maxConcurrency int
	onProbe        func(ProbeEvent)
}

// SchedulerOptions configures a Scheduler.
type SchedulerOptions struct {
	Backends       []config.Backend
	Prober         Prober
	Tracker        *Tracker
	MaxConcurrency int
	// OnProbe receives every completed probe. It must be safe for concurrent
	// use and must not block indefinitely; it is invoked without holding
	// scheduler locks and never while a network request is in flight.
	OnProbe func(ProbeEvent)
}

// NewScheduler builds a Scheduler. Backends are copied so later caller
// mutation cannot race with running schedules.
func NewScheduler(options SchedulerOptions) *Scheduler {
	backends := append([]config.Backend(nil), options.Backends...)
	maxConcurrency := options.MaxConcurrency
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	return &Scheduler{
		backends:       backends,
		prober:         options.Prober,
		tracker:        options.Tracker,
		maxConcurrency: maxConcurrency,
		onProbe:        options.OnProbe,
	}
}

func nowUTC() time.Time {
	return time.Now().UTC()
}

// Run starts per-backend schedules, performs an immediate probe of every
// backend, then ticks each backend on its own interval until ctx is canceled.
// It returns when all backend goroutines have exited. Callers bound total
// shutdown time with runtime.shutdown_timeout.
func (s *Scheduler) Run(ctx context.Context) {
	if s == nil || len(s.backends) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	semaphore := make(chan struct{}, s.maxConcurrency)
	var wg sync.WaitGroup
	for _, backend := range s.backends {
		wg.Add(1)
		go func(current config.Backend) {
			defer wg.Done()
			s.runBackend(ctx, current, semaphore)
		}(backend)
	}
	wg.Wait()
}

func (s *Scheduler) runBackend(ctx context.Context, backend config.Backend, semaphore chan struct{}) {
	interval := backend.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	cycle := 0
	probeOnce := func() bool {
		select {
		case <-ctx.Done():
			return false
		default:
		}

		select {
		case semaphore <- struct{}{}:
		case <-ctx.Done():
			return false
		}
		result := s.prober.Check(ctx, backend)
		<-semaphore

		cycle++
		state := Snapshot{
			BackendID:        backend.ID,
			Name:             backend.Name,
			FailureThreshold: backend.FailureThreshold,
		}
		if s.tracker != nil {
			state = s.tracker.Record(result)
		}
		if s.onProbe != nil {
			s.onProbe(ProbeEvent{
				Backend: backend,
				Checked: CheckedBackend{Backend: backend, Result: result, State: state},
				Cycle:   cycle,
			})
		}
		return true
	}

	// Immediate probe on startup, regardless of interval.
	if !probeOnce() {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Start-to-start scheduling is approximated by ticking on the
			// interval and never overlapping: probeOnce runs synchronously,
			// so the next probe starts immediately after completion when a
			// probe overruns its interval (no catch-up burst, no overlap).
			if !probeOnce() {
				return
			}
			// If the context was canceled during the probe, exit promptly
			// instead of waiting for another tick.
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}
}
