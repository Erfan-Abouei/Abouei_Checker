# Product Requirements

| Field | Value |
| --- | --- |
| Project | AboueiChecker (`Abouei_Checker`) |
| Document status | Approved Phase 0 baseline |
| Product status | Phase 2: configuration, `init`, and `validate` implemented; monitoring pending |
| Last updated | 2026-10-22 |

This document records the approved product requirements and the Phase 0 scope. It
is the entry point for the other specifications. Detailed contracts are kept in
one canonical document each and linked below rather than duplicated here.

## 1. Purpose and Users

AboueiChecker is an open-source command-line tool that monitors the health of
multiple backend HTTP endpoints, detects failures and recoveries, stores
monitoring history and incident state locally, and sends configurable generic
webhook notifications.

Intended users:

- Developers and operators who need a small, self-hosted health monitor.
- Homelab and small-team operators who want a single binary and local SQLite
  storage instead of a hosted monitoring platform.
- Automation authors who need stable JSON output and deterministic exit codes.
- Contributors who need explicit architectural and quality boundaries.

The tool is designed for personal development and public open-source
distribution. It is not a hosted service and does not require an account.

## 2. Supported Use Cases

- Probe several independent backends from one process.
- Run a one-shot check suitable for cron or CI scripts.
- Run a continuous monitor with per-backend intervals.
- Accept normal HTTP status codes and optional response-body conditions as health
  criteria.
- Require consecutive failures before declaring an incident, and consecutive
  successes before declaring recovery.
- Authenticate probes with arbitrary custom headers whose secret values come
  from environment variables.
- Deliver Down, Recovery, and optional reminder notifications to a generic HTTP
  webhook.
- Persist observations, failures, incidents, and notification delivery state in
  SQLite.
- Inspect current status and historical records in human-readable or JSON form.
- Run as a standalone binary or as a non-root Docker container with a persistent
  data volume.

## 3. Approved Product Decisions

The following 15 decisions are authoritative. They were approved before Phase 0
and are not changed by this document.

### Decision 1 — Identity and implementation language

- Product name: **AboueiChecker**.
- GitHub repository: `Abouei_Checker`, owned by `@Erfan-Abouei`.
- Module path: `github.com/Erfan-Abouei/Abouei_Checker`, matching the real
  repository path.
- Executable, container image, and release artifact base name:
  `abouei-checker`.
- Implementation language: Go.
- License: MIT, as confirmed by the repository's existing `LICENSE` file
  (Copyright (c) 2026 Erfan Abouei).

Naming is intentionally split only by context: the product is written
"AboueiChecker", while machine-facing identifiers use the kebab-case
`abouei-checker`. The repository and module retain the approved underscore name.

### Decision 2 — Runtime and distribution

Two distribution models are required:

1. Standalone executables that do not require Go on the target machine.
2. Docker-based execution.

SQLite data must be stored outside a container's writable layer. Configuration
is supplied externally to the container. The initial platform matrix is Linux
`amd64`/`arm64`, Windows `amd64`, and macOS `amd64`/`arm64`, subject to Go
compilation evidence and, for releases, runtime validation.

### Decision 3 — Notification transport

The initial notification transport is a generic HTTP webhook. No messaging
provider is hard-coded. The payload is versioned JSON. The canonical contract is
[incident-lifecycle.md](incident-lifecycle.md#7-webhook-payload-contract).

### Decision 4 — Configuration and persistence

- YAML is the user-editable configuration source of truth.
- SQLite is the persistence source of truth for observations, incident state,
  backend threshold state, and durable notification work.
- Configuration is never copied into SQLite as a second source of truth.
- There is no database abstraction layer for hypothetical future engines.

The canonical configuration contract is [configuration.md](configuration.md).
The persistence contract is [persistence.md](persistence.md).

### Decision 5 — Execution modes

- **One-shot:** perform one monitoring evaluation and exit. `check` is the
  one-shot command.
- **Continuous:** remain running, probe on per-backend schedules, and shut down
  gracefully on SIGINT/SIGTERM. `monitor` is the continuous command.

One-shot and continuous behavior, output, and exit codes are defined in
[cli-contract.md](cli-contract.md). The monitoring loop is not implemented in
Phase 0.

### Decision 6 — Configurable health criteria

Health criteria are configurable per backend:

- Accepted HTTP status codes. If no explicit set is supplied, `2xx` is the
  default.
- Optional response-body matching. The initial contract supports `contains` and
  `regex` (RE2) only; it is not a general matching language.

Response-size limits and malformed or unexpected responses are explicit
reliability and safety requirements. The exact semantics are in
[configuration.md](configuration.md#55-health-criteria) and
[security.md](security.md#7-http-client-and-probe-safety).

### Decision 7 — Backend-specific monitoring policy

Each backend can define:

- Monitoring interval.
- Request timeout.
- Consecutive failures required to declare Down.

Global defaults are defined once and can be overridden per backend. An
individual failed probe is not an incident; an incident is created only after
the configured failure threshold is reached. Probes for the same backend must not
overlap by default. Independent backends may be probed concurrently under a
global concurrency limit.

Threshold state is persisted with the backend identity so that one-shot runs and
restarts do not silently reset a failure or recovery streak. See
[incident-lifecycle.md](incident-lifecycle.md#3-thresholds-and-state-transitions).

### Decision 8 — Authentication and secret handling

Each backend supports arbitrary custom HTTP request headers, including
`Authorization` and `X-API-Key`.

YAML string values support environment-variable references using `${NAME}`.
Missing or empty variables are configuration errors; they never silently become
empty credentials. Literal values remain possible but are discouraged.

Secret values, authorization headers, secret-bearing URLs, and sensitive
response bodies must never appear in logs, errors, JSON output, diagnostics, or
notification payloads. Configuration validation reports field paths, not values.
The canonical rules are in
[configuration.md](configuration.md#56-secret-references) and
[security.md](security.md#3-secrets-and-redaction).

### Decision 9 — CLI command structure

The intended command surface is:

`init`, `validate`, `check`, `monitor`, `status`, `history`, `version`.

Cobra is the selected CLI framework because it provides a maintained command
tree, help generation, and flag parsing without imposing application
architecture. The full contract is [cli-contract.md](cli-contract.md).

Phase 0 implemented only the root command and `version`; Phases 1–2 added the
shared foundation, `init`, and `validate`. Operational monitoring commands
remain planned and must not be presented as implemented.

### Decision 10 — Human-readable and machine-readable output

Human-readable output is the default. Structured output is selected with
`--output json`.

Automation must be able to distinguish at least:

- Successful execution.
- Unhealthy monitored backends.
- Invalid configuration or usage.
- Internal operational errors.
- Notification delivery failure during a one-shot command.

Exact numeric exit codes are defined in
[cli-contract.md](cli-contract.md#5-exit-codes). In JSON mode, stdout contains
only valid JSON for structured results; diagnostics and logs go to stderr.

### Decision 11 — Notification policy and duplicate prevention

Notification policy is configurable per backend with global defaults:

- Notify on Down.
- Notify on Recovery.
- Optional recurring reminders during an outage.
- Prevention of unnecessary duplicate alerts.

Notifications are generated by incident transitions, not individual failed
probes. Delivery is at-least-once, never claimed as exactly-once. Durable event
identity, an `Idempotency-Key` header, and a retry/reconciliation strategy are
required. The canonical semantics are in
[incident-lifecycle.md](incident-lifecycle.md).

### Decision 12 — Restart recovery and reconciliation

On restart, the application must:

1. Restore open incidents and backend threshold state from SQLite.
2. Immediately recheck all configured backends.
3. Reconcile observed health with persisted state.
4. Generate notifications from actual transitions, not by replaying old alerts.
5. Avoid duplicate Down notifications caused solely by restart.

Delivery failures and partial persistence must be handled through durable state
and transactional writes. The lifecycle is in
[incident-lifecycle.md](incident-lifecycle.md#8-restart-reconciliation).

### Decision 13 — Structured logging

Use four levels: `DEBUG`, `INFO`, `WARN`, `ERROR`. Use the Go standard library
`log/slog`; no additional logging dependency is justified.

Records include a UTC timestamp, level, message, and contextual fields such as
backend ID, incident ID, probe duration, outcome, and safe error detail. Logs go
to stderr so stdout remains machine-readable. Redaction rules are in
[security.md](security.md#3-secrets-and-redaction).

### Decision 14 — Engineering quality and CI

The project maintains:

- `gofmt` checks.
- `go vet`.
- A maintained linter (`golangci-lint`).
- Unit and integration tests.
- Race detection.
- Coverage generation and a threshold check.
- `govulncheck`.
- Cross-platform compilation checks.
- GitHub Actions CI with least-privilege permissions.
- CodeQL and dependency update automation.
- Docker build and smoke validation in CI.

Local verification is available through `make check` and `make verify`. Tool
versions and check responsibilities are in
[testing-and-quality.md](testing-and-quality.md).

### Decision 15 — Strict `v1.0.0` release criteria

`v1.0.0` requires automated unit, integration, and end-to-end evidence covering
the Down/Recovery lifecycle, duplicate-alert prevention, restart recovery,
invalid configuration, network failures, and webhook delivery failure/recovery.
It also requires a security review, race and dependency checks, Docker
validation, validation of released binaries on declared platforms, complete
English documentation, reproducible artifacts, and a documented release
procedure.

Compilation alone is never release evidence. The checklist is
[release-checklist.md](release-checklist.md). Phase 0 does not publish a release.

## 4. Phase 0 Scope

Phase 0 establishes the repository foundation, specifications, architecture,
minimal executable bootstrap, tooling, CI, Docker design, and handoff. The
completed artifacts and verification evidence are reported at the end of the
Phase 0 work; this document describes scope only.

### In scope

- Repository and toolchain discovery.
- Requirements, architecture, configuration, CLI, incident/notification,
  persistence, security, testing, release, and roadmap specifications.
- Go module and minimal `version` command.
- Package ownership boundaries and dependency direction.
- Development tooling and quality gates.
- CI configuration and dependency automation.
- Docker foundation and cross-platform build design.
- README, contributor documentation, and security policy.
- Minimal scaffolding verified by real commands.

### Out of scope for Phase 0

- Monitoring scheduler and continuous loop.
- HTTP probe engine and body matching engine.
- Failure-threshold and recovery-threshold evaluation.
- Incident state machine implementation.
- Notification delivery and retry worker.
- SQLite schema implementation and repository operations.
- Restart reconciliation implementation.
- Operational `init`, `validate`, `check`, `monitor`, `status`, and `history`
  behavior.
- End-to-end or production use.
- Release publication or deployment.

Interfaces, contracts, schemas, and acceptance criteria are defined where they
are required for later implementation. Incomplete future features are not
implemented merely to make the repository appear complete.

## 5. Constraints and Assumptions

### Constraints

- A single process owns a SQLite database at a time. SQLite is not used as a
  multi-instance coordination mechanism.
- The initial product exposes one global webhook endpoint; policy can differ per
  backend, but multiple webhook destinations are deferred.
- Probes use HTTP `GET` and do not send a request body.
- Configuration and webhook consumers are operator-controlled.
- The CLI is unauthenticated and intended for local or trusted operator use.
- All timestamps are persisted in UTC; local time is a presentation concern.
- The initial executable requires Go 1.27.3 to build from source.
- Network access is required for dependency downloads, vulnerability database
  updates, webhook delivery, and endpoint probes. Tests must not depend on
  uncontrolled external services.

### Assumptions to review in later phases

- One AboueiChecker process per database is sufficient for the intended users.
- A default recovery threshold of one consecutive successful probe is
  acceptable. It is configurable, but the default is a Phase 0 technical choice.
- Persisting pre-incident failure and recovery streaks is required for coherent
  one-shot behavior and restart recovery.
- The webhook receiver supports idempotent handling by event ID; duplicates are
  possible and expected under retry.
- A generic JSON body matcher with `contains` and RE2 `regex` is sufficient for
  v1. JSONPath and complex expressions are deferred.
- Following redirects is disabled by default for health probes.
- The initial container image can run on a scratch base with a static binary and
  CA certificates; no shell or package manager is required.
- The release process can validate Windows and macOS binaries on real runners or
  equivalent environments; cross-compilation is only compile evidence.

## 6. Non-Goals for v1

- Hosted monitoring, accounts, billing, or multi-tenant isolation.
- Web UI, dashboard, or graphical application.
- Distributed or highly available monitoring.
- Push-based agents, synthetic browser checks, or APM tracing.
- Log aggregation, metrics export, or alert escalation policies.
- PagerDuty/Slack/Discord-specific integrations.
- Kubernetes operator or service discovery.
- Configuration hot reload.
- Multiple webhook destinations or notification channels.
- Exactly-once webhook delivery.
- Built-in TLS client certificates or custom CA configuration.
- HTTP methods other than `GET`, request bodies, or authentication flows.
- Configurable response parsing beyond the documented health criteria.
- Automatic remediation of failing backends.
- Release publication during Phase 0.

## 7. Decision Classification

The project uses four labels to prevent approved requirements from being
confused with implementation choices:

| Label | Meaning | Examples |
| --- | --- | --- |
| Approved | Product owner decision; changing it requires explicit approval | The 15 decisions above |
| Technical choice | Phase 0 engineering decision consistent with approved requirements | Cobra, `log/slog`, Go 1.27.3, `modernc.org/sqlite`, UUIDv4 event IDs, scratch container |
| Assumption | Working belief that needs review if evidence changes | Single process per database, recovery default of 1, global webhook endpoint |
| Deferred | Deliberately postponed with a future phase or review trigger | HMAC signing, custom CAs, JSONPath, multi-destination webhooks, release automation |

Technical choices and assumptions are recorded in the relevant specification and,
where they constrain implementation materially, in an Architecture Decision
Record under [decisions/](decisions/README.md).

## 8. Traceability

| Decision | Canonical contract | Implementation phase |
| --- | --- | --- |
| 1 Identity/language | This document; README | Phase 0 (metadata) |
| 2 Runtime/distribution | [architecture.md](architecture.md), [release-checklist.md](release-checklist.md) | Phase 0 design; Phase 7 release |
| 3 Webhook transport | [incident-lifecycle.md](incident-lifecycle.md) | Phase 4 |
| 4 YAML + SQLite | [configuration.md](configuration.md), [persistence.md](persistence.md) | Phases 2 and 4/5 |
| 5 Execution modes | [cli-contract.md](cli-contract.md) | Phases 3 and 5 |
| 6 Health criteria | [configuration.md](configuration.md) | Phase 3 |
| 7 Monitoring policy | [configuration.md](configuration.md), [incident-lifecycle.md](incident-lifecycle.md) | Phase 3 |
| 8 Auth/secrets | [security.md](security.md) | Phases 2 and 3 |
| 9 CLI surface | [cli-contract.md](cli-contract.md) | Phase 0 (`version`); Phases 1–5 |
| 10 Output/exit codes | [cli-contract.md](cli-contract.md) | Phase 0 (`version`); Phase 1 shared; later commands |
| 11 Notification policy | [incident-lifecycle.md](incident-lifecycle.md) | Phase 4 |
| 12 Restart recovery | [incident-lifecycle.md](incident-lifecycle.md), [persistence.md](persistence.md) | Phases 4–5 |
| 13 Logging | [architecture.md](architecture.md), [security.md](security.md) | Phase 1 |
| 14 Quality/CI | [testing-and-quality.md](testing-and-quality.md) | Phase 0 foundation; all phases |
| 15 v1.0.0 gates | [release-checklist.md](release-checklist.md) | Phase 7 |

## 9. Change Control

- Approved decisions change only with explicit project-owner approval.
- Technical choices that materially constrain implementation are recorded in an
  ADR and updated in the relevant canonical document.
- Documentation must describe the current implementation state. Planned
  functionality is always labeled as planned.
- No phase may silently relax a security, exit-code, persistence, or release
  requirement to make a check pass.
