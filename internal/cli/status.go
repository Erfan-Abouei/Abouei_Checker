package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/app"
)

func newStatusCommand(state *commandState, stdout io.Writer) *cobra.Command {
	var backendFilter string

	command := &cobra.Command{
		Use:   "status",
		Short: "Show persisted backend and incident state",
		Long: `Read the persisted monitoring state and report per-backend health.

status never probes an endpoint and never mutates the database: it opens
SQLite read-only and renders the last persisted observation, consecutive
failure/recovery streaks, and open incidents. An open incident reports "down";
a backend with no observation reports "unknown"; an open incident for a
backend that is no longer configured reports "orphaned" and stays visible
until the incident is closed by a real recovery observation.

Exit 0 means no open incidents; exit 1 means at least one open incident in
the selected scope.`,
		Example: `  abouei-checker status
  abouei-checker status --backend api
  abouei-checker status --config ./abouei.yaml --output json`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if state.application == nil {
				return app.NewError(app.CategoryInternal, app.CodeInternal, "application is not initialized")
			}
			if _, err := state.application.LoadConfig(); err != nil {
				return err
			}
			report, err := state.application.Status(cmd.Context(), backendFilter)
			if err != nil {
				return err
			}

			if state.output == outputJSON {
				if writeErr := writeJSON(stdout, buildStatusPayload(report)); writeErr != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write JSON status output", writeErr)
				}
			} else {
				if writeErr := writeStatusText(stdout, report); writeErr != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write status output", writeErr)
				}
			}

			if report.OpenIncidents > 0 {
				return &outputWrittenError{err: app.WrapError(
					app.CategoryUnhealthy,
					app.CodeInternal,
					statusUnhealthyMessage(report),
					nil,
				)}
			}
			return nil
		},
	}

	command.Flags().StringVar(&backendFilter, "backend", "", "show only one backend ID (exact match)")
	return command
}

func statusUnhealthyMessage(report app.StatusReport) string {
	if report.OpenIncidents == 1 {
		return "1 open incident"
	}
	return fmt.Sprintf("%d open incidents", report.OpenIncidents)
}

func writeStatusText(w io.Writer, report app.StatusReport) error {
	for _, backend := range report.Backends {
		if _, err := fmt.Fprintln(w, formatStatusLine(backend)); err != nil {
			return err
		}
	}
	summary := fmt.Sprintf(
		"Summary: %d backends, %d open incidents, %d pending notifications, %d failed",
		len(report.Backends), report.OpenIncidents,
		report.PendingNotifications, report.FailedNotifications,
	)
	if _, err := fmt.Fprintln(w, summary); err != nil {
		return err
	}
	return nil
}

func formatStatusLine(backend app.BackendStatus) string {
	observed := "never observed"
	if backend.LastObservedAt != nil {
		observed = backend.LastObservedAt.UTC().Format(time.RFC3339)
	}
	statusCode := "no status"
	if backend.LastStatusCode != nil {
		statusCode = fmt.Sprintf("status %d", *backend.LastStatusCode)
	}
	incident := ""
	if backend.OpenIncidentID != nil && *backend.OpenIncidentID != "" {
		opened := ""
		if backend.OpenedAt != nil {
			opened = fmt.Sprintf(" since %s", backend.OpenedAt.UTC().Format(time.RFC3339))
		}
		incident = fmt.Sprintf(" [incident %s%s]", *backend.OpenIncidentID, opened)
	}
	return fmt.Sprintf("%s: %s (last observed %s, %s, failures %d, successes %d)%s",
		backend.BackendID, backend.Status, observed, statusCode,
		backend.ConsecutiveFailures, backend.ConsecutiveSuccesses, incident)
}

func buildStatusPayload(report app.StatusReport) envelope {
	backends := make([]statusBackendData, 0, len(report.Backends))
	for _, backend := range report.Backends {
		entry := statusBackendData{
			BackendID:            backend.BackendID,
			Name:                 backend.Name,
			Status:               backend.Status,
			ConsecutiveFailures:  backend.ConsecutiveFailures,
			ConsecutiveSuccesses: backend.ConsecutiveSuccesses,
		}
		if backend.LastObservedAt != nil {
			observed := backend.LastObservedAt.UTC().Format(time.RFC3339)
			entry.LastObservedAt = &observed
		}
		if backend.LastStatusCode != nil {
			entry.LastStatusCode = backend.LastStatusCode
		}
		if backend.OpenIncidentID != nil {
			entry.OpenIncidentID = backend.OpenIncidentID
		}
		if backend.OpenedAt != nil {
			opened := backend.OpenedAt.UTC().Format(time.RFC3339)
			entry.OpenedAt = &opened
		}
		backends = append(backends, entry)
	}
	return envelope{
		SchemaVersion: "1",
		Command:       "status",
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Status:        statusEnvelopeStatus(report),
		Data: statusData{
			Backends:             backends,
			OpenIncidents:        report.OpenIncidents,
			PendingNotifications: report.PendingNotifications,
			FailedNotifications:  report.FailedNotifications,
		},
		Error: nil,
	}
}

func statusEnvelopeStatus(report app.StatusReport) string {
	if report.OpenIncidents > 0 {
		return "unhealthy"
	}
	return "success"
}

type statusData struct {
	Backends             []statusBackendData `json:"backends"`
	OpenIncidents        int                 `json:"open_incidents"`
	PendingNotifications int                 `json:"pending_notifications"`
	FailedNotifications  int                 `json:"failed_notifications"`
}

type statusBackendData struct {
	BackendID            string  `json:"backend_id"`
	Name                 string  `json:"name"`
	Status               string  `json:"status"`
	LastObservedAt       *string `json:"last_observed_at,omitempty"`
	LastStatusCode       *int    `json:"last_status_code,omitempty"`
	ConsecutiveFailures  int     `json:"consecutive_failures"`
	ConsecutiveSuccesses int     `json:"consecutive_successes"`
	OpenIncidentID       *string `json:"open_incident_id,omitempty"`
	OpenedAt             *string `json:"opened_at,omitempty"`
}
