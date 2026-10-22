# Contributing to AboueiChecker

Thank you for contributing. This project values correctness, clear boundaries,
security, and verifiable claims over volume of code.

## 1. Prerequisites

- Go 1.27.3 (matching `go.mod`).
- GNU Make.
- Git with `git-flow` (AVH edition) for the repository workflow.
- Optional: Docker for image validation.
- Optional: `golangci-lint` v2.15.0.
- Optional: `govulncheck` v1.9.0.
- Optional: `actionlint` v1.7.12 for workflow changes.

```sh
go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
# Install golangci-lint v2.15.0 from its official release.
```

## 2. Branching with git-flow

The repository uses a git-flow layout:

- `main` — production releases only.
- `develop` — integration branch for the next release.
- `feature/<name>` — new work branched from `develop`.
- `bugfix/<name>` — non-release bug fixes.
- `release/<version>` — release stabilization.
- `hotfix/<version>` — urgent production fixes branched from `main`.

Initialize once per clone:

```sh
git flow init -d
```

Start and finish a feature:

```sh
git flow feature start my-change
# edit, test, commit
git flow feature finish my-change
```

Open a pull request from the feature branch or from `develop` as appropriate.
Do not push directly to `main`. Do not rewrite published history or force-push a
shared branch.

Keep commits focused and stepwise. Conventional Commit-style subjects are
preferred:

```text
feat: add configuration discovery
fix: prevent duplicate recovery events
docs: clarify SQLite migration behavior
test: cover webhook retry classification
chore: pin golangci-lint
ci: add race detection
```

## 3. Before Opening a Pull Request

Run:

```sh
make check
make verify
```

`make check` covers formatting, vet, race tests, and coverage. `make verify` adds
lint, vulnerability scanning, and cross-compilation. If Docker was changed:

```sh
make docker-build
docker run --rm abouei-checker:dev version
```

If workflows changed:

```sh
actionlint
```

Report any check you could not run and why. Do not claim a check passed without
observing its result.

## 4. Pull Request Expectations

- Explain the problem and the chosen approach.
- Link relevant requirements, ADRs, or issues.
- Include tests for new behavior and failure paths.
- Keep public contracts backward compatible within v1 unless an ADR and
  documentation change are included.
- Update canonical docs when behavior or defaults change.
- Do not include secrets, real endpoints, production data, or generated build
  artifacts.
- Do not lower coverage thresholds or disable linters without explicit
  justification.
- Do not add a dependency without a documented reason and a review of its
  maintenance, license, and transitive footprint.
- Ensure `stdout` remains machine-readable in JSON mode and diagnostics go to
  `stderr`.

## 5. Code Conventions

- Format with `gofmt`; imports are grouped by `goimports` with the module path as
  the local prefix.
- Keep packages focused; do not add an interface solely for mocking.
- Declare interfaces where they are consumed, not next to every implementation.
- Return errors; do not call `os.Exit` outside `cmd/abouei-checker`.
- Wrap errors with `%w` and preserve causes while keeping user messages safe.
- Propagate `context.Context` through network, database, and long-running work.
- Do not introduce global mutable state.
- Avoid overlapping probes for the same backend.
- Never log header values, bodies, resolved secrets, or secret-bearing URLs.
- Add package comments for new packages and doc comments for exported symbols.
- Keep `internal/cli` free of probing, SQL, and webhook logic.

## 6. Testing

- Put unit tests beside the package they test.
- Use `httptest` instead of real network services.
- Use temporary directories for SQLite/config tests.
- Inject clocks and ID generators for deterministic scheduling/incident tests.
- Run `go test -race ./...` for concurrency changes.
- Add regression tests for every bug fix where practical.
- Do not add trivial tests solely to raise coverage.

The detailed matrix is in
[docs/testing-and-quality.md](docs/testing-and-quality.md).

## 7. Security

- Use `${NAME}` environment references for secrets.
- Never commit a real `abouei.yaml`, `.env`, database, or webhook URL.
- Follow [docs/security.md](docs/security.md).
- Report vulnerabilities privately via [SECURITY.md](SECURITY.md), not in a
  public issue.

## 8. Documentation

Public documentation is in English. Keep it accurate:

- Planned commands must be labeled as planned.
- Do not advertise unreleased behavior.
- Keep one canonical source for configuration fields and CLI contracts; link to
  it instead of duplicating.
- Add an ADR for decisions that materially constrain implementation.
- Update the roadmap when phase scope or dependencies change.

## 9. Releases

Releases are performed by the maintainer following
[docs/release-checklist.md](docs/release-checklist.md). A `v1.0.0` release
requires all functional, quality, security, platform, Docker, documentation, and
reproducibility gates. Contributors must not publish releases or tags.

## 10. License

By contributing, you agree that your contribution is licensed under the
repository's MIT License.
