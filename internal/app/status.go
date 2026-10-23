package app

import (
	"context"
	"fmt"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/store"
)

// Public backend health values reported by Status. "down" covers both down
// and pending-recovery: the incident is still open. "orphaned" marks an open
// incident whose backend is no longer configured.
const (
	StatusHealthy  = "healthy"
	StatusDown     = "down"
	StatusUnknown  = "unknown"
	StatusOrphaned = "orphaned"
)

// BackendStatus is the persisted operational state of one backend. Pointer
// fields are nil when the database holds no value; callers render those as
// "unknown" instead of inventing defaults.
type BackendStatus struct {
	BackendID            string
	Name                 string
	Status               string
	LastObservedAt       *time.Time
	LastStatusCode       *int
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	OpenIncidentID       *string
	OpenedAt             *time.Time
}

// StatusReport is the read-only operational snapshot for the status command.
type StatusReport struct {
	Backends             []BackendStatus
	OpenIncidents        int
	PendingNotifications int
	FailedNotifications  int
	GeneratedAt          time.Time
}

// Status reads persisted backend state and open incidents without probing or
// mutating anything. Configuration must be loaded with LoadConfig before
// calling Status. An empty backendFilter returns every configured backend
// plus orphaned incidents; a non-empty filter selects one backend ID exactly.
func (a *App) Status(ctx context.Context, backendFilter string) (StatusReport, error) {
	if a == nil || a.config == nil {
		return StatusReport{}, NewError(CategoryInternal, CodeInternal, "configuration is not loaded")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Bound the whole read so a locked database cannot hang status forever;
	// the busy timeout bounds lock waits and this bounds everything else.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	db, err := a.OpenReadOnlyStore()
	if err != nil {
		return StatusReport{}, err
	}
	defer func() { _ = db.Close() }()

	states, err := db.GetBackendStates(ctx)
	if err != nil {
		return StatusReport{}, classifyStoreError(err)
	}
	open, err := db.ListOpenIncidents(ctx)
	if err != nil {
		return StatusReport{}, classifyStoreError(err)
	}
	pending, err := db.CountPending(ctx)
	if err != nil {
		return StatusReport{}, classifyStoreError(err)
	}
	failed, err := db.CountFailed(ctx)
	if err != nil {
		return StatusReport{}, classifyStoreError(err)
	}

	byID := make(map[string]store.IncidentRow, len(open))
	for _, incidentRow := range open {
		byID[incidentRow.BackendID] = incidentRow
	}
	configured := make(map[string]string, len(a.config.Backends))
	for _, backend := range a.config.Backends {
		configured[backend.ID] = backend.Name
	}

	// A filter that matches nothing configured and nothing persisted is a
	// usage error, mirroring check's unknown-backend behavior.
	if backendFilter != "" {
		if _, ok := configured[backendFilter]; !ok {
			if _, ok := byID[backendFilter]; !ok {
				return StatusReport{}, WrapError(
					CategoryUsage,
					CodeBackendNotFound,
					fmt.Sprintf("unknown backend %q", backendFilter),
					fmt.Errorf("unknown backend %q", backendFilter),
				)
			}
		}
	}

	report := StatusReport{GeneratedAt: time.Now().UTC()}
	openCount := 0
	for _, backend := range a.config.Backends {
		if backendFilter != "" && backend.ID != backendFilter {
			continue
		}
		view := BackendStatus{
			BackendID: backend.ID,
			Name:      backend.Name,
			Status:    StatusUnknown,
		}
		if state, ok := states[backend.ID]; ok {
			view.ConsecutiveFailures = state.ConsecutiveFailures
			view.ConsecutiveSuccesses = state.ConsecutiveSuccesses
			view.LastObservedAt = state.LastObservedAt
			view.LastStatusCode = state.LastStatusCode
			// A streak row created by startup reconciliation carries no
			// observation yet; report unknown instead of inventing health.
			if state.LastObservedAt != nil {
				view.Status = StatusHealthy
			}
		}
		if incidentRow, ok := byID[backend.ID]; ok {
			view.Status = StatusDown
			id := incidentRow.ID
			view.OpenIncidentID = &id
			opened := incidentRow.OpenedAt.UTC()
			view.OpenedAt = &opened
			openCount++
		}
		report.Backends = append(report.Backends, view)
	}
	// Orphaned incidents stay open and visible; they are never auto-closed,
	// reminded, or probed. They appear after configured backends.
	for _, incidentRow := range open {
		if _, ok := configured[incidentRow.BackendID]; ok {
			continue
		}
		if backendFilter != "" && incidentRow.BackendID != backendFilter {
			continue
		}
		id := incidentRow.ID
		opened := incidentRow.OpenedAt.UTC()
		report.Backends = append(report.Backends, BackendStatus{
			BackendID:      incidentRow.BackendID,
			Name:           incidentRow.BackendID,
			Status:         StatusOrphaned,
			OpenIncidentID: &id,
			OpenedAt:       &opened,
		})
		openCount++
	}
	if report.Backends == nil {
		report.Backends = []BackendStatus{}
	}
	report.OpenIncidents = openCount
	if backendFilter == "" {
		report.PendingNotifications = pending
		report.FailedNotifications = failed
	} else {
		// Filtered counts stay honest: count only the selected backend's
		// non-terminal and failed rows instead of echoing global totals.
		filteredPending, err := db.CountPendingForBackend(ctx, backendFilter)
		if err != nil {
			return StatusReport{}, classifyStoreError(err)
		}
		filteredFailed, err := db.CountFailedForBackend(ctx, backendFilter)
		if err != nil {
			return StatusReport{}, classifyStoreError(err)
		}
		report.PendingNotifications = filteredPending
		report.FailedNotifications = filteredFailed
	}
	return report, nil
}

// OpenReadOnlyStore opens the monitoring database without mutating it for
// read-only commands. A missing file is an operational error (exit 3) with a
// hint to run check or monitor first.
func (a *App) OpenReadOnlyStore() (*store.Store, error) {
	if a == nil || a.config == nil {
		return nil, NewError(CategoryInternal, CodeInternal, "configuration is not loaded")
	}
	db, err := store.OpenReadOnly(a.config.Storage.DatabasePath)
	if err != nil {
		return nil, classifyStoreError(err)
	}
	return db, nil
}
