# AboueiChecker

AboueiChecker (`abouei-checker`) is an open-source Go CLI for monitoring backend
HTTP health endpoints, detecting outages and recoveries, keeping local SQLite
history, and sending configurable generic webhook notifications.

> **Project status: Phase 4 durable incidents, SQLite persistence, and webhook notifications complete.**
>
> **Release status:** `main` intentionally contains no production release yet.
> Active implementation and verification continue on the `develop` branch.
>
> `abouei-checker version`, `init`, `validate`, `check`, and `monitor`,
> root/command help, global `--config`/`--log-level` flags, strict YAML
> loading, defaults/inheritance, environment-reference resolution, validation,
> structured logging, redaction, centralized exit-code mapping, the safe HTTP
> probe engine, the per-backend scheduler, threshold-driven incidents, SQLite
> persistence with migrations, and generic webhook notifications with durable
> retries are implemented. `status`, `history`, retention, and backup guidance
> are specified but **not yet available**. Do not use this repository
> for production monitoring yet.

## Why AboueiChecker

The intended v1.0.0 tool will:

- Monitor multiple independent backends from one process.
- Run one-shot checks suitable for cron and CI.
- Run a continuous monitor with per-backend intervals and graceful shutdown.
- Accept configurable status codes and optional response-body conditions.
- Require consecutive failures before declaring an incident and consecutive
  successes before recovery.
- Authenticate probes with custom headers whose secrets come from environment
  variables.
- Send generic JSON webhooks for Down, Recovery, and optional reminders.
- Store observations, incidents, threshold state, and delivery work in SQLite.
- Provide human-readable output and stable JSON output with documented exit
  codes.
- Run as a standalone binary or a non-root Docker container with persistent
  storage.

This is a small, self-hosted tool—not a hosted monitoring platform, APM, or
distributed observability system.

## Current Implementation

| Capability | Status |
| --- | --- |
| `version` (text and JSON) | Implemented |
| `init` configuration template | Implemented |
| `validate` configuration | Implemented |
| Root help and command help | Implemented |
| Shell completion for bash, zsh, fish, and PowerShell | Implemented |
| Global `--config`, `--log-level`, `--output` flags | Implemented |
| YAML loading, discovery, defaults, and validation | Implemented |
| Environment-reference expansion and redaction registration | Implemented |
| Structured logging and redaction foundation | Implemented |
| Shared error categories and exit-code mapping | Implemented |
| `check`, `monitor` (durable incidents + webhook delivery) | Implemented |
| `status`, `history` | Planned — not available |
| HTTP probing and scheduling | Implemented |
| Incidents, webhooks, retries | Implemented (generic webhook, at-least-once, bounded retries) |
| SQLite persistence and migrations (migration 1) | Implemented |
| Release binaries and container releases | Not published |

`--config` selects an explicit file; when it is omitted, `ABOUEI_CONFIG`,
`./abouei.yaml`, and the platform user configuration directory are searched in
that order. `validate` reads and validates configuration but never probes an
endpoint, opens SQLite, or sends a notification.

The detailed behavior contracts are complete even where implementation is
pending:

- [Product requirements](docs/requirements.md)
- [Architecture](docs/architecture.md)
- [Configuration](docs/configuration.md)
- [CLI contract](docs/cli-contract.md)
- [Incident and notification lifecycle](docs/incident-lifecycle.md)
- [Persistence and migrations](docs/persistence.md)
- [Testing and quality](docs/testing-and-quality.md)
- [Security](docs/security.md)
- [Roadmap](docs/roadmap.md)
- [Release checklist](docs/release-checklist.md)
- [Phase 0 completion report](docs/phase-0-completion.md)
- [Phase 1 completion report](docs/phase-1-completion.md)
- [Phase 2 completion report](docs/phase-2-completion.md)
- [Phase 3 completion report](docs/phase-3-completion.md)
- [Architecture decisions](docs/decisions/README.md)

## Installation (Current Phase)

### From source

Requirements: Go 1.27.3 and `make`.

```sh
make build
./bin/abouei-checker version
./bin/abouei-checker version --output json
```

Build metadata is injected from the current commit and UTC build time. `dev` is
the default version for local builds.

### Docker (version smoke test only)

```sh
docker build -t abouei-checker:dev .
docker run --rm abouei-checker:dev version
docker run --rm abouei-checker:dev version --output json
```

The runtime image is `scratch`-based, runs as UID/GID `65532:65532`, and contains
only the static binary and CA certificates. It has no shell, configuration, or
database. In environments where Docker Hub is unavailable, the build stage image
can be overridden:

```sh
docker build --build-arg GO_IMAGE=public.ecr.aws/docker/library/golang:1.27.3 \
  -t abouei-checker:dev .
```

There is no published image or binary release at this stage.

## Usage

Current commands:

```sh
abouei-checker version
abouei-checker version --output json
abouei-checker version --log-level debug
abouei-checker init
abouei-checker init ./config/abouei.yaml
abouei-checker init ./abouei.yaml --force
abouei-checker validate
abouei-checker validate --config ./abouei.yaml
abouei-checker validate --config ./abouei.yaml --output json
abouei-checker help
abouei-checker validate --help
```

`--log-level` accepts `debug`, `info`, `warn`, and `error` case-insensitively.
An explicit flag overrides `logging.level`; otherwise configuration supplies the
level and format. Logs go to stderr; `--output json` keeps stdout
machine-readable.

### Shell completion

Cobra-generated completion is available for bash, zsh, fish, and PowerShell.
Generate the script with the matching subcommand and load it in the usual way
for your shell:

```sh
# bash (current session)
source <(abouei-checker completion bash)

# zsh (current session; completion system must be initialized)
source <(abouei-checker completion zsh)

# fish
abouei-checker completion fish | source
```

For a persistent installation, write the generated script to your shell's
completion directory. Run `abouei-checker completion <shell> --help` for the
upstream Cobra instructions.

Command surface:

| Command | Status | Intended purpose |
| --- | --- |
| `check [BACKEND_ID...]` | Implemented | Probe selected backends once, persist state, attempt due notifications, and exit |
| `monitor` | Implemented | Run continuous scheduling, notifications, and graceful shutdown |
| `status` | Planned (Phase 5) | Read current backend and incident state from SQLite |
| `history` | Planned (Phase 5) | Read observations and incident transitions from SQLite |

The exact flags, output, and exit semantics are in the
[CLI contract](docs/cli-contract.md).

## Configuration

YAML is the single configuration source of truth. A safe minimal example:

```yaml
version: 1

defaults:
  interval: 60s
  timeout: 10s
  failure_threshold: 3
  recovery_threshold: 1

backends:
  - id: example-api
    name: Example API
    url: https://api.example.com/health
    accepted_status_codes: [200]
    notifications:
      enabled: false
```

`abouei-checker init` writes a commented template that is valid immediately.
`abouei-checker validate` loads and validates a file without network or database
access. A fuller example is in [examples/abouei.yaml](examples/abouei.yaml). The
canonical schema, defaults, validation rules, and environment-reference behavior
are in [configuration.md](docs/configuration.md).

Discovery order is `--config`, `ABOUEI_CONFIG`, `./abouei.yaml`, then
`<user-config-dir>/abouei-checker/config.yaml`. Explicit sources never fall back.
The file must be a regular file; symbolic links are rejected. Relative database
paths resolve against the directory containing the configuration file.

Fields for probing, incidents, persistence, and webhook delivery are validated
now but have no runtime effect until their phases are implemented.

### Secrets

Use environment references instead of literals:

```yaml
headers:
  Authorization: "Bearer ${EXAMPLE_API_TOKEN}"
  X-API-Key: ${EXAMPLE_API_KEY}
```

Missing or empty variables are configuration errors. Secret values must never be
committed, logged, included in JSON output, or stored in SQLite. See
[docs/security.md](docs/security.md) for the full handling rules.

## Exit Codes and JSON

The public contract defines:

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Unhealthy backend or open incident |
| `2` | Invalid configuration or usage |
| `3` | Internal/operational error |
| `4` | Notification delivery failure during a one-shot command |
| `130` / `143` | Forced SIGINT / SIGTERM during continuous mode |

In JSON mode, structured results are written to stdout and logs/diagnostics go to
stderr. The `version` command already follows this discipline, including when
debug logging is enabled. The full JSON envelope and per-command data shapes are
in [cli-contract.md](docs/cli-contract.md#6-json-output-contract).

## Docker and Persistent Storage (Designed, Not Yet Operational)

The final tool must not store SQLite data in a container's writable layer. The
intended deployment mounts:

- configuration read-only at `/etc/abouei/abouei.yaml`;
- a named volume at `/data`, with the database at `/data/abouei.db`.

The image sets `ABOUEI_CONFIG=/etc/abouei/abouei.yaml` and runs as UID/GID
`65532:65532`. Because the data directory is owned by that numeric user in the
image, named volumes inherit writable ownership. Bind mounts must be writable by
UID 65532.

Once `monitor` is implemented, the intended hardened invocation is:

```sh
docker run -d --name abouei-checker \
  --read-only \
  --cap-drop=ALL \
  --security-opt=no-new-privileges \
  --user 65532:65532 \
  -v abouei-data:/data \
  -v "$PWD/abouei.yaml:/etc/abouei/abouei.yaml:ro" \
  abouei-checker:dev monitor
```

This command is **not functional yet** because `monitor` and configuration
loading are not implemented. The image's default command is `version` so the
foundation can be smoke-tested safely. Backup and persistence details are in
[docs/persistence.md](docs/persistence.md#9-docker-persistence).

## Development

Requirements:

- Go 1.27.3
- GNU Make
- Optional: Docker, `golangci-lint` v2.15.0, `govulncheck` v1.9.0, `actionlint`
  v1.7.12

```sh
make check       # gofmt check, vet, race tests, coverage threshold
make verify      # check + lint + vulnerability scan + cross-build
make docker-build
```

`make lint` requires `golangci-lint` on `PATH`; `make vuln` requires
`govulncheck` v1.9.0 or newer. They are intentionally not hidden requirements of
every Go command. CI pins them.

Tool installation examples:

```sh
go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
# Install golangci-lint v2.15.0 from its official release.
```

Cross-compilation targets are Linux `amd64`/`arm64`, macOS `amd64`/`arm64`, and
Windows `amd64`. Compilation is not runtime validation; release platforms must be
executed before being advertised.

See [CONTRIBUTING.md](CONTRIBUTING.md) for branching, commit, review, and
verification requirements.

## Quality and Security

Phase 4 keeps formatting, vet, lint, unit tests, race detection, an 80% overall and per-core-package coverage
gate, cross-build verification, CI, CodeQL, Dependabot, and a pinned
`govulncheck` workflow. The local environment cannot reach `vuln.go.dev` (HTTP
403); this is reported as blocked rather than a pass. GitHub CI and CodeQL
results are recorded separately in the
[Phase 4 completion report](docs/phase-4-completion.md).

Security reporting: use GitHub private vulnerability reporting as described in
[SECURITY.md](SECURITY.md). Do not open a public issue with exploit details or
real credentials.

## Non-Goals for v1

- Hosted or multi-tenant monitoring.
- Web UI, dashboards, or graphical clients.
- Distributed/HA monitoring or multiple writers to one SQLite file.
- APM, tracing, log aggregation, or metrics export.
- Provider-specific Slack/Discord/PagerDuty integrations.
- Kubernetes operator or service discovery.
- Multiple webhook destinations.
- Exactly-once webhook delivery.
- Custom CA/client certificates or insecure TLS.
- HTTP methods other than GET.
- Configuration hot reload.
- Release publication during Phase 0/1.

## Roadmap

Phase 0 delivered the foundation. Phase 1 implemented the shared CLI, logging,
redaction, and error foundation; Phase 2 added strict YAML configuration,
`init`, and `validate`; Phase 3 added the safe HTTP probe engine, one-shot
`check`, continuous `monitor`, and in-memory threshold streaks; Phase 4 adds
incidents, durable state, and webhooks; Phase 5 history and recovery; Phase 6
integrated reliability/security; Phase 7 release validation. The dependency adjustment that
pulls the incident/outbox store slice
into Phase 4 is documented in the
[roadmap](docs/roadmap.md#dependency-adjustment-for-persistence).

## Contributing and License

Contributions are welcome through the workflow in
[CONTRIBUTING.md](CONTRIBUTING.md). Do not commit real endpoints, tokens, or
production data.

The project is licensed under the MIT License; see [LICENSE](LICENSE).
