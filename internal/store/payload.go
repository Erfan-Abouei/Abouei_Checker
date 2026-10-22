package store

import (
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/incident"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/notify"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/probe"
)

// openedPayload builds the incident.opened snapshot at open time.
func openedPayload(
	incidentID, eventID string,
	backend config.Backend,
	result probe.Result,
	failureCount int,
	firstFailure time.Time,
	now time.Time,
) notify.PayloadInput {
	return notify.PayloadInput{
		EventID:    eventID,
		EventType:  incident.EventOpened,
		Sequence:   1,
		OccurredAt: now,
		Backend:    notify.BackendRef{ID: backend.ID, Name: backend.Name},
		Incident: notify.IncidentSnapshot{
			ID:             incidentID,
			Status:         "down",
			OpenedAt:       notify.FormatTime(now),
			FirstFailureAt: notify.FormatTime(firstFailure),
			ResolvedAt:     nil,
			FailureCount:   failureCount,
			RecoveryCount:  0,
			LastFailureAt:  refTime(now),
			LastObservedAt: notify.FormatTime(observedTime(result, now)),
			LastStatusCode: statusCodeOrNil(result.HTTPStatus),
			LastError:      errorOrNil(result.Error),
			DurationMs:     result.Duration.Milliseconds(),
		},
	}
}

// recoveredPayload builds the incident.recovered snapshot at close time.
func recoveredPayload(
	open *IncidentRow,
	eventID string,
	backend config.Backend,
	result probe.Result,
	recoveryCount int,
	sequence int,
	now time.Time,
) notify.PayloadInput {
	resolved := notify.FormatTime(now)
	return notify.PayloadInput{
		EventID:    eventID,
		EventType:  incident.EventRecovered,
		Sequence:   sequence,
		OccurredAt: now,
		Backend:    notify.BackendRef{ID: backend.ID, Name: backend.Name},
		Incident: notify.IncidentSnapshot{
			ID:             open.ID,
			Status:         "healthy",
			OpenedAt:       notify.FormatTime(open.OpenedAt),
			FirstFailureAt: notify.FormatTime(open.FirstFailureAt),
			ResolvedAt:     &resolved,
			FailureCount:   open.FailureCount,
			RecoveryCount:  recoveryCount,
			LastFailureAt:  notify.FormatOptionalTime(open.LastFailureAt),
			LastObservedAt: notify.FormatTime(observedTime(result, now)),
			LastStatusCode: statusCodeOrNil(result.HTTPStatus),
			LastError:      nil,
			DurationMs:     result.Duration.Milliseconds(),
		},
	}
}

// reminderPayload builds the incident.reminder snapshot with the latest
// observed counts at enqueue time.
func reminderPayload(
	open *IncidentRow,
	eventID string,
	backend config.Backend,
	result probe.Result,
	sequence int,
	now time.Time,
) notify.PayloadInput {
	return notify.PayloadInput{
		EventID:    eventID,
		EventType:  incident.EventReminder,
		Sequence:   sequence,
		OccurredAt: now,
		Backend:    notify.BackendRef{ID: backend.ID, Name: backend.Name},
		Incident: notify.IncidentSnapshot{
			ID:             open.ID,
			Status:         "down",
			OpenedAt:       notify.FormatTime(open.OpenedAt),
			FirstFailureAt: notify.FormatTime(open.FirstFailureAt),
			ResolvedAt:     nil,
			FailureCount:   open.FailureCount,
			RecoveryCount:  0,
			LastFailureAt:  notify.FormatOptionalTime(open.LastFailureAt),
			LastObservedAt: notify.FormatTime(observedTime(result, now)),
			LastStatusCode: statusCodeOrNil(result.HTTPStatus),
			LastError:      errorOrNil(result.Error),
			DurationMs:     result.Duration.Milliseconds(),
		},
		ReminderAt: &now,
	}
}

func observedTime(result probe.Result, now time.Time) time.Time {
	if !result.ObservedAt.IsZero() {
		return result.ObservedAt
	}
	return now
}

func statusCodeOrNil(code int) *int {
	if code == 0 {
		return nil
	}
	value := code
	return &value
}

func errorOrNil(summary string) *string {
	if summary == "" {
		return nil
	}
	bounded := notify.BoundError(summary)
	return &bounded
}

func refTime(t time.Time) *string {
	formatted := notify.FormatTime(t)
	return &formatted
}
