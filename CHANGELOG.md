# Changelog

All notable changes to AboueiChecker are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and versioning
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

No public release exists yet. Entries below describe the pre-release
implementation state on the `develop` branch.

## [0.6.0] - 2026-10-23

First public release candidate: Phase 6 integration evidence, npm
distribution, and release automation.

### Added

- Phase 6 integration suites (`internal/integration/`): end-to-end
  Down/Recovery journeys across restarts, transient/permanent webhook
  failure handling, failure-injection matrix, concurrent-backend isolation,
  cancellation safety, and monitor shutdown verification.
- Built-binary E2E tests: `version`, `init`, `validate`, `check`,
  `status`, `history`, unknown-command handling, and concurrent runs
  sharing one database.
- Default TUI launch: bare `abouei-checker` opens the interactive
  dashboard in a TTY, prints guided help otherwise, and never launches a
  fullscreen UI in pipes, CI, or `--output json` mode.
- npm distribution layer (`npm/`): global `abouei-checker` launcher that
  resolves a pinned versioned binary and forwards args, signals, and exit
  codes. Release archives are SHA-256 verified; member validation rejects
  traversal and unexpected layouts before extraction.
- Manual release workflow (`.github/workflows/release.yml`): version/tag
  guards, five platform builds with smoke tests, checksummed archives,
  npm validation, readiness report. No automatic publishing.

### Fixed

- Concurrent database initialization: overlapping processes racing fresh
  database creation (duplicate `CREATE TABLE`, torn version/table reads)
  now converge via schema adoption plus bounded retry instead of failing
  with `database_unavailable`.
- Concurrent startup reconciliation: racing streak-row inserts converge on
  the existing row instead of surfacing unique-constraint errors.
- Concurrent writes during reconciliation and result processing use
  bounded transient-error retry instead of surfacing lock errors.

### Changed

- `internal/store`: `Open` retries only contention-signature failures
  (bounded 25 × 200ms); permissions, schema, and corruption errors still
  fail closed immediately. `ProcessResult`, `EnsureBackendState`,
  `ListOpenIncidents`, and `CountPending` retry SQLite lock waits.

## [0.5.0] — 2026-10-22 (pre-release milestone, unreleased)

### Added

- Phases 0–5 implementation: CLI foundation, strict YAML configuration,
  safe HTTP probe engine, threshold incidents, SQLite persistence with
  migrations, webhook outbox with retries and idempotency, read-only
  `status`/`history`, retention, restart reconciliation, and the
  interactive TUI dashboard.

[0.6.0]: https://github.com/Erfan-Abouei/Abouei_Checker/releases/tag/v0.6.0
[0.5.0]: https://github.com/Erfan-Abouei/Abouei_Checker/tree/develop
