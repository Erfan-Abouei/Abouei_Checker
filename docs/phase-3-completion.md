# Phase 3 Completion Report — Health Probe & Monitoring Engine

| Field | Value |
| --- | --- |
| Phase | 3 — Health Probe & Monitoring Engine |
| Branch | `feature/phase-3-health-check-engine` (from `develop` at `2f3580a`) |
| Scope | Safe HTTP probing, health-criteria evaluation, one-shot `check`, continuous `monitor`, in-memory threshold streaks |
| Out of scope | SQLite, durable incidents, webhook delivery, reminders, `status`/`history` |

## 1. What was built

- `internal/probe`: reusable securely-configured HTTP client (GET only, per-backend
  timeout via context, TLS verification always on, no implicit proxy,
  `DisableCompression`, 64 KiB response-header bound). Status evaluation against
  `accepted_status_codes` (2xx default), `contains`/RE2 body matching after an
  accepted status, `max_response_bytes` enforcement (`response_too_large` only
  when a matcher is configured; bounded drain otherwise), redirect policy
  (disabled by default via `http.ErrUseLastResponse`; when enabled: http/https
  only, no userinfo, no https→http downgrade, at most 5 redirects, cross-origin
  with custom headers rejected). Outcomes: `healthy`, `unhealthy`, `error`,
  `canceled`. No retries; headers copied per request and never logged.
- `internal/monitor`: race-safe in-memory streak tracker (`Record`/`Get`;
  canceled probes leave streaks unchanged), `RunOnce` one-shot fan-out bounded
  by `runtime.max_concurrent_probes` in configuration order, and `Scheduler`
  with one goroutine per backend, immediate first probe, start-to-start
  intervals, no same-backend overlap, and global concurrency bound.
- `internal/app`: `Check` (backend selection with unknown-ID usage errors before
  any request, per-backend logging with safe fields) and `Monitor`
  (startup/shutdown logs, shutdown bounded by `runtime.shutdown_timeout`).
- `internal/cli`: `check [BACKEND_ID...]` (text + JSON result envelope,
  `incident_status: "none"`, incident/notification counters `0`, exit 1 on any
  non-healthy probe) and `monitor` (text lines + NDJSON `started`/`probe`/
  `shutdown` events, exit 0 on SIGINT/SIGTERM). Results on stdout, diagnostics
  on stderr; already-written payloads are not double-enveloped.
- `internal/logging`: allowlist extended with `consecutive_failures`,
  `failure_threshold`, `threshold_reached` (non-sensitive streak reporting).
- Coverage floor raised 70% → 75% (`Makefile`, `COVERAGE_MIN`).

## 2. Local verification (all executed)

- `gofmt -l cmd internal` — clean.
- `go vet ./...` — clean.
- `golangci-lint run ./...` (v2.15.0) — 0 issues.
- `go test -race -count=1 ./...` — all packages pass.
- `go test -race -shuffle=on -count=1 ./...` — all packages pass (order-independent).
- Coverage: **88.8% overall** (`probe` 88.2%, `monitor` 80.6%, `cli` 84.6%,
  `app` 89.8%, `config` 92.7%, `logging` 84.5%) — above the 75% gate.
- `make build` — binary builds; `check`/`monitor --help` verified.
- `make cross-build` — five targets compile (see CI for authoritative run).
- `git diff --check` — clean.
- Manual smoke tests with local servers: mixed healthy/unhealthy `check`
  (exit 1, valid JSON, stdout/stderr separation, secret redaction) and
  `monitor` SIGINT shutdown (exit 0, NDJSON valid, startup/shutdown events).
- `govulncheck ./...` — blocked locally (HTTP 403 from `vuln.go.dev`); not
  claimed as passing. Remote CI runs the pinned scan.

## 3. Security properties verified by tests

- Custom headers are sent; secrets never appear in results, errors, JSON, or logs.
- TLS verification cannot be disabled (no such option; self-signed servers fail
  as `tls` errors).
- Redirect policy unit-tested for all branches; cross-origin with secrets rejected.
- Response bodies bounded and never logged; oversized-with-matcher fails safely.
- No retries: one request per probe (server hit counter).
- URL policy reused from Phase 2 validation; private-network monitoring remains
  allowed per the trusted-configuration model.

## 4. Known limitations

- Threshold streaks are in-memory only and reset on restart (Phase 4 persists them).
- `check` reports `incident_status: "none"` and zero incident/notification
  counters; exit 4 never applies in this phase.
- No SQLite, incidents, webhooks, reminders, `status`, or `history` yet.
- Forced second-signal paths (130/143) and Windows signal behavior are
  implemented by the runtime contract but not exercised locally.

## 5. Handoff to Phase 4

Phase 4 consumes `probe.Result` and the streak semantics (`Record` rules,
`ThresholdReached`) and adds the transactional store slice
(`backend_state`, `incidents`, `notification_events`), the incident state
machine, the webhook worker, and durable `check`/`monitor` behavior.
