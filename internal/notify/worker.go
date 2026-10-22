package notify

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ClaimedEvent is one outbox row claimed for delivery. Payload is a snapshot
// taken at event creation; it contains no secrets.
type ClaimedEvent struct {
	// ID is the stable event identity, also sent as Idempotency-Key.
	ID string
	// IncidentID orders the event within its incident.
	IncidentID string
	// BackendID identifies the backend for logging.
	BackendID string
	// EventType is incident.opened, incident.reminder, or incident.recovered.
	EventType string
	// Sequence preserves per-incident order.
	Sequence int
	// AttemptCount includes the current attempt (incremented at claim time).
	AttemptCount int
	// Payload is the compact JSON snapshot to deliver.
	Payload []byte
}

// Outbox is the narrow persistence boundary consumed by the worker. It is
// implemented by internal/store. Attempts are the only writers; delivery
// never runs inside a claim transaction.
type Outbox interface {
	// ClaimDueEvent atomically claims the oldest due event whose earlier
	// incident sequences are all terminal, preserving per-incident order.
	// It returns sql.ErrNoRows when nothing is due.
	ClaimDueEvent(ctx context.Context, now time.Time) (*ClaimedEvent, error)
	// MarkDelivered records a successful delivery.
	MarkDelivered(ctx context.Context, id string, now time.Time) error
	// MarkFailed records a retryable failure with durable retry scheduling.
	MarkFailed(ctx context.Context, id string, nextAttempt time.Time, lastError string) error
	// MarkDead records a terminal failure. Dead events never block later
	// sequences in the same incident.
	MarkDead(ctx context.Context, id string, lastError string) error
}

// SenderFunc delivers one payload; it matches (*Sender).Send for tests.
type SenderFunc func(ctx context.Context, cfg WebhookConfig, eventID string, payload []byte) AttemptResult

// Worker delivers outbox events with bounded retries. Use ProcessDue for
// one-shot commands and Run for continuous monitoring.
type Worker struct {
	outbox  Outbox
	send    SenderFunc
	webhook WebhookConfig
	retry   RetryConfig
	poke    chan struct{}
}

// WorkerOptions configures a Worker.
type WorkerOptions struct {
	Outbox  Outbox
	Send    SenderFunc
	Webhook WebhookConfig
	Retry   RetryConfig
}

// NewWorker builds a Worker. Send defaults to a live Sender when nil.
func NewWorker(options WorkerOptions) *Worker {
	send := options.Send
	if send == nil {
		sender := NewSender()
		send = sender.Send
	}
	return &Worker{
		outbox:  options.Outbox,
		send:    send,
		webhook: options.Webhook,
		retry:   options.Retry,
		poke:    make(chan struct{}, 1),
	}
}

// Poke wakes a running worker so freshly enqueued events are attempted
// promptly instead of waiting for the idle poll.
func (w *Worker) Poke() {
	if w == nil {
		return
	}
	select {
	case w.poke <- struct{}{}:
	default:
	}
}

// ProcessDue claims and attempts every currently-due event once and returns
// the attempt and failure counts. Failed attempts remain durable for later
// retry; a failed process does not roll back incident state. It stops early
// on context cancellation.
func (w *Worker) ProcessDue(ctx context.Context) (attempted, failed int, err error) {
	if w == nil || w.outbox == nil {
		return 0, 0, errors.New("notify: worker has no outbox")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if ctx.Err() != nil {
			return attempted, failed, ctx.Err()
		}
		now := time.Now().UTC()
		claimed, claimErr := w.outbox.ClaimDueEvent(ctx, now)
		if claimErr != nil {
			if errors.Is(claimErr, sql.ErrNoRows) {
				return attempted, failed, nil
			}
			return attempted, failed, claimErr
		}
		attempted++
		if w.attempt(ctx, claimed) {
			failed++
		}
	}
}

// attempt delivers one claimed event and persists the terminal or retryable
// outcome. It reports whether the attempt failed (retryably or terminally).
func (w *Worker) attempt(ctx context.Context, claimed *ClaimedEvent) (failed bool) {
	result := w.send(ctx, w.webhook, claimed.ID, claimed.Payload)
	now := time.Now().UTC()
	switch Classify(result.StatusCode, result.Err) {
	case VerdictDelivered:
		if err := w.outbox.MarkDelivered(ctx, claimed.ID, now); err != nil {
			// The receiver may already have committed: the next process
			// retries with the same event ID and the consumer deduplicates.
			// Record the failure so the event stays due rather than stuck.
			_ = w.outbox.MarkFailed(ctx, claimed.ID, now.Add(w.retry.InitialBackoff), BoundError("delivery status not recorded"))
			return true
		}
		return false
	case VerdictPermanent:
		summary := BoundError(SafeSummary(result.StatusCode, result.Err, w.webhook.Timeout))
		if err := w.outbox.MarkDead(ctx, claimed.ID, summary); err != nil {
			return true
		}
		return true
	default:
		summary := BoundError(SafeSummary(result.StatusCode, result.Err, w.webhook.Timeout))
		if claimed.AttemptCount >= w.retry.MaxAttempts {
			if err := w.outbox.MarkDead(ctx, claimed.ID, summary); err != nil {
				return true
			}
			return true
		}
		delay := BackoffDelay(claimed.AttemptCount, w.retry.InitialBackoff, w.retry.MaxBackoff, nil)
		if retryAfter, ok := ParseRetryAfter(result.RetryAfter, now); ok {
			if retryAfter > w.retry.MaxBackoff {
				retryAfter = w.retry.MaxBackoff
			}
			if retryAfter > delay {
				delay = retryAfter
			}
		}
		if err := w.outbox.MarkFailed(ctx, claimed.ID, now.Add(delay), summary); err != nil {
			return true
		}
		return true
	}
}

// Run delivers due events until ctx is canceled, polling when idle and
// waking promptly on Poke. It returns nil on graceful shutdown.
func (w *Worker) Run(ctx context.Context) error {
	if w == nil {
		return errors.New("notify: worker is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		case <-w.poke:
		}
		_, _, err := w.ProcessDue(ctx)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			// Outbox errors are operational: back off briefly and keep the
			// worker alive rather than crashing monitoring.
			timer.Reset(workerIdlePoll)
			continue
		}
		if ctx.Err() != nil {
			return nil
		}
		timer.Reset(workerIdlePoll)
	}
}
