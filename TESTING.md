# Testing Guide

## Layout

- **Unit tests** live next to the code they cover (`*_test.go` in each
  `internal/` package): config validation, probe classification, incident
  transitions, retry classification, store queries, CLI envelopes.
- **Integration tests** live in `internal/integration/`:
  - `phase6_test.go` — cross-component journeys through the real CLI tree:
    Down/Recovery across restarts, transient/permanent webhook failures,
    failure-injection matrix, backend isolation, invalid-config guard,
    cancellation safety, monitor shutdown.
  - `binary_test.go` — the compiled binary (`TestMain` builds it once):
    `version`, `init`, `validate`, `check`, `status`, `history`, unknown
    commands, and concurrent runs sharing one database.
- **Fixtures** are inline builders (e.g. `baseConfig`, `webhookRecorder`)
  so each test is hermetic: temp dirs, `httptest` servers, isolated env.

## Commands

```bash
make test          # everything, no race detector
make test-race     # go test -race ./...
make cover         # atomic coverage profile with race detector
make cover-check   # enforce floors: 60% overall, 80% per core package
```

Core packages held to 80%: `internal/config`, `internal/probe`,
`internal/monitor`, `internal/incident`, `internal/notify`,
`internal/store`. Current overall coverage is ~80%; see the coverage log
in the Phase 6 completion report for the exact measured value.

Randomized order for flake hunting:

```bash
go test -race -shuffle=on -count=1 ./...
```

## Failure injection

Covered deterministically (no public network, no sleeps except one
documented backoff wait):

- Connection refused, DNS failure (`nonexistent.invalid`), timeouts,
  malformed HTTP (raw TCP garbage), oversized bodies with matchers.
- Webhook 500s then success (pending survives restart, delivered once),
  permanent 400s (dead letter, no retry storm).
- Canceled contexts during `check` and `monitor`; shutdown emits valid
  NDJSON including a `shutdown` event.
- Concurrent fresh-database creation by 8 processes (DDL race adoption +
  bounded open retry) and concurrent `check` runs sharing one database.

`TestTransientWebhookFailureDeliveredOnceAcrossRestart` sleeps 500ms past
the 100ms capped retry backoff so the retried delivery is deterministically
due. This is the only timing-dependent test; the wait is documented
inline and the backoff cap is asserted by the config it writes.

## Security regression tests

- Payload redaction: webhook bodies contain no headers, URLs, secrets, or
  response bodies (`TestCheckPayloadRedactsSecrets`,
  `TestDownThenRecoveryAcrossRestarts`).
- Error/output redaction: invalid-config and failure outputs never echo
  secret env values.
- `status`/`history` never probe or mutate; JSON envelopes stay valid and
  stdout/stderr separated.

## Local vs. CI

| Check                  | Local (`make verify`) | CI (`.github/workflows`) |
| ---------------------- | --------------------- | ------------------------ |
| `fmt-check`, `vet`     | yes                   | yes                      |
| `test-race`, coverage  | yes                   | yes                      |
| `golangci-lint`        | yes (pinned 2.15.0)   | yes (pinned action)      |
| `govulncheck`          | yes, needs network    | yes                      |
| `cross-build`          | yes                   | yes                      |
| Docker smoke test      | optional              | yes                      |
| CodeQL                 | no (GitHub only)      | yes                      |

## Known limitations

- `govulncheck` cannot reach the vulnerability database behind restrictive
  proxies (observed HTTP 403 from the module proxy). Record as blocked;
  it is not a pass.
- Windows/macOS coverage is compile-only locally; runtime evidence comes
  from CI runners and release validation.
- The interactive TUI is covered through model/update unit tests plus a
  no-TTY guard test; fullscreen rendering is verified manually and in CI
  smoke form, not pixel-asserted.
