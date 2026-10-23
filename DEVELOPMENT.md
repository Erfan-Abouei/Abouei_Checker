# Development Guide

## Prerequisites

- Go 1.27.3 (see `go.mod`; CI uses `go.mod` as the version file).
- `make`, `git`, and a POSIX shell.
- Optional: `golangci-lint` 2.15.0, `govulncheck` 1.9.0, Docker, Node.js 20+
  (only for the npm wrapper in `npm/`).

## Setup

```bash
git clone https://github.com/Erfan-Abouei/Abouei_Checker.git
cd Abouei_Checker
git checkout develop
go build ./...
```

## Everyday commands

| Command             | Purpose                                              |
| ------------------- | ---------------------------------------------------- |
| `make build`        | Build `bin/abouei-checker` with version metadata     |
| `make fmt`          | Format Go sources (`go fmt` + `gofmt`)               |
| `make fmt-check`    | Fail if formatting is dirty                          |
| `make vet`          | `go vet ./...`                                       |
| `make test`         | Unit + integration tests                             |
| `make test-race`    | Tests with the race detector                         |
| `make cover`        | Coverage profile to `coverage.out`                   |
| `make cover-check`  | Enforce the coverage floors (see `TESTING.md`)       |
| `make lint`         | `golangci-lint` (requires the pinned version)        |
| `make vuln`         | `govulncheck ./...` (needs network for the vuln DB)  |
| `make cross-build`  | Linux/macOS/Windows artifacts into `dist/`           |
| `make docker-build` | Local container image                                |
| `make check`        | `fmt-check`, `vet`, `test-race`, `cover-check`       |
| `make verify`       | `check` + `lint` + `vuln` + `cross-build`            |

## Running locally

```bash
# Scaffold a config, then edit it.
./bin/abouei-checker init --config ./abouei.yaml
./bin/abouei-checker validate --config ./abouei.yaml

# One-shot check (cron/CI friendly) and continuous monitor.
./bin/abouei-checker check --config ./abouei.yaml --output json
./bin/abouei-checker monitor --config ./abouei.yaml

# Read-only inspection (never probes or mutates).
./bin/abouei-checker status --config ./abouei.yaml
./bin/abouei-checker history --config ./abouei.yaml --limit 20

# Interactive dashboard (needs a TTY).
./bin/abouei-checker tui --config ./abouei.yaml
# Bare invocation opens the dashboard when interactive, else prints help.
./bin/abouei-checker
```

## Configuration and secrets

- Schema, defaults, and validation: `docs/configuration.md`.
- Example: `examples/` (validate any example with `validate` before use).
- Secrets come from `${ENV_VAR}` references; never commit literal tokens.
- Config discovery order and `--config`/`--log-level` behavior are covered
  by `docs/cli-contract.md`.

## Database and migrations

- SQLite lives at `storage.database_path` (default `./data/abouei.db`,
  relative to the config file directory).
- Migrations are in `internal/store/migrate.go`; `SupportedSchemaVersion`
  is the only writable version. A newer schema fails closed with upgrade
  guidance — never hand-edit `user_version`.
- `status`/`history` open the database read-only.

## Debugging

```bash
./bin/abouei-checker check --config ./abouei.yaml --log-level debug
```

Logs go to stderr (never stdout), timestamps are UTC, and secret values are
redacted. JSON output stays on stdout; piping `--output json` output to
`jq` must always succeed for `check`, `status`, `history`, and `version`.

## Docker

```bash
make docker-build
docker run --rm -v "$PWD/data:/data" abouei-checker:dev \
  check --config /data/abouei.yaml
```

The image runs as non-root (`65532:65532`). Persist the database with a
volume; see `docs/persistence.md`.

## Troubleshooting

- `make verify` fails on `vuln` with a network error: the vulnerability
  database is unreachable (common behind proxies). Record it as blocked;
  do not skip the other gates.
- `golangci-lint` version mismatch: install exactly 2.15.0; newer versions
  may apply different rules than CI.
- Windows builds compile but are runtime-validated only where CI runners
  exist; see `docs/release-checklist.md` for the platform matrix.
