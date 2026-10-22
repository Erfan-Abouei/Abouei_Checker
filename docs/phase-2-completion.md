# Phase 2 Completion Report

| Field | Value |
| --- | --- |
| Date | 2026-10-22 |
| Phase | 2 — Configuration & Backend Registry |
| Status | Implemented and verified locally and on GitHub CI/CodeQL; merged into `develop` via PR #6 and promoted to `main` |
| Repository | `github.com/Erfan-Abouei/Abouei_Checker` |
| Branch | `feature/phase-2-configuration` |
| Pull request | [#6](https://github.com/Erfan-Abouei/Abouei_Checker/pull/6) |
| Baseline | `develop` at `8ea51b5` |
| Production readiness | Not production-ready; monitoring commands remain unavailable |

This report separates implementation, local verification, and remote
verification. A remote check is reported only after an observed GitHub run.

## 1. Executive Summary

Phase 2 implements the complete configuration subsystem:

- versioned YAML schema types with documented defaults and per-backend
  inheritance;
- deterministic discovery through `--config`, `ABOUEI_CONFIG`,
  `./abouei.yaml`, and the platform user configuration directory;
- bounded strict parsing with single-document, unknown-field, and duplicate-key
  rejection;
- `${NAME}` environment expansion with `$$` escaping, non-recursive resolution,
  and redaction registration;
- URL, header, status-code, body-match, duration, threshold, retention,
  notification, and cross-field validation;
- immutable normalized runtime configuration;
- `init` and `validate` commands with text/JSON output and documented exit codes.

No probing, scheduling, SQLite, incident, notification delivery, or future-phase
runtime behavior is introduced. Configuration fields for later phases are
validated and normalized only.

## 2. Initial Repository State and Phase 1 Assessment

- The working tree was clean on `feature/phase-1-project-foundation` at
  `926983a`, tracking `origin` with no local changes.
- `develop` was at `af75327` and `origin/develop` matched.
- `main` was at `a769b7a`; `origin/HEAD` pointed at `main`.
- Pull request #1 (`Phase 1: shared CLI, logging, and error foundation`) was
  open, mergeable, and targeted `develop`.
- Phase 1 verification was rerun before integration: `go test ./...` passed.
- Phase 1 was merged through PR #1 into `develop` at merge commit `8ea51b5`.
  The feature branch `feature/phase-1-project-foundation` remains available.
- Phase 2 was started from the updated `develop` baseline with git-flow as
  `feature/phase-2-configuration`.

The Phase 1 handoff claimed configuration loading was absent. Inspection
confirmed this: `--config` was recorded but never opened; `internal/app` built a
logger and error model only. No prerequisite gap blocked Phase 2.

## 3. Schema and Architecture Implemented

### 3.1 Package ownership

| Package | Phase 2 responsibility |
| --- | --- |
| `internal/config` | Discovery, bounded read, strict decode, expansion, validation, normalization, init template |
| `internal/app` | Config loading orchestration, error classification, redaction registration, logging reconfiguration |
| `internal/cli` | `validate` and `init` commands, text/JSON rendering, exit mapping |
| `internal/logging` | Existing redaction boundary; now receives resolved environment values |

The config package has no dependency on Cobra, SQLite, HTTP, or the application
composition root. The CLI does not import `internal/config`; it consumes an
`internal/app` summary and classified errors.

### 3.2 Input, validated, and runtime representations

- Unexported `raw*` types represent YAML input with pointer fields and a
  `yaml.Node` for `body_match` so omitted, null, and object values remain
  distinguishable.
- Validation resolves inheritance into effective per-backend policy values.
- The returned `*config.Config` is constructed only after all validation
  succeeds. It exposes normalized durations, sorted status codes and headers,
  compiled RE2 matchers, resolved paths, and effective notification policies.
- Resolved environment values are kept in an unexported slice and registered
  with the redactor; they are not exposed through the public API.

### 3.3 Dependencies

- Added `go.yaml.in/yaml/v3` v3.0.5, the maintained go-yaml v3 module.
- It is the only new direct dependency. It adds no runtime transitive
  dependency beyond the standard library.
- License review: MIT and Apache-2.0, compatible with this MIT project.
- Rationale, alternatives, schema freeze, and limits are recorded in
  [ADR-0006](decisions/0006-yaml-decoder-and-config-schema.md).

## 4. Loading, Discovery, Validation, Normalization, and Secret Handling

### 4.1 Discovery

Precedence is exactly:

1. `--config PATH` (empty means omitted);
2. `ABOUEI_CONFIG` (empty is treated as unset);
3. `./abouei.yaml`;
4. `<user-config-dir>/abouei-checker/config.yaml`.

An explicit source that is missing or inaccessible is an error with no
fallback. Default candidates are tried in order. If none exists, the error names
the searched locations and suggests `abouei-checker init`. Relative paths resolve
against the working directory; the source path and origin are retained.

### 4.2 File handling

- 1 MiB hard limit; reads stop at 1 MiB + 1 byte.
- The final path component must be a regular file.
- Symbolic links are rejected for loading and for `init` targets.
- Files are opened read-only; no file is created or repaired by `validate`.
- I/O failures are configuration errors (exit 2), never silently ignored.

### 4.3 Parsing

- Empty files and comments-only files are errors.
- Empty documents (`---`) and null roots are errors.
- Exactly one YAML document is accepted.
- The root must be a mapping.
- Unknown fields are rejected with `KnownFields(true)` at every struct level.
- Duplicate mapping keys are rejected by the decoder, including nested maps.
- Invalid YAML, wrong types, and malformed durations report line information
  where available.
- Decoder errors are sanitized to remove internal Go type names and raw scalar
  snippets. Resolved environment values cannot appear in decode errors because
  expansion runs after decoding.

### 4.4 Environment references

- `${NAME}` with `NAME` matching `[A-Za-z_][A-Za-z0-9_]*`.
- Embedded references and `$$` are supported; a bare `$` is literal.
- Missing, empty, malformed, or unterminated references are configuration errors
  that name the field and variable.
- Expansion applies to string-typed fields, including duration strings. Mapping
  keys and numeric/boolean fields are not expanded.
- Resolved values are not rescanned, so a secret containing `$` is preserved.
- Aliased `body_match` values are expanded once, preventing double expansion.
- Every substituted environment value is registered for redaction.
- Error and validation messages never include resolved values.

### 4.5 Validation and normalization

Implemented constraints include:

- schema `version` required and exactly `1`;
- storage path non-empty, not `:memory:`, resolved against the configuration
  directory, and `~` not expanded;
- concurrency 1..64 and shutdown timeout 1s..5m;
- logging level/format case-insensitive and normalized lowercase;
- interval 1s..24h, timeout 100ms..5m, and effective timeout < effective
  interval;
- failure/recovery thresholds 1..100, response bytes 1..16 MiB;
- unique backend IDs matching `^[a-z0-9][a-z0-9_-]{0,62}$`, names up to 128
  bytes, at most 100 backends;
- absolute HTTP/HTTPS URLs, host required, no userinfo/fragment/whitespace/
  control characters, at most 2048 bytes;
- at most 50 headers per target, valid token names, reserved-name rejection,
  canonical duplicate detection, no CR/LF/NUL, 8 KiB per value, 16 KiB total;
- status codes non-empty, unique, and 100..599, normalized ascending; omitted
  means the 2xx default;
- `body_match` requires `contains` or `regex` and a non-empty value; regex is
  compiled with RE2; `body_match: null` clears an inherited matcher;
- notification policy merge by field, reminder interval `0s` or 1m..168h,
  reminder max 0..1000, enabled policy must enable at least one channel;
- webhook URL required when any effective policy is enabled, retry attempts
  1..20, backoff 100ms..1h, and max backoff >= initial backoff;
- retention ranges exactly as documented.

Validation errors are aggregated in deterministic schema order. At most 20
field errors are shown, followed by a count of omitted errors. No value is
echoed.

## 5. CLI Behavior and Exit Codes

### 5.1 `validate`

- `abouei-checker validate [--config PATH] [--output text|json]`.
- Loads, expands, validates, and normalizes configuration.
- Sends no HTTP request and creates no database or data directory.
- Text output names schema version, backend count, and backend IDs.
- JSON success data: `valid`, `config_version`, `backend_count`,
  `backend_ids`.
- JSON failure output is a valid error envelope on stdout with stable code and
  safe details; stderr contains exactly one `Error:` line.
- Exit `0` valid; `2` missing/invalid configuration or missing secret; `3`
  unexpected internal failure.

### 5.2 `init`

- `abouei-checker init [PATH] [--force]`.
- Defaults to `./abouei.yaml`; creates parent directories with `0700`.
- New files use `0600` where supported; force-overwrite resets the mode to
  `0600`.
- Refuses existing files without `--force`, directories, special files, and
  symbolic-link targets.
- Does not read YAML, resolve secrets, probe, or touch SQLite.
- Exit `0` created/overwritten; `2` invalid target or existing file without
  `--force`; `3` filesystem write failure.

### 5.3 Preserved Phase 1 behavior

- `version` text and JSON output remains compatible and never reads YAML.
- Root help, command help, unknown-command handling, and usage errors still
  behave as before.
- `--config`, `--log-level`, and `--output` remain global flags.
- An explicit `--log-level` overrides configuration; otherwise
  `logging.level` applies. `logging.format` selects text or JSON logs.
- Logs remain on stderr; command results remain on stdout.
- Exit categories remain centralized: usage/configuration `2`, internal `3`,
  unhealthy `1`, notification `4`.

## 6. Documentation and Example Changes

- `docs/configuration.md` now documents the implemented loader, discovery,
  symlink behavior, limits, expansion scope, aggregation, JSON error behavior,
  and runtime-phase boundaries.
- `docs/cli-contract.md` marks `init` and `validate` implemented and documents
  their output/exit behavior.
- `docs/architecture.md` adds `internal/config` to the implemented layout and
  updates the configuration flow and logging descriptions.
- `docs/security.md` records implemented controls: symlink rejection, bounded
  reads, URL/header limits, expansion scope, redaction registration, and init
  permissions.
- `docs/testing-and-quality.md` records the Phase 2 test matrix, 70% floor, and
  measured coverage.
- `docs/requirements.md`, `docs/roadmap.md`, and `README.md` reflect current
  command availability without advertising monitoring features.
- `examples/abouei.yaml` is now a validated, currently supported example; it
  requires `EXAMPLE_API_TOKEN` and `EXAMPLE_API_KEY` when validated.
- Added [ADR-0006](decisions/0006-yaml-decoder-and-config-schema.md).

## 7. Tests and Exact Verification Results

All commands were run from the repository root on 2026-10-22. The test suite has
152 top-level tests, many table-driven with additional subtests.

| Check | Exact command | Result |
| --- | --- | --- |
| Formatting | `gofmt -l cmd internal` | no output |
| Vet | `go vet ./...` | passed |
| Race tests | `go test -race -count=1 ./...` | passed |
| Shuffled race tests | `go test -race -shuffle=on -count=1 ./...` | passed |
| Coverage generation | `go test -race -covermode=atomic -coverprofile=coverage.out ./...` | passed |
| Coverage total | `go tool cover -func=coverage.out` | 91.4% |
| Package coverage | config 92.7%, app 94.8%, cli 89.7%, logging 84.5%, buildinfo 100% | above 70% |
| Coverage gate | `COVERAGE_MIN=70 ./scripts/check-coverage.sh coverage.out` | passed, 91.4% ≥ 70% |
| `make check` | `make check` | passed |
| Lint | `golangci-lint run ./...` (v2.15.0) | `0 issues` |
| Build | `make build` | passed |
| Cross-build | `make cross-build` | five targets built |
| Vulnerability scan | `govulncheck ./...` | **blocked locally**: HTTP 403 from `vuln.go.dev` |
| Full verify | `make verify` | **not claimed**: fails at the blocked `govulncheck` step; constituent checks were run separately |
| Docker build | `make docker-build` | passed |
| Docker version | `docker run --rm abouei-checker:dev version` | correct metadata |
| Docker JSON | `docker run --rm abouei-checker:dev version --output json` | valid JSON, `status=success` |
| Workflow syntax | `actionlint` | passed |
| Git whitespace | `git diff --check` | clean |

CLI smoke tests additionally verified:

- `validate` with the example configuration (text and JSON) exits `0`;
- `validate` with an invalid file exits `2`, writes one `Error:` line, and
  keeps stdout empty in text mode;
- `validate --output json` on invalid input emits a valid JSON error envelope
  with `code=config_invalid` and safe details;
- missing explicit and default configuration exits `2` with a safe message and
  init hint;
- `init` creates the default file with mode `0600`, refuses overwrite without
  `--force`, and `--force` overwrites;
- configuration `logging.format: json` produces JSON diagnostics on stderr;
- `version` text/JSON remains valid and uncontaminated by logs.

## 8. Security Review and Remaining Risks

Implemented controls:

- file size bound and regular-file enforcement;
- symlink rejection for load and init targets;
- strict unknown-field and duplicate-key rejection;
- single-document enforcement;
- URL scheme/host/userinfo/fragment/length/character validation;
- header token/reserved/size/CRLF validation;
- environment-name grammar and missing/empty failure;
- non-recursive expansion so secrets cannot inject new references;
- resolved-value registration and safe errors that omit values and raw scalar
  snippets;
- no network, shell, database, or notification side effects in loading or
  validation;
- init overwrite protection and owner-only permissions;
- deterministic, bounded error aggregation.

Residual risks:

- Configuration is trusted operator input. An untrusted author can direct
  probes to internal networks, as documented in the SSRF trust model.
- Literal secrets in YAML are accepted (discouraged) and are not automatically
  registered because they cannot be distinguished from ordinary header values.
  They are still protected by structural exclusion at logging boundaries.
- Registration of all environment substitutions can over-redact common values;
  dedicated secret variables are recommended.
- The `Lstat`-then-open symlink check has a local TOCTOU window. Configuration is
  operator-controlled and the check is not treated as a privilege boundary.
- Local `govulncheck` could not execute; remote CI remains the authority for the
  vulnerability gate.

## 9. GitHub CI/CodeQL Status

Remote validation was performed on pull request #6 and on the promoted
branches.

| Workflow | Run | Branch/event | Result |
| --- | --- | --- | --- |
| CI | [39784874698](https://github.com/Erfan-Abouei/Abouei_Checker/actions/runs/39784874698) | feature branch, pull request #6 | success |
| CodeQL | [39784874737](https://github.com/Erfan-Abouei/Abouei_Checker/actions/runs/39784874737) | feature branch, pull request #6 | success |
| CI | [39786047251](https://github.com/Erfan-Abouei/Abouei_Checker/actions/runs/39786047251) | final report head, pull request #6 | success |
| CodeQL | [39786047301](https://github.com/Erfan-Abouei/Abouei_Checker/actions/runs/39786047301) | final report head, pull request #6 | success |
| CI | [39786386388](https://github.com/Erfan-Abouei/Abouei_Checker/actions/runs/39786386388) | `develop` push after PR merge | success |
| CodeQL | [39786386387](https://github.com/Erfan-Abouei/Abouei_Checker/actions/runs/39786386387) | `develop` push after PR merge | success |
| CI | [39786991513](https://github.com/Erfan-Abouei/Abouei_Checker/actions/runs/39786991513) | `main` promotion push | success |
| CodeQL | [39786991567](https://github.com/Erfan-Abouei/Abouei_Checker/actions/runs/39786991567) | `main` promotion push | success |
| CI | [39786998156](https://github.com/Erfan-Abouei/Abouei_Checker/actions/runs/39786998156) | `develop` synchronization push | success |
| CodeQL | [39786998221](https://github.com/Erfan-Abouei/Abouei_Checker/actions/runs/39786998221) | `develop` synchronization push | success |

CI jobs observed as successful: `Quality and tests`, `Lint`,
`Cross-platform build`, and `Container smoke test`. The `Quality and tests` job
includes the pinned `govulncheck` step, so the local 403 limitation is covered
by actual remote runs. CodeQL `Analyze Go (go)` completed successfully.

## 10. Scope Compliance

Implemented within Phase 2:

- configuration schema, discovery, parsing, expansion, validation,
  normalization, backend registry, `init`, `validate`, tests, and docs.

Not implemented:

- HTTP probe execution or network access during validation;
- monitoring scheduling or loops;
- failure streak processing, incidents, or state machines;
- SQLite, migrations, persistence, or database file creation;
- webhook dispatch, outbox, retries, or reminders;
- `check`, `monitor`, `status`, or `history`;
- release publication or version tags.

Configuration fields for later phases are accepted and validated but are
explicitly documented as having no runtime effect yet.

## 11. Final Git State and Commits

- Baseline `develop`: `8ea51b5` (Phase 1 merge).
- Feature branch: `feature/phase-2-configuration`.
- Pull request: [#6](https://github.com/Erfan-Abouei/Abouei_Checker/pull/6), merged
  into `develop` at `f77f2b7`.
- Implementation commits:
  - `0c69ef2` — `feat(config): add strict YAML loading, validation, and init template`
  - `a765062` — `feat(app): load configuration and apply configuration logging`
  - `a9ee037` — `feat(cli): add init and validate commands with JSON error envelopes`
  - `c135a24` — documentation and Phase 2 report
  - `2a4f648` — GitHub CI/CodeQL evidence update
- `develop` was promoted to `main` at merge commit `0d90c42`.
- `main` and `develop` were synchronized at `0d90c42`; this report-state update is
  the only subsequent docs-only change.
- No force-push, history rewrite, or tag was created. Feature branches were
  retained.

## 12. Remaining Issues

1. Local `govulncheck` remains blocked by HTTP 403 from `vuln.go.dev`. CI runs
   the pinned scan successfully; local-only environments still need access to
   the vulnerability database.
2. Docker runtime validation covers the `version` smoke path only; configuration
   mount and monitoring behavior remain later-phase gates.
3. Remote feature branches are intentionally retained after merges. Deleting
   them is a repository housekeeping decision, not a correctness requirement.
4. Dependabot may open follow-up dependency PRs; they are outside Phase 2 scope
   and require normal review.

## 13. Phase 3 Handoff and Dependencies

Phase 3 should consume `internal/config.Config` as an immutable input:

- use `Backend.Interval`, `Timeout`, `FailureThreshold`, `RecoveryThreshold`,
  `MaxResponseBytes`, `FollowRedirects`, `AcceptedStatusCodes`,
  `BodyMatch`/`Regexp()`, and `Notifications`;
- use `Runtime.MaxConcurrentProbes` for the global probe limit and
  `Runtime.ShutdownTimeout` for graceful shutdown;
- use `Storage.DatabasePath` only when a later persistence phase needs it;
- do not reload, mutate, or duplicate YAML policy;
- do not log headers, URLs, bodies, or resolved secrets;
- keep `validate` free of probe behavior.

The configuration schema is frozen for v1. New fields require a schema version
change and an ADR. Phase 3 should add probe/HTTP tests and raise the coverage
floor to 75%.
