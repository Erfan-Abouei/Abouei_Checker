# Testing and Quality Strategy

| Field | Value |
| --- | --- |
| Status | Phase 4 incident, persistence, and notification tests implemented; history/status and end-to-end suites planned for Phase 5+ |
| Go toolchain | 1.27.3 |
| Related | [Architecture](architecture.md), [Release checklist](release-checklist.md), [Security](security.md) |

Quality checks are evidence, not decoration. A check is reported as passed only
after it was actually executed in the stated environment.

## 1. Quality Objectives

- Catch regressions in configuration, probing, incident transitions,
  persistence, and delivery before merge.
- Exercise failure paths, not only happy paths.
- Keep tests deterministic and independent of production services.
- Maintain a meaningful coverage floor without rewarding trivial assertions.
- Verify every declared build target compiles, and separately verify runtime
  behavior for releases.
- Run security analysis and dependency vulnerability checks continuously.
- Keep CI least-privileged and free of embedded secrets.

## 2. Local Commands

| Command | Purpose |
| --- | --- |
| `make fmt` | Apply `go fmt`/`gofmt` |
| `make fmt-check` | Fail if Go files are not formatted |
| `make vet` | Run `go vet ./...` |
| `make lint` | Run `golangci-lint` with the pinned config |
| `make test` | Run unit tests |
| `make test-race` | Run tests with the race detector |
| `go test -race -shuffle=on -count=1 ./...` | Run race tests with randomized order |
| `make cover` | Generate `coverage.out` with atomic mode and race detection |
| `make cover-check` | Enforce the coverage floor |
| `make vuln` | Run `govulncheck ./...` |
| `make build` | Build `bin/abouei-checker` with linker metadata |
| `make cross-build` | Compile Linux amd64/arm64, macOS amd64/arm64, Windows amd64 into `dist/` |
| `make docker-build` | Build the local container image |
| `make check` | `fmt-check`, `vet`, `test-race`, `cover-check` |
| `make verify` | `check`, `lint`, `vuln`, `cross-build` |

`make check` requires only Go. `make verify` additionally requires
`golangci-lint` and `govulncheck` on `PATH`; these are intentionally not hidden
inside every one-line Go command.

Pinned tool versions:

| Tool | Version | Notes |
| --- | --- | --- |
| Go | 1.27.3 | `go.mod` and CI must agree |
| Cobra | 1.10.2 | Runtime dependency |
| golangci-lint | 2.15.0 | Configuration schema v2 |
| govulncheck | 1.9.0 | Must be current enough for Go 1.27 syntax |
| actionlint | 1.7.12 | Local workflow syntax validation; optional developer tool |
| `actions/checkout` | v7 | CI |
| `actions/setup-go` | v7 | CI |
| `actions/upload-artifact` | v7 | CI |
| `golangci/golangci-lint-action` | v9 | CI |
| `github/codeql-action` | v4 | CI |

Install `govulncheck` with:

```sh
go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
```

Install `golangci-lint` from its official release or installation instructions,
pinned to `v2.15.0`. Do not use an unpinned `latest` in CI.

## 3. Test Matrix

| Area | What must be covered | Approach | Phase |
| --- | --- | --- | --- |
| Build metadata | Version/commit/date defaults, platform, JSON/text output | Unit tests with injected values | 0 (done) |
| CLI framework | Root help, unknown commands, flag validation, exit mapping, broken writers | Unit tests with buffers | 0 (done) |
| CLI global foundation | `--config`, `--log-level`, `--output`, flag inheritance/defaults, help, stdout/stderr, safe error rendering | In-process command execution with injected writers and app factory | 1 (done) |
| Logging and redaction | Four levels, text/JSON handlers, UTC timestamps, stable fields, registered secrets, credential-shaped strings, error paths, concurrent use | Injected buffers and redactor tests | 1 (done) |
| Error model | Categories, stable codes, safe messages, wrapped causes, central exit mapping | Direct unit tests and table-driven mapping | 1 (done) |
| Configuration discovery | Explicit path, env override, defaults, no fallback, missing file, symlink rejection | Temp directories; `t.Setenv` | 2 (done) |
| Configuration schema | Required fields, unknown fields, duplicate keys, versions, limits, defaults inheritance, validation aggregation, init template | Table-driven decode/validate tests | 2 (done) |
| Secrets | `${NAME}` expansion, embedding, `$$`, missing/empty, malformed references, redaction in errors/JSON/logs | Unit tests with fake env and captured writers | 2 (done) |
| `init` / `validate` CLI | Safe creation, overwrite refusal, JSON/text output, discovery, no network or database side effects | In-process CLI tests with temp dirs and `httptest` counters | 2 (done) |
| HTTP probes | Accepted statuses, body contains/regex, timeouts, size limits, malformed responses, redirects, TLS failure | `httptest.Server` and controlled error servers | 3 |
| Scheduling | No overlapping probes per backend, global concurrency limit, cadence, cancellation | Fake clock/scheduler and atomic counters | 3 |
| Thresholds | Failure/recovery streaks, threshold of 1, cross-invocation persistence, restart | Pure state tests + temp SQLite | 3/4 |
| Incidents | Down, no duplicate Down, recovery threshold, sequences, one open incident | State-machine tests with injected clock/IDs | 4 |
| Notifications | Payload schema, ordering, retries, `Retry-After`, permanent failure, dead events | `httptest.Server` with scripted responses | 4 |
| Reminders | First due, cap, suppression, cancellation on recovery, restart catch-up | Fake clock + outbox tests | 4 |
| Duplicate prevention | Transactional outbox, unique constraints, idempotency key, restart | SQLite and webhook receiver tests | 4 |
| Persistence | Schema creation, pragmas, migrations, newer schema, corruption, rollback | Temp DB files; fault injection | 4/5 |
| Restart recovery | Open incident, still down, recovered, stale delivering, partial crash windows | Restart harness with same DB | 5 |
| Retention | Age boundaries, open incidents preserved, pending events preserved, batches | Temp DB with injected clock | 5 |
| CLI commands | Flags, output modes, exit codes, stdout/stderr separation | Execute command tree in-process | 1–5 |
| Status/history | Read-only behavior, filters, limits, empty DB, orphaned incidents | Temp DB fixtures | 5 |
| End-to-end | Binary startup, config, probes, incident, webhook, SQLite, JSON, exit codes | Build binary and run against local test servers | 6 |
| Docker | Build, non-root, version command, config mount, volume persistence, signals | CI + Docker test script | 0/7 |
| Cross-platform | All declared GOOS/GOARCH compile; runtime behavior on real targets for release | `make cross-build`; release runners/VMs | 0/7 |
| Security | Redaction, header/URL validation, redirects, body limits, TLS, file permissions | Unit + integration + review | 6 |
| Concurrency | Store/probe/worker races, shutdown cancellation | `go test -race`, stress tests | 3–6 |

## 4. Deterministic Tests

- Use `httptest.Server`/`httptest.NewTLSServer`, not production endpoints.
- Use `t.TempDir()` for SQLite and config files.
- Inject clocks and ID generators; do not depend on wall-clock sleeps for
  scheduling assertions.
- Use fake webhook receivers that assert headers and payloads and can return
  scripted status codes.
- Use `t.Setenv` for environment tests; never depend on the developer's real
  environment.
- Run tests in parallel when they do not share global state. `buildinfo` values
  are package-level test overrides and must be restored with `t.Cleanup`.
- Do not require network access in `go test ./...`.
- Docker and platform-runtime checks are separate commands/jobs and are reported
  separately.
- Avoid golden files that encode volatile timestamps; normalize or inject time.

## 5. Test Organization

- Unit tests live beside their packages (`*_test.go`).
- Pure domain tests (incident thresholds, payload encoding, config validation)
  use table-driven cases.
- Local HTTP integration tests use in-process test servers and live in the owning
  package or `test/integration`.
- End-to-end tests build/run the binary against local servers and a temporary
  database; they must not need root or Docker.
- Docker validation is a dedicated script/job, not a default unit test.
- Test helpers are unexported and colocated unless reused across packages. Shared
  helpers go in a small `internal/testutil` package only when real duplication
  appears.

## 6. Coverage Policy

- Phase 2 floor: **70% overall**, enforced by `scripts/check-coverage.sh` and
  `COVERAGE_MIN` in the Makefile.
- Phase 3 floor: **75% overall**, enforced the same way. Phase 3 measured 88.8%
  overall (`probe` 88.2%, `monitor` 80.6%, `cli` 84.6%, `app` 89.8%).
- Phase 4 floor: **80% overall and 80% in each core package** (`config`,
  `probe`, `monitor`, `incident`, `notify`, `store`), enforced by
  `scripts/check-coverage.sh` plus `scripts/check-package-coverage.sh` through
  `make cover-check` with `COVERAGE_MIN=80`. Phase 4 measured 86.4% overall
  (`config` 92.7%, `probe` 88.2%, `app` 87.8%, `cli` 84.9%, `incident` 88.2%,
  `notify` 81.9%, `store` 81.0%, `monitor` 80.6%).
- Phase 1 measured 89.4% overall; Phase 2 measured 91.5% overall (config
  package 92.8%). Exact command output is in the corresponding completion
  reports.
- Ratchet as implementation lands:
  - Phase 3: at least 75%.
  - Phase 4/5: at least 80%.
  - v1.0.0: at least 80% overall and at least 80% in core packages
    (`config`, `probe`, `monitor`, `incident`, `notify`, `store`).
- Coverage is a floor, not a target. Do not add assertion-free tests, duplicate
  executions, or tests of generated code to raise a number.
- Critical state transitions, redaction, and error mapping require explicit
  behavioral assertions even if coverage is already above the floor.
- The CI job uploads `coverage.out` as an artifact for inspection.
- The threshold is set in the Makefile (`COVERAGE_MIN`) and may only be lowered
  with documented justification in the same change.

## 7. Linting and Static Analysis

`.golangci.yml` uses golangci-lint v2 and the standard linter set plus:

- `bodyclose` — response body closure.
- `errorlint` — wrapping and error comparison.
- `gosec` — common security issues.
- `misspell`.
- `nilerr`.
- `revive`.
- `unconvert`.
- `wastedassign`.

Formatters: `gofmt` and `goimports` with the module path as the local prefix.

`go vet` remains mandatory and independent of the linter. `gofmt -l` must be
empty. Generated code, if introduced, must be documented and excluded explicitly
rather than disabling checks globally.

## 8. CI Gates

The CI workflow runs on pull requests and pushes to `main` and `develop`:

### Mandatory for every pull request

1. `gofmt` check.
2. `go vet ./...`.
3. `golangci-lint run ./...` with v2.15.0.
4. Unit/integration tests with `-race`.
5. Coverage floor.
6. `govulncheck ./...`.
7. Cross-compilation of all five declared targets.
8. Docker image build, `version` smoke test, and non-root configuration check.
9. CodeQL analysis (separate workflow; required branch check when configured).

### Additional release gates

- End-to-end and failure-injection suites.
- Runtime validation on each declared platform (not only cross-compilation).
- Docker persistence, config mount, read-only root filesystem, and signal tests.
- Image vulnerability scan and SBOM.
- Security review and release checklist evidence.
- Reproducible artifact verification and checksums.

CI uses `contents: read` by default. CodeQL receives only
`security-events: write`. No repository secrets are exposed to pull requests,
and workflows do not use `pull_request_target` with untrusted code. Actions are
version-pinned and updated by Dependabot. Workflows must fail when required
checks fail; `continue-on-error` is not acceptable for mandatory gates.

## 9. Vulnerability and Supply-Chain Checks

- `govulncheck ./...` is mandatory in CI. It must be a version that supports the
  Go language version in `go.mod`; v1.1.4 crashes on Go 1.27 syntax and is not
  acceptable.
- CodeQL uses the `security-extended` query set for Go.
- `gosec` runs through golangci-lint.
- Dependabot monitors Go modules, GitHub Actions, and Docker base images.
- Base images and tools are version-pinned. Release builds should pin image
  digests and produce an SBOM.
- A vulnerability finding must be fixed, explicitly accepted with rationale, or
  shown to be unreachable. It is not ignored to make CI green.

The current environment cannot reach `vuln.go.dev` (HTTP 403). This is reported
as blocked locally; CI is still configured to run the scan and is not claimed to
have passed until it executes.

## 10. Cross-Platform Verification

`make cross-build` proves compilation for:

- `linux/amd64`
- `linux/arm64`
- `darwin/amd64`
- `darwin/arm64`
- `windows/amd64`

Compilation is not runtime proof. Release validation must execute representative
binaries on each declared OS/architecture, including config loading, a local
probe, SQLite persistence, JSON output, and exit codes. If a target cannot be
runtime-validated, it must not be advertised as supported for that release.

## 11. Docker Verification

Phase 0 local/CI checks:

- `docker build` succeeds.
- Image runs as UID/GID `65532:65532`.
- `version` and `version --output json` execute in the scratch image.
- Image contains CA certificates and no shell/config/secrets.
- `.dockerignore` excludes VCS data, docs, local binaries, and data.

Later release checks:

- Mount config read-only and a named data volume.
- Run `monitor` with `--read-only`, `--cap-drop=ALL`, and
  `--security-opt=no-new-privileges`.
- Verify SQLite survives container recreation.
- Verify SIGTERM produces graceful shutdown.
- Verify webhook and probe connectivity from the container.
- Scan the image and record the result.

## 12. Workflow Validation

- `actionlint` validates workflow syntax and common expression mistakes locally.
- A clean `actionlint` run is not proof that GitHub Actions executed. CI is only
  reported as passing after a real workflow run.
- YAML formatting and permissions are reviewed manually.
- Workflow changes are tested on a branch before being required for merges.

## 13. Reporting Results

Every completion report must state, for each check:

- the exact command;
- whether it ran;
- the observed result;
- any environmental limitation.

Prohibited claims:

- "Tests pass" when they were not run.
- "CI passes" when no workflow executed.
- "Works on Windows/macOS" based only on cross-compilation.
- "No vulnerabilities" when the vulnerability database was unreachable.
- "Docker validated" when only the Dockerfile was reviewed.

## 14. Evidence Summary

### Phase 0

The Phase 0 report records actual results for:

- `gofmt`/`make fmt-check`
- `go vet ./...`
- `go test -race ./...`
- coverage and threshold
- `golangci-lint run ./...`
- `go build` and `version` smoke tests
- five-target cross-compilation and `file` inspection
- Docker build/run and non-root check
- `actionlint`
- `git diff --check`

`govulncheck` was blocked locally by the environment's HTTP 403 response from
`vuln.go.dev`; CI execution was pending the first push. CodeQL and the GitHub
Actions workflow likewise could not execute until the repository was pushed.

### Phase 1

The Phase 1 report records actual local and remote results, including:

- `gofmt -l cmd internal` (no output) and `go vet ./...`;
- `go test -race -count=1 ./...` and
  `go test -race -shuffle=on -count=1 ./...`;
- coverage generation and the 60% gate;
- `golangci-lint run ./...`;
- `make check`, `make build`, and `make cross-build`;
- text/JSON version smoke tests, debug stdout/stderr separation, and usage-error
  exit codes;
- `git diff --check`;
- GitHub Actions CI and CodeQL runs on pull request #1.

`make verify` cannot complete locally because `govulncheck` receives HTTP 403
from `vuln.go.dev`. The GitHub CI `Quality and tests` job executed the pinned
`govulncheck` step successfully, so that gap is closed remotely but is never
reported as a local pass.

### Phase 2

The Phase 2 report records actual local and remote results, including:

- strict-parsing tests for empty files/documents, multiple documents, wrong root
  types, unknown fields, duplicate keys, size limits, and wrong field types;
- discovery precedence, no-fallback, relative-path, permission, and symlink
  rejection tests;
- validation tests for versions, IDs, URLs, headers, status codes, body-match
  RE2 compilation, intervals, timeouts, thresholds, retention, notifications,
  and cross-field constraints;
- secret expansion tests for `${NAME}`, embedding, `$$`, missing/empty/malformed
  references, non-recursive substitution, alias handling, and redaction;
- CLI tests proving `validate` performs no HTTP request and creates no database,
  and that `init` refuses overwrite without `--force`;
- `go test -race`, shuffled race tests, coverage (91.5% overall, 92.8% config),
  `golangci-lint`, `make check`, `make build`, and `git diff --check`;
- GitHub Actions CI and CodeQL results on the Phase 2 pull request.
