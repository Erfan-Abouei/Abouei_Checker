package app

import (
	"context"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/monitor"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/probe"
)

// MonitorEvent is the application-facing form of one scheduled probe.
type MonitorEvent struct {
	// BackendID is the stable configured backend identity.
	BackendID string
	// Name is the display name.
	Name string
	// Outcome is one of healthy, unhealthy, error, or canceled.
	Outcome string
	// Healthy is true only for OutcomeHealthy.
	Healthy bool
	// HTTPStatus is the received status code, or 0 when no response exists.
	HTTPStatus int
	// DurationMs is the wall-clock request duration in milliseconds.
	DurationMs int64
	// ObservedAt is the UTC timestamp when the probe started.
	ObservedAt time.Time
	// FailureReason is the sanitized failure category (empty when healthy).
	FailureReason string
	// Error is a safe diagnostic summary (empty when healthy).
	Error string
	// ConsecutiveFailures is the in-memory streak after recording the result.
	ConsecutiveFailures int
	// FailureThreshold is the configured consecutive-failure threshold.
	FailureThreshold int
	// ThresholdReached is true once the streak reaches the threshold.
	ThresholdReached bool
	// Cycle is the per-backend sequence number starting at 1.
	Cycle int
}

// MonitorOptions configures continuous monitoring.
type MonitorOptions struct {
	// OnEvent receives every completed probe. It must be safe for concurrent
	// use and must not block indefinitely.
	OnEvent func(MonitorEvent)
}

// Monitor runs continuous per-backend schedules until ctx is canceled. It
// performs an immediate probe of every backend on startup, never overlaps
// probes for the same backend, bounds global concurrency with
// runtime.max_concurrent_probes, and keeps streaks in memory only. A canceled
// context is a graceful shutdown and returns nil.
func (a *App) Monitor(ctx context.Context, options MonitorOptions) error {
	if a == nil || a.config == nil {
		return NewError(CategoryInternal, CodeInternal, "configuration is not loaded")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	return a.runMonitor(ctx, options)
}

func (a *App) runMonitor(ctx context.Context, options MonitorOptions) error {
	backends := a.config.Backends
	thresholds := make(map[string]int, len(backends))
	names := make(map[string]string, len(backends))
	for _, backend := range backends {
		thresholds[backend.ID] = backend.FailureThreshold
		names[backend.ID] = backend.Name
	}
	tracker := monitor.NewTracker(thresholds, names)

	prober := probe.New()
	defer prober.CloseIdleConnections()

	a.logger.InfoContext(ctx, "monitor started",
		"component", "monitor",
		"command", "monitor",
		"operation", "monitor_start",
		"backend_count", len(backends),
	)

	scheduler := monitor.NewScheduler(monitor.SchedulerOptions{
		Backends:       backends,
		Prober:         prober,
		Tracker:        tracker,
		MaxConcurrency: a.config.Runtime.MaxConcurrentProbes,
		OnProbe: func(event monitor.ProbeEvent) {
			monitorEvent := MonitorEvent{
				BackendID:           event.Checked.Result.BackendID,
				Name:                event.Checked.Result.Name,
				Outcome:             string(event.Checked.Result.Outcome),
				Healthy:             event.Checked.Result.Healthy(),
				HTTPStatus:          event.Checked.Result.HTTPStatus,
				DurationMs:          event.Checked.Result.Duration.Milliseconds(),
				ObservedAt:          event.Checked.Result.ObservedAt,
				FailureReason:       string(event.Checked.Result.FailureReason),
				Error:               event.Checked.Result.Error,
				ConsecutiveFailures: event.Checked.State.ConsecutiveFailures,
				FailureThreshold:    event.Checked.State.FailureThreshold,
				ThresholdReached:    event.Checked.State.ThresholdReached,
				Cycle:               event.Cycle,
			}
			a.logMonitorEvent(ctx, monitorEvent)
			if options.OnEvent != nil {
				options.OnEvent(monitorEvent)
			}
		},
	})

	shutdownTimeout := a.config.Runtime.ShutdownTimeout
	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.Run(ctx)
	}()

	select {
	case <-ctx.Done():
		// Graceful shutdown: wait for in-flight probes up to the configured
		// timeout, then return success. Streak state is in-memory only.
		timer := time.NewTimer(shutdownTimeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		}
		a.logger.InfoContext(context.Background(), "monitor stopped",
			"component", "monitor",
			"command", "monitor",
			"operation", "monitor_stop",
			"backend_count", len(backends),
		)
		return nil
	case <-done:
		return nil
	}
}

func (a *App) logMonitorEvent(ctx context.Context, event MonitorEvent) {
	if a == nil || a.logger == nil {
		return
	}
	attrs := []any{
		"component", "monitor",
		"command", "monitor",
		"operation", "probe_backend",
		"backend_id", event.BackendID,
		"outcome", event.Outcome,
		"duration_ms", event.DurationMs,
		"consecutive_failures", event.ConsecutiveFailures,
		"threshold_reached", event.ThresholdReached,
	}
	if event.HTTPStatus != 0 {
		attrs = append(attrs, "http_status", event.HTTPStatus)
	}
	if event.FailureReason != "" {
		attrs = append(attrs, "failure_reason", event.FailureReason)
	}
	if event.Healthy {
		a.logger.InfoContext(ctx, "backend healthy", attrs...)
		return
	}
	if event.Outcome == string(probe.OutcomeCanceled) {
		a.logger.WarnContext(ctx, "backend probe canceled", attrs...)
		return
	}
	a.logger.WarnContext(ctx, "backend unhealthy", attrs...)
}
