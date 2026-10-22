# ADR-0005: Phase 1 Logging, Redaction, and Error Boundaries

- **Status:** Accepted
- **Date:** 2026-10-22
- **Phase:** 1
- **Related:** [Architecture](../architecture.md), [Security](../security.md), [CLI contract](../cli-contract.md), [ADR-0001](0001-modular-layout-and-dependencies.md), [ADR-0004](0004-secret-resolution-and-redaction.md)

## Context

Phase 0 specified `log/slog`, stderr diagnostics, and structural exclusion of
secrets but did not implement a logger. Phase 1 must provide a logger that later
packages can use without each command inventing its own destination, level,
timestamp, field names, or redaction behavior. It also needs a shared error
vocabulary so the CLI can map failures to the documented exit codes in one place.

The architecture originally said logging would be configured in `internal/app`
and that a separate package was not justified unless redaction or sinks became
complex enough. Implemented redaction needs to be reusable by future probe,
notification, and configuration packages, which must not import the application
composition root.

## Decision

- Add a small `internal/logging` package. It owns:
  - level and format parsing (`debug`/`info`/`warn`/`error`, `text`/`json`);
  - `slog` handler construction with UTC timestamps;
  - the `Redactor` and the safe-field rules.
- `internal/app` owns application composition: it accepts global options,
  creates the logger with an injected writer, retains the redactor, and exposes
  build metadata. The CLI injects stderr; the package never defaults to a global
  stream.
- Redaction is allowlist-first:
  - only explicitly documented safe field names keep their values;
  - unknown field names keep their names but lose their values;
  - groups, maps, structs, and non-error `any` values are redacted;
  - errors that implement `SafeMessage() string` use that message; other errors
    are reduced to text and passed through the redactor as a backstop;
  - resolved secrets are registered with `Redactor.AddSecrets` and replaced
    literally and in query-escaped form;
  - URL-like strings are rendered as scheme and host only; bearer/basic
    credentials, cookies, query-secret parameters, and secret assignments are
    normalized.
- Timestamps use a fixed-width UTC RFC3339 format with milliseconds. JSON
  handlers emit one object per line.
- Add `internal/app.Error` with a category, stable code, safe message, and
  optional wrapped cause. `Error()` and `SafeMessage()` never render the cause.
- `internal/cli` owns the single category-to-exit-code mapping. Unclassified
  errors default to internal (`3`); Cobra parse/validation errors are wrapped as
  usage (`2`) at the execution boundary. Usage and configuration share exit
  code `2`, matching the approved CLI contract.
- `--config` is accepted, recorded in application options, and deliberately not
  read in Phase 1. No YAML dependency is added.
- The CLI does not expose a `--log-format` flag. JSON logging is available
  through the logging and application APIs and will be selected by
  `logging.format` when Phase 2 implements configuration loading.

## Consequences

Positive:

- Future packages can log and redact without importing the composition root.
- New log fields fail closed instead of leaking by default.
- Error classification and exit-code mapping are testable and centralized.
- Command output on stdout remains independent of diagnostics on stderr.
- The logger has no global mutable state and accepts injected writers.

Negative and mitigations:

- Adding a legitimate log field requires updating the allowlist and a test. This
  friction is intentional.
- Arbitrary secrets inside arbitrary free-form strings are not guaranteed to be
  detected. The primary control is structural exclusion; callers must register
  resolved secrets and use safe errors/URLs. This limitation is documented in
  [security.md](../security.md#10-logging-and-error-handling).
- Log format cannot be selected from the CLI until configuration loading exists;
  Phase 1 uses text by default.
- The extra package slightly expands the package layout compared with the Phase 0
  sketch. It avoids a dependency inversion that would force domain packages to
  import `internal/app`.

## Alternatives Considered

- **Keep logging only in `internal/app`:** rejected because future probe and
  notification packages cannot import the composition root without violating the
  dependency direction.
- **Blocklist-first redaction:** rejected as the primary control; unknown fields
  would leak by default. Patterns remain a defense-in-depth backstop.
- **A third-party logging library:** rejected; `log/slog` meets the requirements
  without another dependency.
- **A custom logger interface:** rejected; it would add ceremony without a second
  implementation or test need.
- **A global `slog.SetDefault` logger:** rejected because it complicates tests
  and hides dependencies.
- **Add `--log-format` now:** rejected because the approved CLI contract exposes
  logging format through configuration, not a new global flag.
