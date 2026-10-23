package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/app"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/term"
)

func newMonitorCommand(state *commandState, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "monitor",
		Short: "Continuously probe configured backends until shutdown",
		Long: `Probe all configured backends on their own intervals until SIGINT or SIGTERM.

On startup every backend is probed immediately, then each backend is probed on
its configured interval. Probes for the same backend never overlap;
independent backends run concurrently under runtime.max_concurrent_probes.

Failure streaks, incidents, and notification intent persist in SQLite across
restarts. Reaching a backend's failure threshold opens an incident and
enqueues a Down event; meeting its recovery threshold closes it and enqueues
Recovery. A background worker delivers due webhook events with bounded
retries. Unhealthy backends do not terminate monitoring.
On SIGINT/SIGTERM the command stops scheduling, waits up to
runtime.shutdown_timeout for in-flight probes, and exits 0. Pending outbox
rows remain durable for the next start.`,
		Example: `  abouei-checker monitor
  abouei-checker monitor --config ./abouei.yaml --output json`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if state.application == nil {
				return app.NewError(app.CategoryInternal, app.CodeInternal, "application is not initialized")
			}
			summary, err := state.application.LoadConfig()
			if err != nil {
				return err
			}

			writer := &lockedWriter{writer: stdout}
			jsonMode := state.output == outputJSON
			style := term.Plain()
			if !jsonMode {
				style = term.ForWriter(stdout)
			}

			if jsonMode {
				started := envelope{
					SchemaVersion: "1",
					Command:       "monitor",
					GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
					Status:        "success",
					Data: map[string]any{
						"kind":          "started",
						"backend_count": len(summary.BackendIDs),
						"backend_ids":   summary.BackendIDs,
					},
					Error: nil,
				}
				if writeErr := writeCompactJSON(writer, started); writeErr != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write monitor output", writeErr)
				}
			} else if style.Enabled {
				if _, writeErr := fmt.Fprintf(writer, "%s %s\n", style.Wordmark(), style.Muted("monitoring "+fmt.Sprintf("%d", len(summary.BackendIDs))+" backends: "+joinIDs(summary.BackendIDs))); writeErr != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write monitor output", writeErr)
				}
			} else {
				if _, writeErr := fmt.Fprintf(writer, "monitoring %d backends: %s\n", len(summary.BackendIDs), joinIDs(summary.BackendIDs)); writeErr != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write monitor output", writeErr)
				}
			}

			monitorErr := state.application.Monitor(cmd.Context(), app.MonitorOptions{
				OnEvent: func(event app.MonitorEvent) {
					if jsonMode {
						_ = writeCompactJSON(writer, monitorProbeEnvelope(event))
					} else if style.Enabled {
						_, _ = fmt.Fprintln(writer, formatMonitorLineStyled(event, style))
					} else {
						_, _ = fmt.Fprintln(writer, formatMonitorLine(event))
					}
				},
			})
			if monitorErr != nil {
				return monitorErr
			}

			if jsonMode {
				shutdown := envelope{
					SchemaVersion: "1",
					Command:       "monitor",
					GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
					Status:        "success",
					Data: map[string]any{
						"kind":          "shutdown",
						"backend_count": len(summary.BackendIDs),
					},
					Error: nil,
				}
				if writeErr := writeCompactJSON(writer, shutdown); writeErr != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write monitor output", writeErr)
				}
			} else if _, writeErr := fmt.Fprintln(writer, style.Muted("monitor stopped")); writeErr != nil {
				return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write monitor output", writeErr)
			}
			return nil
		},
	}
}

func incidentStatusOrNone(status string) string {
	if status == "" {
		return "none"
	}
	return status
}

func joinIDs(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	joined := ids[0]
	for _, id := range ids[1:] {
		joined += ", " + id
	}
	return joined
}

func formatMonitorLine(event app.MonitorEvent) string {
	incident := ""
	if event.IncidentStatus != "" && event.IncidentStatus != "none" {
		incident = fmt.Sprintf(" [incident %s]", event.IncidentStatus)
	}
	if event.Healthy {
		if event.HTTPStatus != 0 {
			return fmt.Sprintf("%s: healthy (%d in %dms) [streak %d/%d]%s",
				event.BackendID, event.HTTPStatus, event.DurationMs,
				event.ConsecutiveFailures, event.FailureThreshold, incident)
		}
		return fmt.Sprintf("%s: healthy (in %dms)%s", event.BackendID, event.DurationMs, incident)
	}
	detail := event.Error
	if detail == "" {
		detail = event.Outcome
	}
	status := event.Outcome
	if event.HTTPStatus != 0 {
		return fmt.Sprintf("%s: %s (%d in %dms): %s [streak %d/%d]%s",
			event.BackendID, status, event.HTTPStatus, event.DurationMs,
			detail, event.ConsecutiveFailures, event.FailureThreshold, incident)
	}
	return fmt.Sprintf("%s: %s: %s [streak %d/%d]%s",
		event.BackendID, status, detail,
		event.ConsecutiveFailures, event.FailureThreshold, incident)
}

func formatMonitorLineStyled(event app.MonitorEvent, style term.Style) string {
	id := style.Bold(style.Fit(event.BackendID, 48))
	incident := style.IncidentTag(event.IncidentStatus)
	if event.IncidentStatus == "none" {
		incident = ""
	}
	if event.Healthy {
		status := style.Status("healthy")
		if event.HTTPStatus != 0 {
			return fmt.Sprintf("%s: %s (%d in %dms) [streak %d/%d]%s",
				id, status, event.HTTPStatus, event.DurationMs,
				event.ConsecutiveFailures, event.FailureThreshold, incident)
		}
		return fmt.Sprintf("%s: %s (in %dms)%s", id, status, event.DurationMs, incident)
	}
	detail := style.Fit(event.Error, 120)
	if detail == "" {
		detail = event.Outcome
	}
	status := style.Status(event.Outcome)
	if event.HTTPStatus != 0 {
		return fmt.Sprintf("%s: %s (%d in %dms): %s [streak %d/%d]%s",
			id, status, event.HTTPStatus, event.DurationMs,
			detail, event.ConsecutiveFailures, event.FailureThreshold, incident)
	}
	return fmt.Sprintf("%s: %s: %s [streak %d/%d]%s",
		id, status, detail,
		event.ConsecutiveFailures, event.FailureThreshold, incident)
}

func monitorProbeEnvelope(event app.MonitorEvent) envelope {
	status := "success"
	if !event.Healthy {
		status = "unhealthy"
	}
	data := map[string]any{
		"kind":                 "probe",
		"backend_id":           event.BackendID,
		"name":                 event.Name,
		"healthy":              event.Healthy,
		"outcome":              event.Outcome,
		"http_status":          event.HTTPStatus,
		"duration_ms":          event.DurationMs,
		"observed_at":          event.ObservedAt.UTC().Format(time.RFC3339),
		"consecutive_failures": event.ConsecutiveFailures,
		"failure_threshold":    event.FailureThreshold,
		"threshold_reached":    event.ThresholdReached,
		"incident_status":      incidentStatusOrNone(event.IncidentStatus),
		"cycle":                event.Cycle,
	}
	if event.FailureReason != "" {
		data["failure_reason"] = event.FailureReason
	}
	if event.Error != "" {
		data["error"] = event.Error
	}
	return envelope{
		SchemaVersion: "1",
		Command:       "monitor",
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Status:        status,
		Data:          data,
		Error:         nil,
	}
}

type lockedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *lockedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(data)
}

func writeCompactJSON(w io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode JSON output: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := w.Write(encoded); err != nil {
		return fmt.Errorf("write JSON output: %w", err)
	}
	return nil
}
