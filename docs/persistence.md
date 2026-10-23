# Persistence and Migration Strategy

| Field | Value |
| --- | --- |
| Status | Phase 5 implementation; migration 1 (`backend_state`, `incidents`, `observations`, `notification_events`) with read-only queries, batched retention, and restart reconciliation implemented |
| Engine | SQLite (single file) |
| Driver decision | `modernc.org/sqlite` (pure Go) implemented in Phase 4 |
| Related | [Configuration](configuration.md), [Incident lifecycle](incident-lifecycle.md), [Security](security.md) |

SQLite is the persistence source of truth for monitoring history and recovery
state. Configuration remains the source of truth for backend definitions and
policy. `status` and `history` open the database read-only (`mode=ro`): they
never create directories or files, never migrate, and never reset delivery
claims.

## 1. Responsibilities and Boundaries

### Stored in SQLite

- Probe observations.
- Per-backend consecutive failure and recovery state.
- Open and closed incidents.
- Incident event sequence and durable notification outbox rows.
- Delivery attempts, retry timing, and safe delivery errors.
- Minimal timestamps needed for retention.

### Not stored in SQLite

- Backend URLs, request headers, secret values, or a duplicate configuration.
- Webhook URL or webhook headers/secrets.
- Response bodies.
- Full configuration snapshots.

Backend IDs in the database reference the configuration identity. Removing a
backend does not delete its history or silently resolve its incident.

## 2. Database Location and Permissions

- Default path: `./data/abouei.db`, relative to the configuration file
  directory.
- Explicit path from `storage.database_path`.
- Parent directories are created on first use with owner-only permissions
  (`0700` where supported).
- The database file is created with owner-only permissions (`0600` where
  supported). WAL and shared-memory files created by SQLite inherit the
  directory's protection and should also be treated as sensitive.
- `:memory:` is not accepted in user configuration. Tests may use temporary
  files or explicitly constructed in-memory databases.
- The process requires write permission for `check` and `monitor`; `status` and
  `history` should open read-only when the file exists.
- If the database file does not exist:
  - `check` and `monitor` create it and apply migrations;
  - `status` and `history` fail with exit `3` and a hint to run a check or
    monitor first.
- If the path is a directory, special file, or unwritable, the command fails
  safely with exit `3`.

The Docker image uses `/data/abouei.db` inside a mounted volume. See
[section 9](#9-docker-persistence).

## 3. Driver and Connection Policy

The planned driver is `modernc.org/sqlite`:

- Pure Go, so the release matrix does not require CGO or platform-specific
  compilers.
- Compatible with static, cross-compiled binaries.
- Adds a dependency only when persistence is implemented in Phase 4/5.

The database is opened through the standard `database/sql` package. No ORM,
query builder, or database abstraction layer is introduced. There is no plan to
support another database engine.

Connection policy:

- One writer connection initially (`MaxOpenConns(1)`) to avoid `SQLITE_BUSY`
  complexity and keep transaction ordering predictable.
- WAL journal mode for concurrent readers and durable commits.
- `synchronous=FULL` for incident/outbox durability. Monitoring volume is low
  enough that this is an acceptable trade-off.
- `foreign_keys=ON`.
- `temp_store=MEMORY` so the documented read-only container root filesystem does
  not require a writable `/tmp`.
- `busy_timeout=5000` milliseconds.
- Short transactions; no HTTP/network work inside a transaction.
- The process is the only writer. Multi-instance access to the same file is not
  supported.

Exact pragma execution belongs in the Phase 4/5 store implementation and must be
verified by tests.

## 4. Data Model

Timestamps are stored as UTC Unix milliseconds in `INTEGER` columns. JSON and
CLI output render RFC3339 UTC strings. IDs are UUIDv4 strings generated with
`crypto/rand`.

The following DDL is the proposed initial schema. It is not executed in Phase 0
and may receive implementation-driven adjustments before migration 1 is frozen.
The migration runner sets `PRAGMA user_version = 1` after creating these objects.

```sql
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
    opened_at_ms           INTEGER NOT NULL,
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
```

### 4.1 Table responsibilities

`backend_state`:

- One row per backend that has been observed.
- Persists failure/recovery streaks so one-shot runs and restarts do not reset
  thresholds.
- `first_failure_at_ms` is the start of the streak leading to a potential
  incident.
- `last_outcome`, status, and observation time support `status` without scanning
  the full observation history.
- Rows for removed backends are retained until retention cleanup determines they
  are safe to remove.

`incidents`:

- The partial unique index enforces at most one open incident per backend.
- `state = 'open'` covers both Down and pending recovery; recovery is not
  complete until the incident is closed.
- `next_reminder_at_ms` and `reminder_count` support reminder scheduling.
- `resolved_at_ms` is set only on close.
- Failure counts are snapshots/accumulations for incident context; they are not
  a substitute for observations.

`observations`:

- One row per probe that is successfully persisted.
- Stores safe failure reason and a sanitized summary, never headers, bodies, or
  secrets.
- Retention may delete old rows; `backend_state` still supports current status.

`notification_events`:

- Durable outbox created in the same transaction as the incident transition.
- `payload_json` is a snapshot, allowing delivery to reproduce the exact event
  after restart. It contains no secrets.
- `sequence` is unique per incident and preserves event ordering.
- `status` transitions:
  `pending → delivering → delivered`, or `delivering → failed → delivering`, or
  `failed → dead`, and `pending/failed → canceled` on recovery.
- `attempt_count` includes the initial attempt.
- `next_attempt_at_ms` is the earliest retry time; `last_error` is safe and
  bounded to 512 UTF-8 bytes.
- Delivered events are retained for a short diagnostic window, then pruned.

### 4.2 Invariants

- At most one open incident per backend.
- Every open incident has at least one failure and `opened_at_ms` not before
  `first_failure_at_ms`.
- A closed incident has `resolved_at_ms >= opened_at_ms`.
- Event sequences are unique and positive within an incident.
- Every event belongs to an existing incident.
- Payload JSON is valid JSON and contains no resolved secrets.
- Backend IDs are stable strings; the database does not enforce configuration
  membership.
- Pending events for an incident are not deleted by retention.

## 5. Initialization and Migrations

### 5.1 First initialization

1. Resolve the database path.
2. Create parent directories with `0700` where supported.
3. Open the file and apply connection pragmas.
4. Read `PRAGMA user_version`.
5. If version is `0` and the database has no application tables, run migration 1
   in a transaction.
6. If version is `0` but application tables exist, fail closed as an inconsistent
   database rather than overwriting it.
7. Record `PRAGMA user_version = 1` and commit.
8. Verify essential tables and indexes exist.

### 5.2 Upgrade

- Migrations are ordered, embedded SQL files/statements compiled into the binary.
- Each migration runs in a transaction and advances `user_version` atomically.
- Migrations are forward-only. There are no automatic down migrations.
- A database with a version newer than the binary supports fails with exit `3`
  and a clear "upgrade the binary" message.
- A database with an unknown/partial schema fails closed.
- Before a migration that rewrites data, the implementation must create a backup
  using SQLite's backup mechanism or `VACUUM INTO`, or require an operator
  backup. The initial create-only migration does not need a pre-backup.
- Migration failure leaves the transaction rolled back and the process exits `3`.
- The application never deletes a database to "fix" migration or corruption
  errors.

### 5.3 Schema versioning summary

| Database state | Behavior |
| --- | --- |
| Empty file, version 0 | Create schema, set version 1 |
| Version 1, binary supports 1 | Normal operation |
| Version > supported | Fail with exit `3` and upgrade guidance |
| Version < supported | Apply sequential migrations (writable open only) |
| Version < supported on a read-only open | Fail with exit `3`; rerun a writable command to migrate |
| Corrupt/inconsistent | Fail closed; no destructive repair |
| Read-only and version mismatch | Fail; requires writable migration run |
| Read-only open with a missing file | Fail with exit `3` and a hint to run `check`/`monitor` first |

The proposed initial schema is migration 1. Phase 4 froze it; once released,
changes require migration 2. Phase 5 adds no schema change: status/history
queries and retention run on migration 1 with the existing indexes
(`observations_backend_time`, `observations_time`,
`incidents_backend_opened`, `incidents_open`, `notification_events_due`,
`notification_events_incident`), which already serve the newest-first,
backend-filtered query patterns.

## 6. Transaction Boundaries

### 6.1 Probe result transaction

For one backend result, a single transaction must:

1. Insert the observation.
2. Upsert `backend_state` with the new streaks and last outcome.
3. Open/update/close the incident when thresholds require it.
4. Insert the corresponding notification event(s) when policy enables them.
5. Update reminder scheduling fields.
6. Commit.

If any step fails, none of the state changes are visible. The probe may be
retried by the scheduler according to normal cadence; one-shot commands report an
internal error (`3`) if persistence fails.

### 6.2 Delivery transaction

Delivery is deliberately split:

1. Transaction: claim a due event by changing `pending`/`failed` to
   `delivering`, incrementing the attempt count, and recording the attempt time.
2. Outside a transaction: perform the HTTP request with timeout.
3. Transaction: mark `delivered`, schedule `failed` with backoff, or mark `dead`.

A crash between steps 2 and 3 leaves a `delivering` row. Startup resets stale
`delivering` rows to `pending` without incrementing the attempt count again. This
is the documented duplicate window.

### 6.3 Reminder scheduling transaction

Reminder enqueueing and `next_reminder_at_ms`/`reminder_count` updates happen in
the same transaction as the reminder event insert. Recovery cancels future
reminders and queued reminders transactionally, except for an HTTP request
already in flight.

### 6.4 Retention transaction

Retention deletes in bounded batches and commits per batch to avoid long write
locks. It never deletes:

- an open incident;
- a closed incident with pending/delivering/failed outbox events;
- a pending/delivering/failed/dead notification event before its retention
  policy allows;
- `backend_state` for a backend with an open incident.

## 7. Restart Recovery

On open:

1. Verify schema version.
2. Load open incidents and backend state.
3. Load due/pending notification events.
4. Reset `delivering` rows older than the delivery timeout to `pending`.
5. Validate invariants; reconstruct missing backend state from an open incident
   with a warning if necessary, without emitting a new Down event.
6. Return control to the application, which immediately probes configured
   backends as described in
   [incident-lifecycle.md](incident-lifecycle.md#10-restart-reconciliation).

No historical notification rows are replayed beyond those still in a
non-terminal state. Orphaned incidents remain open and visible.

## 8. Retention and Backup

Defaults are defined in [configuration.md](configuration.md#59-retention):

- observations: 30 days;
- closed incidents: 180 days;
- delivered events: 7 days.

Retention behavior:

- Observations older than `retention.observations` are deleted, regardless of
  backend, while `backend_state` keeps current status.
- Closed incidents older than `retention.incidents` are deleted only after they
  have no pending/delivering/failed notification events. Delivered and canceled
  events may cascade-delete with the incident.
- Delivered and canceled events older than `retention.delivered_events` are
  deleted. Dead/failed events are retained for diagnosis until incident cleanup,
  subject to a documented implementation cap.
- Open incidents, pending work, and the latest backend state are never removed.
- Cleanup runs on startup for commands that open the database read-write, and at
  most once every 24 hours during continuous monitoring. Cleanup failure logs a
  warning and does not stop monitoring.
- Retention is local and best-effort; operators who need a compliance archive
  must export data before it expires.
- Phase 5 implementation: `ApplyRetention` deletes in bounded batches (500
  rows for observations and terminal events, 200 for closed incidents, one
  committed statement per batch) so cleanup never holds a long write lock.
  Each predicate is a pure age-and-state filter, which makes reruns safe and
  deterministic. Closed incidents are eligible only when no
  pending/delivering/failed event references them; dead events cascade with
  their incident. `check` and `monitor` run retention once on startup and the
  monitor reruns it at most every 24 hours; failures log a warning and never
  stop monitoring or fail a one-shot command.

Backup guidance:

- Do not copy only the main database file while the process is writing; WAL may
  contain committed data.
- Use SQLite's online backup API or `VACUUM INTO` to create a consistent
  single-file snapshot, or stop the process before copying the database and its
  WAL/SHM files.
- A future `backup` command is deferred; external tooling is acceptable.
- Restore by stopping the process, replacing the database from a consistent
  snapshot, and starting with a compatible binary.
- Backups contain monitoring history and safe error summaries, not secrets or
  configuration, but should still be protected as operational data.

## 9. Docker Persistence

The container stores data under `/data`:

```text
/data/
  abouei.db
  abouei.db-wal  (when active)
  abouei.db-shm  (when active)
```

- The image does not declare an anonymous `VOLUME`; users must mount a named
  volume or bind mount explicitly.
- The image creates `/data` owned by numeric UID/GID `65532:65532`, and runs as
  that user.
- Configuration is mounted read-only at `/etc/abouei/abouei.yaml` and is not
  stored in the data volume.
- The container's writable layer is never treated as durable storage.
- Example: `-v abouei-data:/data`.
- Back up the volume only with the process stopped or through a consistent SQLite
  snapshot.
- If a bind mount is used, the host directory must be writable by UID `65532`.

The database path in container configuration should be absolute:
`/data/abouei.db`.

## 10. Error Behavior

| Condition | Behavior |
| --- | --- |
| Parent directory cannot be created | Exit `3`, safe path/permission message |
| Database file absent for check/monitor | Create and migrate |
| Database file absent for status/history | Exit `3`, hint to run check/monitor |
| Database locked beyond busy timeout | Exit `3`; no corrupt fallback |
| Corrupt database | Exit `3`; never overwrite or recreate |
| Schema newer than binary | Exit `3`; upgrade guidance |
| Migration failure | Roll back; exit `3` |
| Persistence failure during probe | No partial transition; exit `3` for one-shot, log/continue or fatal for monitor |
| Notification write failure | Incident transaction rolls back; error surfaced |
| Delivery failure | Persist retry state; incident remains valid |
| Read-only database for status/history | Allowed if schema is current |
| Multiple processes | Unsupported; busy/lock errors surfaced safely |

Database errors must name the operation and safe path context. They never include
SQL parameter values, headers, or secrets. A database error is an internal
operational error (`3`), not an unhealthy backend result (`1`).

## 11. Delivery-State Reset on Startup

- Rows in `delivering` are considered stale after `webhook.timeout + a small
  grace period`, or unconditionally at process startup because only one process
  may write.
- Reset to `pending` without incrementing `attempt_count`; the current attempt is
  considered abandoned.
- If the previous HTTP request actually succeeded, the receiver may see a
  duplicate. The event ID is unchanged and is the deduplication key.
- Do not reset `delivered`, `dead`, or `canceled` rows.

## 12. Phase 5 Acceptance Criteria

Persistence work is acceptable only with tests proving:

- first-run creation and migration from an empty file;
- idempotent startup with an already-current schema;
- forward migration and refusal to open a newer schema;
- corrupt/inconsistent database fails closed without data loss;
- atomic observation + state + incident + outbox commit and rollback;
- unique open incident per backend under concurrent transitions;
- unique event sequence and correct per-incident ordering;
- restart restores open incidents, streaks, reminders, and pending deliveries;
- stale `delivering` reset and duplicate-window behavior;
- retention never deletes open incidents or pending events;
- SQLite permissions and Docker volume persistence;
- status/history queries and read-only behavior;
- race detector passes around store access.
