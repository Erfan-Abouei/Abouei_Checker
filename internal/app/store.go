package app

import (
	"context"
	"errors"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/store"
)

// OpenStore opens the monitoring database for the loaded configuration,
// creating parent directories and applying migrations. Database failures are
// internal operational errors (exit 3): the application never falls back to
// ephemeral state while claiming durability.
func (a *App) OpenStore() (*store.Store, error) {
	if a == nil || a.config == nil {
		return nil, NewError(CategoryInternal, CodeInternal, "configuration is not loaded")
	}
	db, err := store.Open(a.config.Storage.DatabasePath)
	if err != nil {
		return nil, classifyStoreError(err)
	}
	return db, nil
}

func classifyStoreError(err error) *Error {
	var openErr *store.OpenError
	if !errors.As(err, &openErr) {
		return WrapError(CategoryInternal, CodeDatabaseUnavailable, "monitoring database is unavailable", err)
	}
	switch openErr.Reason {
	case "schema_newer":
		return WrapError(CategoryInternal, CodeDatabaseIncompatible, openErr.Message, err)
	default:
		return WrapError(CategoryInternal, CodeDatabaseUnavailable, openErr.Message, err)
	}
}

// reconcileStore restores durable threshold state after (re)start: it ensures
// every configured backend and every open incident's backend has a streak
// row, reconstructing missing rows from open incidents with a warning instead
// of resetting streaks or emitting duplicate Down events.
func (a *App) reconcileStore(ctx context.Context, db *store.Store) error {
	if a == nil || db == nil {
		return NewError(CategoryInternal, CodeInternal, "configuration is not loaded")
	}
	seen := make(map[string]struct{}, len(a.config.Backends))
	for _, backend := range a.config.Backends {
		if _, duplicate := seen[backend.ID]; duplicate {
			continue
		}
		seen[backend.ID] = struct{}{}
		if _, err := db.EnsureBackendState(ctx, backend.ID, nil); err != nil {
			return classifyStoreError(err)
		}
	}
	open, err := db.ListOpenIncidents(ctx)
	if err != nil {
		return classifyStoreError(err)
	}
	for _, incidentRow := range open {
		reconstructed, err := db.EnsureBackendState(ctx, incidentRow.BackendID, &incidentRow)
		if err != nil {
			return classifyStoreError(err)
		}
		if reconstructed {
			a.logger.WarnContext(ctx, "backend state reconstructed from open incident",
				"component", "store",
				"operation", "reconcile_state",
				"backend_id", incidentRow.BackendID,
				"incident_id", incidentRow.ID,
			)
		}
	}
	pending, err := db.CountPending(ctx)
	if err != nil {
		return classifyStoreError(err)
	}
	if pending > 0 && a.config.Notifications.Webhook.URL == "" {
		a.logger.WarnContext(ctx, "pending notifications exist but no webhook is configured",
			"component", "notify",
			"operation", "reconcile_outbox",
		)
	}
	return nil
}
