package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// migration is one forward schema step.
type migration struct {
	version int
	name    string
	ddl     string
}

// migration1 creates the Phase 4 store slice. It follows the DDL proposed in
// docs/persistence.md. Later changes require migration 2, never an edit here.
const migration1DDL = `
CREATE TABLE backend_state (
    backend_id              TEXT PRIMARY KEY,
    consecutive_failures    INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    consecutive_successes   INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_successes >= 0),
    first_failure_at_ms     INTEGER,
    last_outcome            TEXT CHECK (last_outcome IN ('healthy', 'failed')),
    last_status_code        INTEGER,
    last_observed_at_ms     INTEGER,
    updated_at_ms           INTEGER NOT NULL
);

CREATE TABLE incidents (
    id                      TEXT PRIMARY KEY,
    backend_id              TEXT NOT NULL,
    state                   TEXT NOT NULL CHECK (state IN ('open', 'closed')),
    opened_at_ms            INTEGER NOT NULL,
    first_failure_at_ms     INTEGER NOT NULL,
    last_failure_at_ms      INTEGER,
    resolved_at_ms          INTEGER,
    failure_count           INTEGER NOT NULL CHECK (failure_count >= 1),
    recovery_count          INTEGER NOT NULL DEFAULT 0 CHECK (recovery_count >= 0),
    next_reminder_at_ms     INTEGER,
    reminder_count          INTEGER NOT NULL DEFAULT 0 CHECK (reminder_count >= 0),
    created_at_ms           INTEGER NOT NULL,
    updated_at_ms           INTEGER NOT NULL
);

CREATE UNIQUE INDEX incidents_one_open_per_backend
    ON incidents (backend_id)
    WHERE state = 'open';

CREATE INDEX incidents_backend_opened
    ON incidents (backend_id, opened_at_ms DESC);

CREATE INDEX incidents_open
    ON incidents (state, opened_at_ms DESC);

CREATE TABLE observations (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    backend_id              TEXT NOT NULL,
    observed_at_ms          INTEGER NOT NULL,
    outcome                 TEXT NOT NULL CHECK (outcome IN ('healthy', 'failed')),
    status_code             INTEGER,
    duration_ms             INTEGER NOT NULL CHECK (duration_ms >= 0),
    failure_reason          TEXT,
    error_summary           TEXT
);

CREATE INDEX observations_backend_time
    ON observations (backend_id, observed_at_ms DESC);

CREATE INDEX observations_time
    ON observations (observed_at_ms DESC);

CREATE TABLE notification_events (
    id                      TEXT PRIMARY KEY,
    incident_id             TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    backend_id              TEXT NOT NULL,
    event_type              TEXT NOT NULL CHECK (
                                event_type IN ('incident.opened',
                                               'incident.reminder',
                                               'incident.recovered')
                            ),
    sequence                INTEGER NOT NULL CHECK (sequence >= 1),
    payload_json            TEXT NOT NULL,
    status                  TEXT NOT NULL CHECK (
                                status IN ('pending', 'delivering', 'failed',
                                           'delivered', 'dead', 'canceled')
                            ),
    attempt_count           INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at_ms      INTEGER,
    last_error              TEXT,
    created_at_ms           INTEGER NOT NULL,
    updated_at_ms           INTEGER NOT NULL,
    delivered_at_ms         INTEGER,
    UNIQUE (incident_id, sequence)
);

CREATE INDEX notification_events_due
    ON notification_events (status, next_attempt_at_ms);

CREATE INDEX notification_events_incident
    ON notification_events (incident_id, sequence);
`

// migrations are applied in order. Each runs in a transaction that also
// advances user_version atomically.
var migrations = []migration{
	{version: 1, name: "initial incident and outbox schema", ddl: migration1DDL},
}

// requiredTables guards against a version-0 database that already contains
// application-like tables: such a database is inconsistent and must fail
// closed rather than being migrated over.
var requiredTables = []string{"backend_state", "incidents", "observations", "notification_events"}

// migrate brings the database to SupportedSchemaVersion.
func (s *Store) migrate() error {
	version, err := s.userVersion()
	if err != nil {
		return err
	}
	if version > SupportedSchemaVersion {
		return &OpenError{
			Reason:  "schema_newer",
			Path:    s.path,
			Message: fmt.Sprintf("monitoring database schema version %d is newer than this binary supports (%d); upgrade the binary", version, SupportedSchemaVersion),
		}
	}
	if version == 0 && s.hasApplicationTables() {
		// A concurrent creator may commit between the version read above
		// and the table check: re-read and adopt the freshly committed
		// schema instead of crying corruption. Only a version that is
		// still absent afterwards is genuinely inconsistent.
		if current, verr := s.userVersion(); verr == nil && current > 0 {
			version = current
		} else {
			return &OpenError{
				Reason:  "schema_inconsistent",
				Path:    s.path,
				Message: "monitoring database has an unknown schema; refusing to migrate over it",
			}
		}
	}
	for _, step := range migrations {
		if step.version <= version {
			continue
		}
		if err := s.applyMigration(step); err != nil {
			// Concurrent creators race this DDL: both can observe
			// version 0 and attempt the same CREATE TABLE. When the
			// loser re-reads and finds the winner's committed schema,
			// it adopts that version instead of failing. A version
			// that is still absent means a genuine migration defect,
			// which fails closed below.
			current, verr := s.userVersion()
			if verr == nil && current >= step.version {
				version = current
				continue
			}
			return err
		}
		version = step.version
	}
	if err := s.verifyTables(); err != nil {
		return err
	}
	return nil
}

func (s *Store) userVersion() (int, error) {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return 0, &OpenError{
			Reason:  "corrupt",
			Path:    s.path,
			Message: "monitoring database is not usable",
			cause:   err,
		}
	}
	return version, nil
}

// hasApplicationTables reports whether any of the managed tables already
// exist. A version-0 database with such tables is inconsistent.
func (s *Store) hasApplicationTables() bool {
	for _, table := range requiredTables {
		var name string
		err := s.db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err == nil && name == table {
			return true
		}
	}
	return false
}

func (s *Store) applyMigration(step migration) error {
	tx, err := s.db.Begin()
	if err != nil {
		return &OpenError{Reason: "migration", Path: s.path, Message: "cannot migrate monitoring database", cause: err}
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.Exec(step.ddl); err != nil {
		return &OpenError{
			Reason:  "migration",
			Path:    s.path,
			Message: "monitoring database migration failed and was rolled back",
			cause:   fmt.Errorf("migration %d (%s): %w", step.version, step.name, err),
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", step.version)); err != nil {
		return &OpenError{
			Reason:  "migration",
			Path:    s.path,
			Message: "monitoring database migration failed and was rolled back",
			cause:   err,
		}
	}
	if err := tx.Commit(); err != nil {
		return &OpenError{Reason: "migration", Path: s.path, Message: "cannot migrate monitoring database", cause: err}
	}
	committed = true
	return nil
}

func (s *Store) verifyTables() error {
	for _, table := range requiredTables {
		var name string
		err := s.db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			if err == sql.ErrNoRows {
				return &OpenError{
					Reason:  "schema_inconsistent",
					Path:    s.path,
					Message: "monitoring database schema is incomplete",
				}
			}
			return &OpenError{Reason: "corrupt", Path: s.path, Message: "monitoring database is not usable", cause: err}
		}
	}
	var partialIndex string
	err := s.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='index' AND name='incidents_one_open_per_backend'",
	).Scan(&partialIndex)
	if err != nil {
		return &OpenError{Reason: "schema_inconsistent", Path: s.path, Message: "monitoring database schema is incomplete"}
	}
	return nil
}

// isConstraintError reports SQLite constraint violations (unique, check,
// foreign key) so callers can distinguish expected races from corruption.
func isConstraintError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToUpper(err.Error())
	return strings.Contains(message, "CONSTRAINT") ||
		strings.Contains(message, "UNIQUE") ||
		strings.Contains(message, "CHECK CONSTRAINT FAILED")
}
