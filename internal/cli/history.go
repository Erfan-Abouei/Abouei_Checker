package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/app"
)

func newHistoryCommand(state *commandState, stdout io.Writer) *cobra.Command {
	var backendFilter string
	var sinceRaw string
	var limit int

	command := &cobra.Command{
		Use:   "history",
		Short: "Show persisted observations and incident events",
		Long: `Read persisted probe observations and incident notification events.

history never probes an endpoint and never mutates the database: it opens
SQLite read-only and renders the newest rows first with a deterministic
order, so equal timestamps never shuffle pagination. --since accepts an
RFC3339 timestamp or a Go duration interpreted backwards from now (for
example "24h"). --limit bounds each list to 1..1000 rows and defaults to 50;
"has_more" reports whether either list was truncated. Empty history is a
valid result, not an error.`,
		Example: `  abouei-checker history
  abouei-checker history --backend api --limit 20
  abouei-checker history --since 24h
  abouei-checker history --since 2026-10-01T00:00:00Z --output json`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if state.application == nil {
				return app.NewError(app.CategoryInternal, app.CodeInternal, "application is not initialized")
			}
			if _, err := state.application.LoadConfig(); err != nil {
				return err
			}
			if limit < 1 || limit > app.MaxHistoryLimit {
				return app.NewUsageError(
					fmt.Sprintf("invalid --limit %d: expected 1..%d", limit, app.MaxHistoryLimit),
				)
			}
			var since *time.Time
			if sinceRaw != "" {
				parsed, err := parseHistorySince(sinceRaw, time.Now().UTC())
				if err != nil {
					return err
				}
				since = &parsed
			}
			report, err := state.application.History(cmd.Context(), app.HistoryOptions{
				BackendID: backendFilter,
				Since:     since,
				Limit:     limit,
			})
			if err != nil {
				return err
			}

			if state.output == outputJSON {
				if writeErr := writeJSON(stdout, buildHistoryPayload(report)); writeErr != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write JSON history output", writeErr)
				}
			} else {
				if writeErr := writeHistoryText(stdout, report); writeErr != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write history output", writeErr)
				}
			}
			state.application.Logger().DebugContext(cmd.Context(), "history command completed",
				"component", "cli",
				"command", "history",
				"operation", "history",
				"observation_count", len(report.Observations),
				"event_count", len(report.Events),
			)
			return nil
		},
	}

	command.Flags().StringVar(&backendFilter, "backend", "", "filter by exact backend ID")
	command.Flags().StringVar(&sinceRaw, "since", "", "RFC3339 timestamp or Go duration backwards from now (for example 24h)")
	command.Flags().IntVar(&limit, "limit", app.DefaultHistoryLimit, "maximum rows per list (1..1000)")
	return command
}

// parseHistorySince accepts an RFC3339 instant or a Go duration meaning
// "that long ago from now". Anything else is a usage error.
func parseHistorySince(raw string, now time.Time) (time.Time, error) {
	if instant, err := time.Parse(time.RFC3339, raw); err == nil {
		return instant.UTC(), nil
	}
	if duration, err := time.ParseDuration(raw); err == nil {
		if duration < 0 {
			return time.Time{}, app.NewUsageError(
				fmt.Sprintf("invalid --since %q: duration must not be negative", raw),
			)
		}
		return now.Add(-duration).UTC(), nil
	}
	return time.Time{}, app.NewUsageError(
		fmt.Sprintf("invalid --since %q: expected an RFC3339 timestamp or a Go duration like 24h", raw),
	)
}

func writeHistoryText(w io.Writer, report app.HistoryReport) error {
	if _, err := fmt.Fprintln(w, "Observations:"); err != nil {
		return err
	}
	for _, observation := range report.Observations {
		if _, err := fmt.Fprintln(w, "  "+formatHistoryObservation(observation)); err != nil {
			return err
		}
	}
	if len(report.Observations) == 0 {
		if _, err := fmt.Fprintln(w, "  (none)"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w, "Incidents:"); err != nil {
		return err
	}
	for _, event := range report.Events {
		if _, err := fmt.Fprintln(w, "  "+formatHistoryEvent(event)); err != nil {
			return err
		}
	}
	if len(report.Events) == 0 {
		if _, err := fmt.Fprintln(w, "  (none)"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "has_more: %v\n", report.HasMore); err != nil {
		return err
	}
	return nil
}

func formatHistoryObservation(observation app.HistoryObservation) string {
	status := "no status"
	if observation.StatusCode != nil {
		status = fmt.Sprintf("status %d", *observation.StatusCode)
	}
	detail := observation.Outcome
	if observation.FailureReason != nil && *observation.FailureReason != "" {
		detail = observation.Outcome + "/" + *observation.FailureReason
	}
	if observation.Error != nil && *observation.Error != "" {
		return fmt.Sprintf("%s %s %s (%s): %s",
			observation.ObservedAt.UTC().Format(time.RFC3339),
			observation.BackendID, detail, status, *observation.Error)
	}
	return fmt.Sprintf("%s %s %s (%s)",
		observation.ObservedAt.UTC().Format(time.RFC3339),
		observation.BackendID, detail, status)
}

func formatHistoryEvent(event app.HistoryIncidentEvent) string {
	return fmt.Sprintf("%s %s %s seq=%d status=%s incident=%s",
		event.OccurredAt.UTC().Format(time.RFC3339),
		event.BackendID, event.EventType, event.Sequence, event.Status, event.IncidentID)
}

func buildHistoryPayload(report app.HistoryReport) envelope {
	observations := make([]historyObservationData, 0, len(report.Observations))
	for _, observation := range report.Observations {
		entry := historyObservationData{
			BackendID:  observation.BackendID,
			ObservedAt: observation.ObservedAt.UTC().Format(time.RFC3339),
			Outcome:    observation.Outcome,
			DurationMs: observation.DurationMs,
		}
		if observation.StatusCode != nil {
			entry.StatusCode = observation.StatusCode
		}
		if observation.FailureReason != nil {
			entry.FailureReason = observation.FailureReason
		}
		if observation.Error != nil {
			entry.Error = observation.Error
		}
		observations = append(observations, entry)
	}
	events := make([]historyIncidentData, 0, len(report.Events))
	for _, event := range report.Events {
		events = append(events, historyIncidentData{
			IncidentID: event.IncidentID,
			BackendID:  event.BackendID,
			EventType:  event.EventType,
			Sequence:   event.Sequence,
			Status:     event.Status,
			OccurredAt: event.OccurredAt.UTC().Format(time.RFC3339),
		})
	}
	return envelope{
		SchemaVersion: "1",
		Command:       "history",
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Status:        "success",
		Data: historyData{
			Observations: observations,
			Incidents:    events,
			HasMore:      report.HasMore,
		},
		Error: nil,
	}
}

type historyData struct {
	Observations []historyObservationData `json:"observations"`
	Incidents    []historyIncidentData    `json:"incidents"`
	HasMore      bool                     `json:"has_more"`
}

type historyObservationData struct {
	BackendID     string  `json:"backend_id"`
	ObservedAt    string  `json:"observed_at"`
	Outcome       string  `json:"outcome"`
	StatusCode    *int    `json:"status_code,omitempty"`
	DurationMs    int64   `json:"duration_ms"`
	FailureReason *string `json:"failure_reason,omitempty"`
	Error         *string `json:"error,omitempty"`
}

type historyIncidentData struct {
	IncidentID string `json:"incident_id"`
	BackendID  string `json:"backend_id"`
	EventType  string `json:"event_type"`
	Sequence   int    `json:"sequence"`
	Status     string `json:"status"`
	OccurredAt string `json:"occurred_at"`
}
