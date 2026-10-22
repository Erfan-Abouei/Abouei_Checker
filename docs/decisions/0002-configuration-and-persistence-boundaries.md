# ADR-0002: Configuration and Persistence Boundaries

- **Status:** Accepted
- **Date:** 2026-10-22
- **Phase:** 0
- **Related:** [Configuration](../configuration.md), [Persistence](../persistence.md), [Incident lifecycle](../incident-lifecycle.md)

## Context

The product needs user-editable backend definitions and durable monitoring
state. Two plausible designs are:

1. Treat configuration as the source of truth for policy and use SQLite only for
   history.
2. Mirror configuration into SQLite and treat the database as an editable
   runtime store.

The second design creates conflicting sources of truth, secret duplication, and
ambiguous merge behavior. The product also supports one-shot runs and restart
recovery, so consecutive failure/recovery streaks cannot live only in memory.

## Decision

- YAML is the source of truth for all configuration: backend identities, URLs,
  headers, health criteria, scheduling, notification policy, logging, retention,
  and database location.
- SQLite is the source of truth for observations, per-backend threshold state,
  incidents, notification outbox rows, retry state, and recovery metadata.
- Configuration is not copied into SQLite. Backend IDs reference configuration
  identities.
- Persist consecutive failure and recovery counts in a `backend_state` table so
  one-shot `check` invocations and process restarts do not reset thresholds.
- Preserve open incidents and their event sequences across restarts.
- Use one open incident per backend, enforced by a partial unique index.
- Use SQLite only. Do not add a database interface, ORM, or alternative-engine
  abstraction.
- Use `PRAGMA user_version` with embedded, ordered, forward-only migrations.
- Open the database with one writer connection, WAL mode, `synchronous=FULL`,
  foreign keys, and a busy timeout.
- Store timestamps as UTC Unix milliseconds and render RFC3339 in output.
- Persist no secrets, headers, URLs, webhook configuration, or response bodies.
- Pull the transactional incident/outbox store slice into Phase 4 because
  durable state is required for correct notification and restart semantics.
  Phase 5 completes history queries, retention, and recovery hardening.

## Consequences

Positive:

- One unambiguous source for policy and one for runtime state.
- One-shot and continuous modes share threshold semantics.
- Restart recovery and duplicate prevention are implementable without replaying
  alerts.
- No premature multi-database abstraction.

Negative and mitigations:

- Backend removal leaves historical rows and can create orphaned incidents.
  These are shown explicitly, not auto-closed.
- The single-writer policy limits throughput. Monitoring volume is low, and
  correctness/durability are prioritized.
- Forward-only migrations require operators to upgrade deliberately and back up
  before risky changes.

## Alternatives Considered

- **Config in SQLite:** rejected; creates two sources of truth and secret storage
  risk.
- **In-memory threshold streaks:** rejected; breaks one-shot cron behavior and
  restart recovery.
- **External database (PostgreSQL):** rejected; disproportionate for a local CLI
  and conflicts with single-binary distribution.
- **ORM/migration framework:** deferred; a small embedded migration runner and
  `database/sql` are sufficient.
- **Down migrations:** rejected for v1; they add risk and are rarely tested
  better than restore-from-backup.
