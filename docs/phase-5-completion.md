# Phase 5 Completion Report — History, Retention & Recovery Hardening

| Field | Value |
| --- | --- |
| Phase | 5 — History, Retention & Recovery Hardening |
| Branch | `feature/phase-5-history-retention-recovery` (from `develop`) |
| Scope | `status`/`history` commands, read-only SQLite access, batched retention, migration/restart hardening, backup guidance |
| Out of scope | End-to-end suites, failure injection, Docker runtime validation, releases (Phase 6–7); no schema change |

## 1. Scope reconciliation with Phase 4

Phase 4 was intentionally expanded to bring SQLite, the incident state machine,
and the transactional notification outbox forward. Phase 5 reuses all of it
without duplication:

Already delivered and verified in Phase 4 (not reimplemented):

- Migration 1 DDL for `backend_state`, `incidents`, `observations`, and
  `notification_events`, including the one-open-incident partial unique index
  and the documented secondary indexes.
- Transactional probe application (observation + streaks + incident + outbox in
  one commit), crash-safe delivery split, stale-`delivering` reset, duplicate
  prevention, reminder scheduling, and at-least-once semantics with stable
  event IDs.
- Pragmas and connection policy (one writer, WAL, `synchronous=FULL`,
  `foreign_keys=ON`, `temp_store=MEMORY`, 5s busy timeout), owner-only file
  and directory permissions, and fail-closed behavior for corrupt,
  inconsistent, and newer-schema databases.

Gaps closed in Phase 5:

- No read path existed: `status` and `history` were planned but unregistered.
  Added read-only queries and both commands; no second persistence layer, no
  new state machine, no new migration, no new index.
- No retention existed: Phase 4 explicitly never deleted rows. Added
  `ApplyRetention` with the documented never-delete invariants, wired as a
  best-effort startup step (and a 24h ticker in `monitor`).
- Read-only behavior was specified but unimplemented. Added `OpenReadOnly`
  (`mode=ro`, no creation/migration/delivery reset) and made `status` and
  `history` use it exclusively.
- Migration hardening gaps: read-only version-mismatch handling, missing-file
  guidance, and a rollback test for failed migrations.
- Restart/recovery coverage for the operational read path: status/history
  consistency across restarts, pending-delivery survival, no Down recreation,
  orphaned-incident visibility, and failure surfacing.

Defects in earlier phases fixed only as needed for Phase 5 safety: none in
domain logic. One honesty fix: a never-observed backend now reports
`unknown` (instead of `healthy`) when its streak row exists without an
observation. The logging allowlist gained seven numeric count fields used by
the new operational and retention logs.

## 2. What was built

- `internal/store/query.go` (new): `OpenReadOnly`, `GetBackendStates`,
  `CountFailed`/`CountFailedForBackend`/`CountPendingForBackend`,
  `QueryHistory` (parameterized, newest-first, deterministic tie-break,
  per-list limit 1..1000 with default 50, `HasMore`), and `ApplyRetention`
  (bounded 500/200-row batches, never deletes open incidents,
  pending/delivering/failed events, or `backend_state`; closed incidents are
  eligible only with no guarding non-terminal events; dead events cascade;
  idempotent; context-cancelable).
- `internal/app/status.go` (new): `Status` composes configuration identity
  with persisted state. Open incidents report `down`; never-observed backends
  report `unknown`; removed backends with open incidents report `orphaned`
  after configured backends. Unknown `--backend` values are usage errors;
  filtered counts stay honest to the selected scope. Read-only, 30s bounded.
- `internal/app/history.go` (new): `History` returns newest-first
  observations and incident notification events with `HasMore`. Unknown
  backend filters and empty history are valid empty results. Read-only, 30s
  bounded.
- `internal/app/retention.go` (new): best-effort `runRetention` mapping
  `config.Retention` to the store policy. Failures log a warning and never
  stop monitoring or fail a one-shot command. Wired into `check` startup and
  `monitor` startup plus a 24h ticker.
- `internal/cli/status.go`, `internal/cli/history.go` (new): registered
  `status` (`--backend`) and `history` (`--backend`, `--since` RFC3339 or Go
  duration, `--limit` 1..1000) with text and stable JSON output, stdout/stderr
  discipline, exit mapping (`status`: 0/1/2/3; `history`: 0/2/3), and debug
  completion logs.
- `internal/logging`: allowlist extended with `backend_count`,
  `open_incidents`, `observation_count`, `event_count`,
  `observations_deleted`, `events_deleted`, `incidents_deleted` (numeric
  counts only) with a regression test.
- No new dependencies, no schema migration, no config change.

## 3. Local verification (all executed)

- `gofmt -l cmd internal` — clean; `git diff --check` — clean.
- `go vet ./...` — clean.
- `golangci-lint run ./...` (v2.15.0) — 0 issues.
- `go test -race -count=1 ./...` — all 10 packages pass.
- `go test -race -shuffle=on -count=1 ./...` — all pass.
- Coverage: **86.4% overall** (`config` 92.7%, `probe` 88.2%, `app` 87.1%,
  `cli` 85.9%, `incident` 88.2%, `notify` 81.9%, `store` 82.5%,
  `monitor` 80.6%, `logging` 84.5%) — above the 80% overall and core-package gates.
- `make build`, `make cross-build` (5 targets) — pass.
- CLI smoke tests with the built binary: missing-DB hint (exit 3),
  healthy `check`/`status`/`history` (exits 0), failing backend opens an
  incident with `status` exit 1 and stable JSON, restart preserves state,
  permissions `0600`/`0700` observed.
- `govulncheck ./...` — blocked locally (HTTP 403 from `vuln.go.dev`);
  not claimed as passing. Remote CI runs the pinned scan.

## 4. Security properties verified by tests

- `status`/`history` never probe (backend request counter unchanged) and never
  mutate (row counts and `user_version` identical before/after; a `delivering`
  row survives read-only opens untouched).
- Resolved secrets (header values, webhook URL tokens) never appear in
  `status`/`history` text or JSON, stderr, or the SQLite file.
- Endpoint and webhook URLs never appear in operational JSON.
- Database errors carry safe paths and hints only; SQL parameters, headers,
  and secrets are never included.
- Read-only handles refuse writes at the SQLite VFS layer (`mode=ro`).
- File permissions remain owner-only on creation paths.
- New log fields are numeric counts covered by the allowlist regression test;
  unknown fields still fail closed to `[REDACTED]`.

## 5. Reliability guarantees and limitations

Verified:

- No silent in-memory fallback: every store failure surfaces as exit `3`
  (`database_unavailable` or `database_incompatible`).
- No false success: writes report success only after commit; retention and
  read failures never masquerade as clean results.
- Atomic incident transitions preserved from Phase 4; Phase 5 adds no path
  that splits an incident write from its event write.
- At-least-once delivery unchanged and documented; retries reuse the stable
  event ID and `Idempotency-Key`. Exactly-once is not claimed.
- Startup never deletes data: no reset, no vacuum, no automatic repair.
  Retention is the only deleter and is fenced by the never-delete invariants.
- Restart preserves open incidents, streaks, reminders, pending deliveries,
  and history; stale `delivering` rows reset to `pending` without a new event
  or an attempt-count increment (verified at store, app, and CLI levels).
- Schema newer than the binary fails closed with upgrade guidance on both
  handles; older schemas fail read-only opens with a writable-command hint;
  corrupt databases fail closed without modification.

Limitations:

- `monitor`'s 24h retention ticker is implemented but not time-travel tested;
  retention itself is covered directly with injected clocks. A 24h
  wall-clock test would be nondeterministic and is intentionally absent.
- Docker volume persistence across recreation is documented and follows from
  the file-based store plus permission handling, but container runtime
  validation belongs to Phase 6/7 and was not executed here.
- `govulncheck` could not reach its database locally; the finding is
  environmental, not a code gate waiver. CI evidence is still required.
- No `backup` command was added: the roadmap defers it and documents
  `VACUUM INTO`/online-backup plus stopped-process copies instead.

## 6. Remote verification (observed after push)

Push of the Phase 5 merge to `main` triggered GitHub Actions:

- CI (push on `main`, run `39912570050`): `success` — jobs `Quality and
  tests`, `Lint`, `Cross-platform build`, and `Container smoke test` all
  `success`. The `Quality and tests` job runs the pinned `govulncheck` scan,
  which closes the local environmental gap from §3.
- CodeQL (push on `main`, run `39912570086`): `success`, no unresolved
  high-severity findings.

## 7. Handoff to Phase 6
Phase 6 consumes as fixtures: `status`/`history` text and JSON with exit
codes, retention windows and guards, read-only failure modes (missing,
corrupt, directory, newer/older schema), restart preservation, and the
redaction boundary. Suggested Phase 6 targets drawn from the roadmap:
end-to-end binary runs, failure injection (DNS/refusal/timeout/TLS/malformed
and oversized bodies, webhook 4xx/429/5xx, retry exhaustion), crash-window
simulations, `-race` stress, goroutine/resource lifecycle checks, and Docker
persistence/signal tests. No schema change is pending for that work.
