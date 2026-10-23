package app

import (
	"context"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/store"
)

// DefaultHistoryLimit is the history row limit per list when --limit is
// omitted. MaxHistoryLimit is the documented upper bound.
const (
	DefaultHistoryLimit = 50
	MaxHistoryLimit     = 1000
)

// HistoryObservation is one persisted probe result for history output.
type HistoryObservation struct {
	BackendID     string
	ObservedAt    time.Time
	Outcome       string
	StatusCode    *int
	DurationMs    int64
	FailureReason *string
	Error         *string
}

// HistoryIncidentEvent is one durable notification outbox row for history
// output. It carries event identity and timing only.
type HistoryIncidentEvent struct {
	IncidentID string
	BackendID  string
	EventType  string
	Sequence   int
	Status     string
	OccurredAt time.Time
}

// HistoryReport is the read-only history snapshot for the history command.
type HistoryReport struct {
	Observations []HistoryObservation
	Events       []HistoryIncidentEvent
	HasMore      bool
	GeneratedAt  time.Time
}

// HistoryOptions bounds a history query. BackendID is an exact match and may
// name a backend that no longer exists; history is retained after removal.
// Since excludes older rows. Limit follows the 1..1000 contract with a
// default of 50; values outside the range are clamped.
type HistoryOptions struct {
	BackendID string
	Since     *time.Time
	Limit     int
}

// History reads persisted observations and incident events without probing
// or mutating state. Configuration must be loaded with LoadConfig before
// calling History. Empty history is a valid result, not an error.
func (a *App) History(ctx context.Context, options HistoryOptions) (HistoryReport, error) {
	if a == nil || a.config == nil {
		return HistoryReport{}, NewError(CategoryInternal, CodeInternal, "configuration is not loaded")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	db, err := a.OpenReadOnlyStore()
	if err != nil {
		return HistoryReport{}, err
	}
	defer func() { _ = db.Close() }()

	page, err := db.QueryHistory(ctx, historyStoreQuery(options))
	if err != nil {
		return HistoryReport{}, classifyStoreError(err)
	}

	report := HistoryReport{
		GeneratedAt:  time.Now().UTC(),
		HasMore:      page.HasMore,
		Observations: make([]HistoryObservation, 0, len(page.Observations)),
		Events:       make([]HistoryIncidentEvent, 0, len(page.Events)),
	}
	for _, row := range page.Observations {
		report.Observations = append(report.Observations, HistoryObservation{
			BackendID:     row.BackendID,
			ObservedAt:    row.ObservedAt.UTC(),
			Outcome:       row.Outcome,
			StatusCode:    row.StatusCode,
			DurationMs:    row.DurationMs,
			FailureReason: row.FailureReason,
			Error:         row.ErrorSummary,
		})
	}
	for _, row := range page.Events {
		report.Events = append(report.Events, HistoryIncidentEvent{
			IncidentID: row.IncidentID,
			BackendID:  row.BackendID,
			EventType:  row.EventType,
			Sequence:   row.Sequence,
			Status:     row.Status,
			OccurredAt: row.OccurredAt.UTC(),
		})
	}
	return report, nil
}

func historyStoreQuery(options HistoryOptions) store.HistoryQuery {
	return store.HistoryQuery{
		BackendID: options.BackendID,
		Since:     options.Since,
		Limit:     normalizeHistoryLimit(options.Limit),
	}
}

// normalizeHistoryLimit applies the documented default and bounds.
func normalizeHistoryLimit(limit int) int {
	if limit <= 0 {
		return DefaultHistoryLimit
	}
	if limit > MaxHistoryLimit {
		return MaxHistoryLimit
	}
	return limit
}
