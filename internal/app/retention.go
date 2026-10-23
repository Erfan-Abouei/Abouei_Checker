package app

import (
	"context"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/store"
)

// retentionInterval bounds how often the continuous monitor reruns cleanup.
// Startup always runs retention once for commands that open the database
// read-write; the monitor additionally reruns at this interval.
const retentionInterval = 24 * time.Hour

// runRetention applies the configured retention windows as a best-effort
// background step. Cleanup failures are observable through a warning log but
// never stop monitoring or fail a one-shot command: retention must not turn
// an expired-history problem into a health-check outage.
func (a *App) runRetention(ctx context.Context, db *store.Store) store.RetentionResult {
	if a == nil || a.config == nil || db == nil {
		return store.RetentionResult{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	result, err := db.ApplyRetention(ctx, store.RetentionPolicy{
		Observations:    a.config.Retention.Observations,
		Incidents:       a.config.Retention.Incidents,
		DeliveredEvents: a.config.Retention.DeliveredEvents,
	}, time.Now().UTC())
	if err != nil {
		a.logger.WarnContext(ctx, "retention cleanup failed; will retry on the next run",
			"component", "store",
			"operation", "retention",
			"error_category", "internal",
		)
		return store.RetentionResult{}
	}
	if result.ObservationsDeleted+result.EventsDeleted+result.IncidentsDeleted > 0 {
		a.logger.InfoContext(ctx, "retention cleanup completed",
			"component", "store",
			"operation", "retention",
			"observations_deleted", result.ObservationsDeleted,
			"events_deleted", result.EventsDeleted,
			"incidents_deleted", result.IncidentsDeleted,
		)
	}
	return result
}
