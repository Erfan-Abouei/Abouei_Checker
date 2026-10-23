package app

import (
	"context"
	"fmt"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/incident"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/monitor"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/notify"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/probe"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/store"
)

// CheckResult is the application-facing outcome of one backend probe with its
// persisted threshold and incident snapshot. It uses only plain types so the
// CLI can render results without importing the probe, monitor, or store
// packages.
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
	// ConsecutiveFailures is the persisted streak after recording the result.
	ConsecutiveFailures int
	// FailureThreshold is the configured consecutive-failure threshold.
	FailureThreshold int
	// ThresholdReached is true once the streak reaches the threshold.
	ThresholdReached bool
	// IncidentStatus is one of none, open, still_open, or recovered.
	IncidentStatus string
}

// CheckSummary aggregates one-shot outcomes with durable incident and
// delivery counts.
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
	// IncidentsOpened counts incidents opened by this invocation.
	IncidentsOpened int
	// IncidentsClosed counts incidents closed by this invocation.
	IncidentsClosed int
	// NotificationsAttempted counts webhook attempts made by this invocation.
	NotificationsAttempted int
	// NotificationsFailed counts attempted deliveries that did not succeed.
	NotificationsFailed int
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
// runtime.max_concurrent_probes, and persists each result with its incident
// transition and notification intent in a per-backend transaction. It then
// attempts every due notification once. Configuration must be loaded with
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

	db, err := a.OpenStore()
	if err != nil {
		return nil, CheckSummary{}, err
	}
	defer func() { _ = db.Close() }()

	if err := a.reconcileStore(ctx, db); err != nil {
		return nil, CheckSummary{}, err
	}

	// Best-effort retention on startup: expired terminal rows are removed
	// before new results land. Failures only log; see runRetention.
	a.runRetention(ctx, db)

	prober := probe.New()
	defer prober.CloseIdleConnections()

	checked := monitor.RunOnce(ctx, selected, prober, nil, a.config.Runtime.MaxConcurrentProbes)

	now := time.Now().UTC()
	results := make([]CheckResult, len(checked))
	summary := CheckSummary{Total: len(checked)}
	for i, item := range checked {
		outcome, err := db.ProcessResult(ctx, item.Backend, item.Result, now)
		if err != nil {
			return nil, CheckSummary{}, classifyStoreError(err)
		}
		results[i] = persistedCheckResult(item, outcome)
		switch item.Result.Outcome {
		case probe.OutcomeHealthy:
			summary.Healthy++
		case probe.OutcomeCanceled:
			summary.Canceled++
		default:
			summary.Unhealthy++
		}
		switch outcome.Transition {
		case incident.TransitionOpened:
			summary.IncidentsOpened++
		case incident.TransitionRecovered:
			summary.IncidentsClosed++
		}

		a.logCheckResult(ctx, item.Result, outcome)
	}

	attempted, failed, err := a.deliverDue(ctx, db)
	if err != nil {
		return results, summary, err
	}
	summary.NotificationsAttempted = attempted
	summary.NotificationsFailed = failed

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

// deliverDue attempts every currently-due notification once. Delivery
// failures stay durable and are reported; incident state is never rolled
// back because of them.
func (a *App) deliverDue(ctx context.Context, db *store.Store) (attempted, failed int, err error) {
	if a == nil || a.config == nil || db == nil {
		return 0, 0, NewError(CategoryInternal, CodeInternal, "configuration is not loaded")
	}
	worker := a.newWorker(db)
	return worker.ProcessDue(ctx)
}

// newWorker builds the notification worker for the loaded configuration.
// Header values are copied so later caller mutation cannot race delivery.
func (a *App) newWorker(db *store.Store) *notify.Worker {
	webhook := a.config.Notifications.Webhook
	headers := append([]config.Header(nil), webhook.Headers...)
	return notify.NewWorker(notify.WorkerOptions{
		Outbox: db,
		Webhook: notify.WebhookConfig{
			URL:     webhook.URL,
			Headers: headers,
			Timeout: webhook.Timeout,
		},
		Retry: notify.RetryConfigFrom(a.config.Notifications.Retry),
	})
}

func persistedCheckResult(item monitor.CheckedBackend, outcome store.ProcessOutcome) CheckResult {
	status := store.IncidentNone
	if !outcome.Skipped {
		status = outcome.IncidentStatus
	}
	failures := outcome.Failures
	threshold := outcome.FailureThreshold
	thresholdReached := threshold > 0 && failures >= threshold
	return CheckResult{
		BackendID:           item.Result.BackendID,
		Name:                item.Result.Name,
		Outcome:             string(item.Result.Outcome),
		HTTPStatus:          item.Result.HTTPStatus,
		Duration:            item.Result.Duration,
		ObservedAt:          item.Result.ObservedAt,
		FailureReason:       string(item.Result.FailureReason),
		Error:               item.Result.Error,
		BodyMatched:         item.Result.BodyMatched,
		ConsecutiveFailures: failures,
		FailureThreshold:    threshold,
		ThresholdReached:    thresholdReached,
		IncidentStatus:      status,
	}
}

func (a *App) logCheckResult(ctx context.Context, result probe.Result, outcome store.ProcessOutcome) {
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
	if !outcome.Skipped {
		attrs = append(attrs, "consecutive_failures", outcome.Failures)
		attrs = append(attrs, "threshold_reached", outcome.FailureThreshold > 0 && outcome.Failures >= outcome.FailureThreshold)
	}
	if outcome.IncidentID != "" {
		attrs = append(attrs, "incident_id", outcome.IncidentID)
	}

	switch result.Outcome {
	case probe.OutcomeHealthy:
		a.logger.InfoContext(ctx, "backend healthy", attrs...)
	case probe.OutcomeCanceled:
		a.logger.WarnContext(ctx, "backend probe canceled", attrs...)
	default:
		a.logger.WarnContext(ctx, "backend unhealthy", attrs...)
	}
}
