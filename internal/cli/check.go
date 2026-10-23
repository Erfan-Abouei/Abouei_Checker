package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/app"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/term"
)

// outputWrittenError marks an error for which the command already wrote its
// stdout payload (text results or a JSON envelope). reportError must not write
// a second JSON document in that case; it only writes the stderr Error line
// and maps the exit code.
type outputWrittenError struct {
	err *app.Error
}

func (e *outputWrittenError) Error() string {
	if e == nil || e.err == nil {
		return "command failed"
	}
	return e.err.Error()
}

// Unwrap exposes the classified application error for exit-code mapping.
func (e *outputWrittenError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func asOutputWritten(err error) (*app.Error, bool) {
	var written *outputWrittenError
	if errors.As(err, &written) && written != nil && written.err != nil {
		return written.err, true
	}
	return nil, false
}

func newCheckCommand(state *commandState, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "check [BACKEND_ID...]",
		Short: "Probe each selected backend once and exit",
		Long: `Probe each selected backend once and report health.

With no IDs, all configured backends are checked. An unknown backend ID is a
usage error and no request is performed. Each backend is probed concurrently
under runtime.max_concurrent_probes with its configured timeout.

Results persist atomically with incident transitions and notification intent
in SQLite. Reaching a backend's failure threshold opens an incident and
enqueues a Down event; meeting its recovery threshold closes it and enqueues
Recovery. Due notifications are attempted once; a failed attempt exits 4 with
the event left queued. Exit 1 reports at least one non-healthy probe, even
below the configured failure threshold.`,
		Example: `  abouei-checker check
  abouei-checker check api web
  abouei-checker check --config ./abouei.yaml --output json`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if state.application == nil {
				return app.NewError(app.CategoryInternal, app.CodeInternal, "application is not initialized")
			}
			if _, err := state.application.LoadConfig(); err != nil {
				return err
			}
			results, summary, err := state.application.Check(cmd.Context(), args)
			if err != nil {
				// Configuration/usage/internal failures: no result payload was
				// written, so reportError renders the standard error envelope.
				return err
			}

			if state.output == outputJSON {
				payload := buildCheckPayload(results, summary)
				if writeErr := writeJSON(stdout, payload); writeErr != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write JSON check output", writeErr)
				}
			} else {
				if writeErr := writeCheckText(stdout, results, summary); writeErr != nil {
					return app.WrapError(app.CategoryInternal, app.CodeInternal, "failed to write check output", writeErr)
				}
			}

			// Precedence: notification failure (4) outranks unhealthy (1).
			if summary.NotificationsFailed > 0 {
				return &outputWrittenError{err: app.WrapError(
					app.CategoryNotification,
					app.CodeNotificationFailed,
					fmt.Sprintf(
						"%d of %d notifications failed; events remain queued",
						summary.NotificationsFailed, summary.NotificationsAttempted,
					),
					fmt.Errorf(
						"%d of %d notifications failed",
						summary.NotificationsFailed, summary.NotificationsAttempted,
					),
				)}
			}
			if summary.Canceled > 0 {
				return &outputWrittenError{err: app.WrapError(
					app.CategoryInternal,
					app.CodeInternal,
					"check canceled",
					context.Canceled,
				)}
			}
			if summary.Unhealthy > 0 {
				return &outputWrittenError{err: app.WrapError(
					app.CategoryUnhealthy,
					app.CodeInternal,
					checkUnhealthyMessage(summary),
					fmt.Errorf("%d of %d backends unhealthy", summary.Unhealthy, summary.Total),
				)}
			}
			return nil
		},
	}
}

func checkUnhealthyMessage(summary app.CheckSummary) string {
	if summary.Total == 1 {
		return "backend is unhealthy"
	}
	return fmt.Sprintf("%d of %d backends are unhealthy", summary.Unhealthy, summary.Total)
}

func writeCheckText(w io.Writer, results []app.CheckResult, summary app.CheckSummary) error {
	style := term.ForWriter(w)
	if !style.Enabled {
		for _, result := range results {
			line := formatCheckLine(result)
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
		summaryLine := fmt.Sprintf(
			"Summary: %d healthy, %d unhealthy, %d canceled of %d backends; %d incidents opened, %d closed; %d notifications attempted, %d failed",
			summary.Healthy, summary.Unhealthy, summary.Canceled, summary.Total,
			summary.IncidentsOpened, summary.IncidentsClosed,
			summary.NotificationsAttempted, summary.NotificationsFailed,
		)
		if _, err := fmt.Fprintln(w, summaryLine); err != nil {
			return err
		}
		return nil
	}
	for _, result := range results {
		if _, err := fmt.Fprintln(w, formatCheckLineStyled(result, style)); err != nil {
			return err
		}
	}
	rest := fmt.Sprintf(
		"%d healthy, %d unhealthy, %d canceled of %d backends; %d incidents opened, %d closed; %d notifications attempted, %d failed",
		summary.Healthy, summary.Unhealthy, summary.Canceled, summary.Total,
		summary.IncidentsOpened, summary.IncidentsClosed,
		summary.NotificationsAttempted, summary.NotificationsFailed,
	)
	if _, err := fmt.Fprintln(w, style.SummaryLine(rest)); err != nil {
		return err
	}
	if summary.NotificationsFailed > 0 {
		if _, err := fmt.Fprintln(w, style.Danger("Warning: some webhook deliveries failed; events remain queued for retry.")); err != nil {
			return err
		}
	}
	return nil
}

// formatCheckLineStyled adds restrained color and width-aware truncation for
// human terminals. Plain output (pipes/tests) always uses formatCheckLine so
// bytes stay identical.
func formatCheckLineStyled(result app.CheckResult, style term.Style) string {
	id := style.Bold(style.Fit(result.BackendID, 48))
	incident := style.IncidentTag(result.IncidentStatus)
	if result.Outcome == "healthy" {
		status := style.Status("healthy")
		if result.HTTPStatus != 0 {
			return fmt.Sprintf("%s: %s (%d in %s)%s",
				id, status, result.HTTPStatus,
				style.Muted(result.Duration.Truncate(time.Millisecond).String()), incident)
		}
		return fmt.Sprintf("%s: %s (in %s)%s",
			id, status, style.Muted(result.Duration.Truncate(time.Millisecond).String()), incident)
	}
	status := style.Status(result.Outcome)
	var detail strings.Builder
	if result.HTTPStatus != 0 {
		fmt.Fprintf(&detail, "%d", result.HTTPStatus)
	} else {
		detail.WriteString("no response")
	}
	detail.WriteString(" in ")
	detail.WriteString(result.Duration.Truncate(time.Millisecond).String())
	if result.Error != "" {
		detail.WriteString(": ")
		detail.WriteString(style.Fit(result.Error, 120))
	}
	fmt.Fprintf(&detail, " [streak %d/%d]", result.ConsecutiveFailures, result.FailureThreshold)
	return fmt.Sprintf("%s: %s (%s)%s", id, status, style.Muted(detail.String()), incident)
}

func formatCheckLine(result app.CheckResult) string {
	status := result.Outcome
	incident := ""
	if result.IncidentStatus != "" && result.IncidentStatus != "none" {
		incident = fmt.Sprintf(" [incident %s]", result.IncidentStatus)
	}
	if result.Outcome == "healthy" {
		if result.HTTPStatus != 0 {
			return fmt.Sprintf("%s: healthy (%d in %s)%s", result.BackendID, result.HTTPStatus, result.Duration.Truncate(time.Millisecond), incident)
		}
		return fmt.Sprintf("%s: healthy (in %s)%s", result.BackendID, result.Duration.Truncate(time.Millisecond), incident)
	}
	var detail strings.Builder
	if result.HTTPStatus != 0 {
		fmt.Fprintf(&detail, "%d", result.HTTPStatus)
	} else {
		detail.WriteString("no response")
	}
	detail.WriteString(" in ")
	detail.WriteString(result.Duration.Truncate(time.Millisecond).String())
	if result.Error != "" {
		detail.WriteString(": ")
		detail.WriteString(result.Error)
	}
	fmt.Fprintf(&detail, " [streak %d/%d]", result.ConsecutiveFailures, result.FailureThreshold)
	detail.WriteString(incident)
	return fmt.Sprintf("%s: %s (%s)", result.BackendID, status, detail.String())
}

func buildCheckPayload(results []app.CheckResult, summary app.CheckSummary) envelope {
	status := "success"
	if summary.Unhealthy > 0 {
		status = "unhealthy"
	} else if summary.Canceled > 0 {
		status = "error"
	}
	converted := make([]checkResultData, 0, len(results))
	for _, result := range results {
		converted = append(converted, checkResultData{
			BackendID:           result.BackendID,
			Name:                result.Name,
			Status:              result.Outcome,
			HTTPStatus:          result.HTTPStatus,
			DurationMs:          result.Duration.Milliseconds(),
			ObservedAt:          result.ObservedAt.UTC().Format(time.RFC3339),
			FailureReason:       result.FailureReason,
			Error:               result.Error,
			ConsecutiveFailures: result.ConsecutiveFailures,
			FailureThreshold:    result.FailureThreshold,
			ThresholdReached:    result.ThresholdReached,
			IncidentStatus:      result.IncidentStatus,
		})
	}
	if converted == nil {
		converted = []checkResultData{}
	}
	return envelope{
		SchemaVersion: "1",
		Command:       "check",
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Status:        status,
		Data: checkSummaryData{
			Summary: checkSummaryFields{
				Total:                  summary.Total,
				Healthy:                summary.Healthy,
				Unhealthy:              summary.Unhealthy,
				Canceled:               summary.Canceled,
				IncidentsOpened:        summary.IncidentsOpened,
				IncidentsClosed:        summary.IncidentsClosed,
				NotificationsAttempted: summary.NotificationsAttempted,
				NotificationsFailed:    summary.NotificationsFailed,
			},
			Results: converted,
		},
		Error: nil,
	}
}

type checkSummaryData struct {
	Summary checkSummaryFields `json:"summary"`
	Results []checkResultData  `json:"results"`
}

type checkSummaryFields struct {
	Total                  int `json:"total"`
	Healthy                int `json:"healthy"`
	Unhealthy              int `json:"unhealthy"`
	Canceled               int `json:"canceled,omitempty"`
	IncidentsOpened        int `json:"incidents_opened"`
	IncidentsClosed        int `json:"incidents_closed"`
	NotificationsAttempted int `json:"notifications_attempted"`
	NotificationsFailed    int `json:"notifications_failed"`
}

type checkResultData struct {
	BackendID           string `json:"backend_id"`
	Name                string `json:"name"`
	Status              string `json:"status"`
	HTTPStatus          int    `json:"http_status"`
	DurationMs          int64  `json:"duration_ms"`
	ObservedAt          string `json:"observed_at"`
	FailureReason       string `json:"failure_reason,omitempty"`
	Error               string `json:"error,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	FailureThreshold    int    `json:"failure_threshold"`
	ThresholdReached    bool   `json:"threshold_reached"`
	IncidentStatus      string `json:"incident_status"`
}
