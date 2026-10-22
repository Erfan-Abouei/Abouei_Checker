# ADR-0001: Modular Layout, Dependency Direction, and Dependency Set

- **Status:** Accepted
- **Date:** 2026-10-22
- **Phase:** 0
- **Related:** [Architecture](../architecture.md), [Requirements](../requirements.md)

## Context

AboueiChecker is a small Go CLI, but its future behavior spans CLI parsing,
configuration, HTTP probing, scheduling, incident state, SQLite persistence, and
webhook delivery. Without boundaries, Cobra and SQLite concerns leak into domain
logic and make failure-path testing difficult. At the same time, a small tool
does not benefit from a framework, a DI container, or an interface for every
struct.

The repository started with only an MIT license. The module path must match the
real GitHub repository, and the dependency set must stay proportionate.

## Decision

Use a pragmatic layered layout:

```text
cmd/abouei-checker     process entry and signal handling
internal/cli           Cobra, flags, rendering, exit mapping
internal/app           use-case orchestration and wiring
internal/config        YAML discovery/validation/secrets
internal/probe         HTTP probing and health criteria
internal/monitor       scheduling and concurrency
internal/incident      threshold and incident transitions
internal/notify        webhook payload and delivery
internal/store         SQLite and migrations
internal/buildinfo     linker metadata
```

Dependency direction:

- `cli → app`; `app` orchestrates all operational packages.
- `monitor → probe` and `monitor → incident`.
- `store` and `notify` are infrastructure implementations invoked by `app`.
- Domain packages do not import Cobra, SQLite, or webhook transport code.
- Interfaces are declared by consumers and only where a real testing or boundary
  need exists. Concrete types are preferred otherwise.
- No plugin framework, DI container, generic repository, or `models`/`domain`
  catch-all package.

Initial runtime dependencies:

| Dependency | Purpose | Justification |
| --- | --- | --- |
| `github.com/spf13/cobra` v1.10.2 | CLI command tree/help/flags | Maintained standard choice; small transitive set; avoids bespoke parsing |
| Go standard library `log/slog` | Structured logging | No additional logging dependency |
| `go.yaml.in/yaml/v3` (planned, Phase 2) | YAML decoding | Maintained YAML v3 fork with strict decoding support; selected during Phase 2 after tests |
| `modernc.org/sqlite` (planned, Phase 4/5) | SQLite driver | Pure Go, no CGO, compatible with static cross-platform builds |
| Go standard library `net/http`, `database/sql`, `crypto/rand`, `regexp` | HTTP, DB access, IDs, RE2 | Sufficient and well understood |

No YAML or SQLite dependency is added during Phase 0 because no loader or store
exists yet. A dependency is added in the phase where it is first used and must be
justified in review.

## Consequences

Positive:

- Failure paths can be tested without Cobra, SQLite, or real webhooks.
- The scheduler and incident policy remain deterministic and clock-injectable.
- The dependency set stays small and cross-compilation remains simple.
- Package ownership is clear for later phases.

Negative and mitigations:

- Some mapping code between configuration, domain values, and database rows is
  required. This is preferred over a generic abstraction.
- `internal/app` can become a large composition root. It should be split by use
  case only when it develops distinct responsibilities.
- A pure-Go SQLite driver is larger than a CGO wrapper but avoids platform build
  requirements. Binary size is not optimized before correctness.

## Alternatives Considered

- **Single `main` package:** rejected; untestable and encourages global state.
- **Full Clean Architecture/DDD layers:** rejected as disproportionate ceremony.
- **Generic repository/database interface:** rejected; only SQLite is planned.
- **`mattn/go-sqlite3`:** rejected because CGO complicates the declared static
  cross-platform matrix.
- **A third-party logging framework:** rejected; `log/slog` covers the required
  levels and structured fields.
- **A bespoke CLI parser:** rejected; help, flags, and command conventions are
  maturely handled by Cobra.
