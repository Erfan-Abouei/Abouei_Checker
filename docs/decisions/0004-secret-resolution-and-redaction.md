# ADR-0004: Secret References and Redaction

- **Status:** Accepted
- **Date:** 2026-10-22
- **Phase:** 0
- **Related:** [Configuration](../configuration.md), [Security](../security.md)

## Context

Backends require arbitrary custom headers such as `Authorization` and
`X-API-Key`. Putting literal secrets in a YAML file is convenient but risky, and
logging request structures or raw errors can leak credentials. The tool also
prints JSON for automation, stores history in SQLite, and sends webhook payloads,
creating several output surfaces that must remain secret-free.

The approved requirement permits literal values but asks for environment-variable
references and redaction everywhere.

## Decision

- Support `${NAME}` environment references in YAML string scalar values.
- Allow embedded references and `$$` as a literal dollar.
- Reject malformed references and missing/empty environment variables. Never
  silently substitute an empty credential.
- Report the field path and variable name, never the resolved value.
- Keep literal values valid but discourage them in documentation.
- Treat all header values, URL query strings, userinfo, webhook URLs, and
  environment values as sensitive by default.
- Prefer structural exclusion over string replacement:
  - build log records from explicit safe fields;
  - build errors from safe metadata;
  - never log request/response structs or header maps.
- Never persist secrets in SQLite or include them in notification payloads.
- Never log response bodies.
- Preserve standard TLS verification; provide no insecure override.
- Apply the same secret and redaction rules to backend probes and webhook
  authentication headers.
- Use `log/slog` with debug/info/warn/error and stderr output; debug mode does not
  weaken redaction.

## Consequences

Positive:

- Secrets can live in the process environment or a secret manager rather than the
  repository.
- Missing credentials fail closed at validation/startup.
- Logs, JSON, payloads, and database rows have a consistent secret-free contract.
- Redaction is less likely to miss a value because raw structures are never
  logged.

Negative and mitigations:

- Environment expansion complicates YAML parsing and must be tested for escaping,
  embedded values, malformed input, and empty variables.
- Operators must manage environment variables in cron/container/systemd
  environments. Documentation provides examples.
- Literal secrets remain possible and cannot be technically prevented. The
  project warns, ignores local config files in Git, and recommends permissions.
- Some raw transport errors are less convenient to debug because they are
  normalized. Safe reason codes and host/status context compensate.

## Alternatives Considered

- **Only literal YAML secrets:** rejected; poor operational security.
- **Only an external secret manager/file:** rejected; adds dependencies and
  complexity beyond the approved environment-reference requirement.
- **Log full URLs but redact headers:** rejected; tokens commonly appear in query
  strings, and paths can be sensitive.
- **Blocklist-based redaction of log strings:** rejected as the primary control;
  keep it only as defense in depth.
- **Allow insecure TLS for self-signed endpoints:** rejected; custom CA support
  must be designed explicitly rather than disabling verification.
