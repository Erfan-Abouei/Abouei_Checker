# Phase 4 Completion Report — Incident Lifecycle, Persistence & Notification Engine

| Field | Value |
| --- | --- |
| Phase | 4 — Incident Lifecycle, Durable State & Notifications |
| Branch | `feature/phase-4-incident-notification-engine` (from `develop` at `2c0f3a5`) |
| Scope | SQLite persistence, incident state machine, outbox, webhook delivery, reminders, durable `check`/`monitor` |
| Out of scope | `status`/`history`, retention, backup guidance (Phase 5); new providers; releases |

## 1. What was built

- `internal/store` (new): pure-Go SQLite via `modernc.org/sqlite` v1.39.0
  (pinned; no CGO, cross-compile safe). Migration 1 creates `backend_state`,
  `incidents`, `observations`, and `notification_events` exactly per the
  `docs/persistence.md` DDL, with the one-open-incident partial unique index
  and `user_version = 1`. One writer connection, WAL, `synchronous=FULL`,
  `foreign_keys=ON`, `temp_store=MEMORY`, 5s busy timeout; owner-only
  directories (`0700`) and files (`0600`). Every probe commits observation,
  streak upsert, incident transition, outbox insert, and due-reminder
  evaluation in one transaction; network I/O never runs inside a transaction.
  Newer schemas fail closed with upgrade guidance; inconsistent or corrupt
  databases fail closed without repair.
- `internal/incident` (new): pure threshold state machine (`Evaluate`,
  `ShouldRemind`, UUIDv4 identity). Sub-threshold failures create no
  incident; the threshold opens exactly one incident; repeats never duplicate
  Down; recovery thresholds >1 close exactly once; disabled policy still
  records incidents without events; canceled probes change nothing.
- `internal/notify` (new): versioned compact JSON payloads (`incident.opened`
  / `incident.reminder` / `incident.recovered`) with no headers, URLs,
  secrets, or bodies; POST with `Content-Type`, `User-Agent`, and
  `Idempotency-Key = event_id`; no redirects; TLS verified; 64 KiB bounded
  body drain; response classification (2xx delivered; 408/425/429/5xx and
  transport errors retryable; other 4xx and 3xx permanent); full-jitter
  exponential backoff honoring `Retry-After` (capped); outbox worker with
  atomic per-incident-ordered claims, stale-`delivering` reset on startup,
  dead events that never block later sequences.
- `internal/app`: `check` and `monitor` now open/migrate SQLite, reconcile
  state (streak reconstruction with warning, pending-event + missing-webhook
  warning), persist every probe, and deliver due notifications (`check`
  attempts once per due event: exit `4` on failure with precedence `4 > 1`;
  `monitor` runs the worker with poke-on-enqueue and bounded shutdown).
  Persistence failures are exit `3`; unknown backends fail before any
  database is created.
- `internal/cli`: real `incidents_opened/closed`,
  `notifications_attempted/failed`, and `incident_status`
  (`none/open/still_open/recovered`) in text and JSON; `monitor` probe
  events carry `incident_status`; new `incident_id`/`incident_status` safe
  log fields.
- Tooling: `modernc.org/sqlite` dependency, `COVERAGE_MIN=80`, and
  `scripts/check-package-coverage.sh` enforcing 80% in each core package
  through `make cover-check`.

## 2. Local verification (all executed)

- `gofmt -l cmd internal` — clean; `git diff --check` — clean.
- `go vet ./...` — clean.
- `golangci-lint run ./...` (v2.15.0) — 0 issues.
- `go test -race -count=1 ./...` — all 10 packages pass.
- `go test -race -shuffle=on -count=1 ./...` — all pass.
- Coverage: **86.4% overall** (`config` 92.7%, `probe` 88.2%, `app` 87.8%,
  `cli` 84.9%, `incident` 88.2%, `notify` 81.9%, `store` 81.0%,
  `monitor` 80.6%, `logging` 84.5%) — above the 80% overall and per-core-package gates.
- `make build`, `make cross-build` (5 targets) — pass.
- CLI smoke tests with local backends and webhooks: threshold-gated
  incident open/close across restarts, exit `4` on failing webhook with the
  event still queued, SIGINT shutdown with pending work durable.
- `govulncheck ./...` — blocked locally (HTTP 403 from `vuln.go.dev`);
  not claimed as passing. Remote CI runs the pinned scan.

## 3. Security properties verified by tests

- Webhook payloads carry no secrets, headers, URLs, or bodies (receiver-side
  assertions); persisted `payload_json` and error summaries are clean;
  the SQLite file contains no resolved secrets even when the webhook URL
  and headers use `${...}` references.
- TLS verification cannot be disabled; webhook redirects fail permanently;
  custom headers are sent but never logged.
- Retry state is durable and bounded (`max_attempts` includes the first
  attempt); dead events are terminal and visible.
- SQLite files are owner-only; corrupt/newer/foreign databases fail closed.

## 4. Delivery guarantees and limitations

- Delivery is **at-least-once**: a crash between remote success and local
  status commit retries with the same `event_id`; consumers deduplicate by
  `event_id`/`Idempotency-Key`. Exactly-once is not claimed.
- One writer process per database; concurrent processes surface lock errors
  (exit `3`) rather than coordinating.
- No retention or cleanup runs in Phase 4; outbox and history rows accumulate
  until Phase 5 retention lands.
- `status` and `history` remain Phase 5; orphaned incidents stay open and
  visible in the database but have no CLI surface yet.

## 5. Handoff to Phase 5

Phase 5 consumes the `observations`, `incidents`, and `notification_events`
tables as built here and adds read-only `status`/`history`, retention with
the documented never-delete invariants, migration hardening, backup guidance,
and full restart/reconciliation coverage. No schema change is required for
that work; retention deletes must respect the pending/delivering/failed and
open-incident guards already documented in `docs/persistence.md`.
