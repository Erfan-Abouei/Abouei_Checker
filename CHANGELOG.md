# Changelog

All notable changes to AboueiChecker are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and versioning
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

No public release exists yet. Entries below describe the pre-release
implementation state on the `develop` branch.

## [Unreleased]

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
  codes.

### Fixed

- Concurrent database initialization: overlapping processes racing fresh
  database creation (duplicate `CREATE TABLE`, torn version/table reads)
  now converge via schema adoption plus bounded retry instead of failing
  with `database_unavailable`. Verified 0 failures in 80 concurrent runs.
- Concurrent writes during reconciliation and result processing now use
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

[Unreleased]: https://github.com/Erfan-Abouei/Abouei_Checker/compare/main...develop
[0.5.0]: https://github.com/Erfan-Abouei/Abouei_Checker/tree/develop
