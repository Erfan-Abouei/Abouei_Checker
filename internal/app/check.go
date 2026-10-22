package app

import (
	"context"
	"fmt"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/monitor"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/probe"
)

// CheckResult is the application-facing outcome of one backend probe with its
// in-memory threshold snapshot. It uses only plain types so the CLI can render
// results without importing the probe or monitor packages.
type CheckResult struct {
	// BackendID is the stable configured backend identity.
	BackendID string
	// Name is the display name.
	Name string
	// Outcome is one of healthy, unhealthy, error, or canceled.
	Outcome string
	// HTTPStatus is the received status code, or 0 when no response exists.
	HTTPStatus int
	// Duration is the wall-clock request duration.
	Duration time.Duration
	// ObservedAt is the UTC timestamp when the probe started.
	ObservedAt time.Time
	// FailureReason is the sanitized failure category (empty when healthy).
	FailureReason string
	// Error is a safe diagnostic summary (empty when healthy).
	Error string
	// BodyMatched reports the body-condition outcome. Nil when no matcher is
	// configured or evaluation did not reach the body stage.
	BodyMatched *bool
	// ConsecutiveFailures is the in-memory streak after recording the result.
	ConsecutiveFailures int
	// FailureThreshold is the configured consecutive-failure threshold.
	FailureThreshold int
	// ThresholdReached is true once the streak reaches the threshold.
	ThresholdReached bool
}

// CheckSummary aggregates one-shot outcomes.
type CheckSummary struct {
	// Total is the number of selected backends.
	Total int
	// Healthy counts OutcomeHealthy results.
	Healthy int
	// Unhealthy counts OutcomeUnhealthy and OutcomeError results. Canceled
	// results are reported separately and never counted as healthy.
	Unhealthy int
	// Canceled counts OutcomeCanceled results.
	Canceled int
}

// SelectBackends resolves backendIDs to configured backends in configuration
// order. An empty selection returns every backend. Unknown IDs are usage
// errors and no network request is performed in that case.
func SelectBackends(cfg *config.Config, backendIDs []string) ([]config.Backend, error) {
	if cfg == nil {
		return nil, NewError(CategoryInternal, CodeInternal, "configuration is not loaded")
	}
	if len(backendIDs) == 0 {
		return append([]config.Backend(nil), cfg.Backends...), nil
	}
	byID := make(map[string]config.Backend, len(cfg.Backends))
	for _, backend := range cfg.Backends {
		byID[backend.ID] = backend
	}
	selected := make([]config.Backend, 0, len(backendIDs))
	for _, id := range backendIDs {
		backend, ok := byID[id]
		if !ok {
			return nil, WrapError(
				CategoryUsage,
				CodeBackendNotFound,
				fmt.Sprintf("unknown backend %q", id),
				fmt.Errorf("unknown backend %q", id),
			)
		}
		selected = append(selected, backend)
	}
	return selected, nil
}

// Check executes one probe per selected backend concurrently, bounded by
// runtime.max_concurrent_probes, and returns deterministic configuration-order
// results with in-memory streak state. Configuration must be loaded with
// LoadConfig before calling Check.
func (a *App) Check(ctx context.Context, backendIDs []string) ([]CheckResult, CheckSummary, error) {
	if a == nil || a.config == nil {
		return nil, CheckSummary{}, NewError(CategoryInternal, CodeInternal, "configuration is not loaded")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	selected, err := SelectBackends(a.config, backendIDs)
	if err != nil {
		return nil, CheckSummary{}, err
	}

	thresholds := make(map[string]int, len(selected))
	names := make(map[string]string, len(selected))
	for _, backend := range selected {
		thresholds[backend.ID] = backend.FailureThreshold
		names[backend.ID] = backend.Name
	}
	tracker := monitor.NewTracker(thresholds, names)

	prober := probe.New()
	defer prober.CloseIdleConnections()

	checked := monitor.RunOnce(ctx, selected, prober, tracker, a.config.Runtime.MaxConcurrentProbes)

	results := make([]CheckResult, len(checked))
	summary := CheckSummary{Total: len(checked)}
	for i, item := range checked {
		results[i] = CheckResult{
			BackendID:           item.Result.BackendID,
			Name:                item.Result.Name,
			Outcome:             string(item.Result.Outcome),
			HTTPStatus:          item.Result.HTTPStatus,
			Duration:            item.Result.Duration,
			ObservedAt:          item.Result.ObservedAt,
			FailureReason:       string(item.Result.FailureReason),
			Error:               item.Result.Error,
			BodyMatched:         item.Result.BodyMatched,
			ConsecutiveFailures: item.State.ConsecutiveFailures,
			FailureThreshold:    item.State.FailureThreshold,
			ThresholdReached:    item.State.ThresholdReached,
		}
		switch item.Result.Outcome {
		case probe.OutcomeHealthy:
			summary.Healthy++
		case probe.OutcomeCanceled:
			summary.Canceled++
		default:
			summary.Unhealthy++
		}

		a.logCheckResult(ctx, item.Result, item.State)
	}

	if ctx.Err() != nil && summary.Canceled > 0 && summary.Healthy+summary.Unhealthy == 0 {
		return results, summary, WrapError(
			CategoryInternal,
			CodeInternal,
			"check canceled",
			ctx.Err(),
		)
	}

	return results, summary, nil
}

func (a *App) logCheckResult(ctx context.Context, result probe.Result, state monitor.Snapshot) {
	if a == nil || a.logger == nil {
		return
	}
	attrs := []any{
		"component", "probe",
		"command", "check",
		"operation", "probe_backend",
		"backend_id", result.BackendID,
		"outcome", string(result.Outcome),
		"duration_ms", result.Duration.Milliseconds(),
	}
	if result.HTTPStatus != 0 {
		attrs = append(attrs, "http_status", result.HTTPStatus)
	}
	if result.FailureReason != "" {
		attrs = append(attrs, "failure_reason", string(result.FailureReason))
	}
	attrs = append(attrs, "consecutive_failures", state.ConsecutiveFailures)
	attrs = append(attrs, "threshold_reached", state.ThresholdReached)

	switch result.Outcome {
	case probe.OutcomeHealthy:
		a.logger.InfoContext(ctx, "backend healthy", attrs...)
	case probe.OutcomeCanceled:
		a.logger.WarnContext(ctx, "backend probe canceled", attrs...)
	default:
		a.logger.WarnContext(ctx, "backend unhealthy", attrs...)
	}
}
