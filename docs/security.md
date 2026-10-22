# Security Specification

| Field | Value |
| --- | --- |
| Status | Phase 1 logging, redaction, and error controls implemented; remaining controls in later phases |
| Related | [Configuration](configuration.md), [Incident lifecycle](incident-lifecycle.md), [Persistence](persistence.md), [Testing](testing-and-quality.md) |

Security is a release gate, not a Phase 0 claim. This document defines the
threat model and mandatory controls for the implementation.

## 1. Trust Model

| Asset or actor | Trust level |
| --- | --- |
| Configuration file and environment variables | Trusted operator input |
| Local SQLite database and data directory | Trusted local state; protect with filesystem permissions |
| Monitored HTTP endpoints | Untrusted network peers |
| Webhook receiver | Untrusted network peer until configured by the operator |
| CLI user | Trusted local operator |
| Third-party Go modules and container base tooling | Untrusted until pinned and reviewed |

The tool monitors arbitrary endpoints, including private networks and localhost.
The operator is expected to control the configuration. If configuration is
supplied by an untrusted party, that party can direct requests to internal
services. This is an explicit SSRF trust decision, not an overlooked control.

The CLI has no authentication or authorization in v1. Do not expose a shell or
allow untrusted users to invoke it with arbitrary configuration.

## 2. Security Objectives

- Secret values never leak through logs, errors, JSON, diagnostics, database
  rows, notification payloads, or container build context.
- Health probes and webhook calls use bounded time and response sizes.
- TLS certificate verification is never silently disabled.
- Redirects cannot be used to forward credentials to a different origin.
- Webhook delivery is durable but not falsely claimed to be exactly-once.
- The container runs without root, a shell, or embedded secrets.
- Dependency and static-analysis checks are part of normal change control.

## 3. Secrets and Redaction

### 3.1 Sources

Secrets may appear in:

- backend request headers (`Authorization`, `X-API-Key`, `Cookie`, custom
  headers);
- the webhook URL (userinfo or query parameters);
- webhook custom headers;
- literal values in YAML, although environment references are preferred.

Configuration supports `${NAME}` environment references. Missing or empty
variables are errors. See
[configuration.md](configuration.md#56-secret-references).

### 3.2 Mandatory handling rules

1. Resolve secrets only when constructing the client/request, not at parse time
   for display.
2. Store resolved secrets only in memory for the lifetime of the process. Never
   persist them in SQLite.
3. Never log:
   - any header value, regardless of header name;
   - resolved environment variables;
   - webhook URLs including query strings or userinfo;
   - request/response bodies;
   - complete configuration structures.
4. Errors must be constructed from safe metadata: field path, backend ID, host
   (without userinfo/query), status code, normalized failure reason, and
   operation name. Do not wrap raw `net/http` errors without sanitization.
5. JSON output and notification payloads never include request headers, backend
   URLs, webhook URLs, environment values, or response bodies.
6. Configuration validation reports field paths and variable names only. It does
   not print the offending value.
7. Webhook payload `last_error` is bounded and sanitized; it uses status/reason
   context, not raw transport errors that may contain URLs.
8. Debug logging does not relax redaction. There is no "unsafe verbose" mode.
9. Panics are avoided in request paths. Any unexpected panic must not be recovered
   by dumping configuration or request structures.
10. `status`/`history` output omits secrets because secrets are never stored.
11. Backups and database files are operational data, not secret stores, but
    should still be permission-protected.

### 3.3 Redaction strategy

Prefer structural exclusion over string replacement:

- Construct log records from explicit safe fields instead of logging request
  structs.
- Construct error messages from explicit safe fields instead of formatting a
  URL or header map.
- Expose only header names when diagnostics require them.
- Treat all header values and URL query strings as sensitive by default.
- Use a dedicated safe-URL helper that renders scheme and host only, optionally
  adding a normalized path only after review.

String blacklists are a defense-in-depth backstop, not the primary control.

## 4. TLS

- Certificate and hostname verification always use the standard Go defaults.
- There is no `insecure_skip_verify` or equivalent option in v1.
- The minimum TLS version is the Go client default (TLS 1.2 or newer for
  supported Go releases).
- Custom CA bundles and client certificates are deferred. Operators needing
  private CAs must wait for a reviewed feature; they must not disable
  verification.
- TLS errors are reported with safe context and a normalized reason. Certificate
  subjects/SANs may be included only if reviewed as non-sensitive.
- Webhook delivery uses the same verification policy as probes.

## 5. HTTP Client Configuration

Backend probes:

- Method: `GET`; no request body.
- One overall request timeout from `timeout`, implemented with context; connect,
  TLS handshake, response headers, and body read all share the deadline.
- No automatic retries of failed probes. A retry would distort consecutive-failure
  semantics.
- `Proxy` is not taken implicitly from environment variables in v1. Proxy support
  requires explicit reviewed configuration; implicit proxies can leak
  credentials and create unexpected egress.
- No cookie jar.
- `Accept-Encoding: identity` by default, with `DisableCompression` so response
  size limits apply to bytes actually read.
- `MaxResponseHeaderBytes` is bounded (64 KiB) and response bodies are limited by
  `max_response_bytes` (default 1 MiB, maximum 16 MiB).
- Idle connections are bounded and closed when the process exits.
- HTTP/2 may be used through the standard library; no custom framing.
- Custom header names and values are validated for token syntax and CR/LF/NUL
  injection.
- `Host`, `Content-Length`, `Transfer-Encoding`, `Connection`, and other
  hop-by-hop headers are rejected.
- `User-Agent` defaults to `abouei-checker/<version>` and may be overridden.

Webhook client:

- `POST` with a bounded JSON body.
- Per-attempt timeout from `webhook.timeout`.
- No redirects followed; `3xx` is a delivery failure.
- TLS verification enabled.
- Custom headers validated and redacted.
- Response bodies read up to 64 KiB, never logged verbatim, and discarded.
- `Idempotency-Key` is reserved for the event ID.

## 6. SSRF and Network Trust

AboueiChecker intentionally allows private, loopback, link-local, and internal
hostnames because monitoring internal services is a primary use case.

Controls and constraints:

- Configuration is trusted. Accepting configuration from an untrusted source is
  outside the trust model and can enable SSRF against internal networks.
- Redirects are disabled by default.
- If redirects are enabled, the client:
  - allows only `http`/`https` schemes;
  - rejects URLs with userinfo;
  - rejects HTTPS-to-HTTP downgrade;
  - follows at most five redirects;
  - rejects cross-origin redirects when any custom request headers are
    configured, preventing credential forwarding;
  - strips standard sensitive headers on any same-origin change according to Go
    defaults and never forwards the configured header map to another origin.
- Webhook redirects are never followed.
- No proxy environment variables are honored implicitly.
- A future optional host/CIDR allowlist or denylist can be added with explicit
  approval and tests. It must not silently break legitimate private-network
  monitoring.
- DNS rebinding and cloud metadata endpoints are residual risks under the
  trusted-config model. Recommended deployment controls include network egress
  policy and not exposing configuration control to untrusted users.

## 7. HTTP Client and Probe Safety

- Enforce a response-body limit even when body matching is not configured.
- If body matching is configured and the limit is exceeded, fail the observation
  with `response_too_large`; do not match against a truncated body.
- Close response bodies on every path, including errors.
- Treat malformed responses, premature EOF, invalid status lines, and unexpected
  content as failed observations, not crashes.
- Do not log response bodies, even at debug level.
- Bound response headers to avoid memory exhaustion.
- Do not follow redirects by default.
- Do not reuse a request context after timeout.
- Do not schedule overlapping probes for the same backend.
- A global concurrency limit prevents unbounded sockets/goroutines.

## 8. URL and Header Validation

URLs:

- Absolute URL with scheme `http` or `https`.
- Non-empty host.
- No userinfo (`user:password@host` is rejected; use headers).
- No fragment.
- Query strings are allowed for compatibility but treated as sensitive in logs.
- Backend and webhook URLs are limited in length (implementation cap, at least
  2048 bytes; document final value in Phase 2/3).
- No `file:`, `unix:`, `ftp:`, or other schemes.

Headers:

- Header name must be a valid HTTP token.
- Value must not contain CR, LF, or NUL.
- Maximum 50 configured headers per target.
- Maximum 8 KiB per value and 16 KiB total header material, subject to final
  implementation validation.
- Reserved names are rejected as described in
  [configuration.md](configuration.md#58-backends).
- Header names are canonicalized for duplicate detection.
- Values are never logged or echoed in validation errors.

## 9. Webhook Security

- Prefer HTTPS; plain HTTP is allowed for local testing but should be avoided
  over untrusted networks.
- The global webhook endpoint is operator-configured; no provider is hard-coded.
- Custom headers can carry authentication tokens; they are resolved from
  environment references and redacted.
- No redirects.
- Payload contains only backend identity, incident metadata, and safe error
  context. It never contains configured URLs, request headers, response bodies,
  or environment values.
- Event IDs are UUIDv4 and used as `Idempotency-Key`. Receivers must deduplicate.
- Delivery is at-least-once. Duplicate requests after crashes are expected and
  documented.
- HMAC request signing and replay-window validation are deferred. If added, the
  signature secret must use the same environment-reference and redaction rules.
- Webhook response bodies are untrusted input; limit and ignore them.
- A malicious receiver can delay delivery up to the timeout; the worker must not
  block other incidents indefinitely. Per-incident ordering may be preserved
  without a global head-of-line block.

## 10. Logging and Error Handling

- Use `log/slog` to stderr.
- Levels: `DEBUG`, `INFO`, `WARN`, `ERROR`; the CLI accepts them
  case-insensitively.
- Safe fields include `component`, `command`, `backend_id`, `incident_id`,
  `event_id`, `operation`, `duration_ms`, `http_status`, `failure_reason`,
  `outcome`, `attempt`, `error_category`, and `error_code`.
- Never log header values, query strings, full URLs, bodies, or resolved env
  values.
- Do not log the full configuration at startup. Log a summary such as backend
  count and config schema version.
- Errors preserve causes internally (`%w`) but user/JSON messages are sanitized.
  Expected failures use `internal/app.Error`, whose safe message is separate from
  its wrapped cause.
- Do not include stack traces in normal CLI output.
- Exit codes distinguish usage/configuration (`2`), internal (`3`), unhealthy
  (`1`), and notification failure (`4`). Security errors such as certificate
  failure are failed probes, not internal crashes.

### 10.1 Implemented safe-logging boundary

Phase 1 implements the following boundary in `internal/logging`:

1. **Structural allowlist (primary control).** Only documented safe field names
   retain their values. An unknown field name keeps its name but its value becomes
   `[REDACTED]`. Groups, maps, structs, slices, and non-error `any` values are
   redacted rather than serialized.
2. **Safe errors.** Errors implementing `SafeMessage() string` are rendered with
   that message. Other errors are reduced to text and passed through the
   redactor. Application errors never render their wrapped cause.
3. **Registered secrets.** Resolved secret values are registered with
   `Redactor.AddSecrets` and replaced literally and in query-escaped form. Phase
   1 has no configuration loader, so the production registry starts empty; Phase
   2 must register every resolved header, token, cookie, and environment value.
4. **Pattern backstop.** URL-like strings render as scheme and host only.
   Bearer/basic credentials, `Cookie`/`Set-Cookie`, query-secret parameters, and
   named secret assignments are normalized. This is defense in depth, not a
   guarantee.
5. **Fixed output shape.** Timestamps are UTC RFC3339 with milliseconds. JSON
   records are one object per line. The CLI injects stderr as the only log
   destination.

**Residual limitation:** arbitrary secret values embedded in arbitrary strings
are not automatically detectable. Callers must not format raw request/response
structures, must register resolved secrets, and must use safe errors and
`logging.SafeURL`. Debug level never relaxes these rules.

## 11. SQLite and Local Data

- Create the data directory with owner-only permissions (`0700`) and the database
  with owner-only permissions (`0600`) where supported.
- Treat `-wal` and `-shm` files as equally sensitive.
- Run as a dedicated non-root user in Docker.
- Do not store secrets, headers, webhook URLs, or response bodies in SQLite.
- Do not expose the database over a network listener. SQLite is a local file.
- Backups must be consistent and protected; see
  [persistence.md](persistence.md#8-retention-and-backup).
- A corrupt or newer database fails closed. The application never deletes or
  recreates it automatically.
- Database error messages avoid SQL parameter values.

## 12. Container Security

The provided foundation:

- Multi-stage build; build tools are absent from the runtime image.
- `scratch` runtime with a static binary and CA certificates only.
- Numeric non-root user/group `65532:65532`.
- No shell, package manager, config, database, or secrets in the image.
- Configuration mounted read-only from the host.
- Data explicitly mounted at `/data`; the writable layer is not durable.
- `STOPSIGNAL SIGTERM`; the process handles SIGTERM for graceful shutdown.
- Image labels identify source, license, and version.

Recommended deployment flags:

```text
--read-only
--cap-drop=ALL
--security-opt=no-new-privileges
--user 65532:65532
-v abouei-data:/data
-v /host/abouei.yaml:/etc/abouei/abouei.yaml:ro
```

Environment variables can be inspected by users with Docker access. Prefer a
secret manager or protected env file, and never bake secrets into the image or
commit them to the repository. The container is not a sandbox against a
malicious configuration author.

Base images and tool versions are pinned. Release phases should additionally pin
image digests, generate an SBOM, and scan the runtime image.

## 13. Dependency and Supply-Chain Security

- Dependencies require a documented technical justification and a minimal
  footprint.
- `go.sum` is committed; module downloads use checksum verification.
- `govulncheck` scans source and dependencies.
- CodeQL runs on pull requests, pushes to protected branches, and a schedule.
- `golangci-lint` includes `gosec` and correctness linters.
- Dependabot proposes Go module, GitHub Actions, and Docker updates.
- GitHub Actions are pinned by version and use least-privilege `permissions`.
- CI does not expose repository secrets to pull requests and does not use
  `pull_request_target` with untrusted code.
- Release artifacts must be reproducible, checksummed, and produced by a reviewed
  workflow. Signing/provenance is a Phase 7 decision.
- New dependencies are reviewed for maintenance status, license compatibility,
  CGO requirements, and transitive footprint.

## 14. Configuration File Security

- Prefer environment references over literal secrets.
- Restrict configuration permissions to the owner (for example `0600`).
- Do not commit a real `abouei.yaml`; the repository ignores it.
- `init` should create files with owner-only permissions.
- `validate` never prints the file or resolved values.
- Configuration is trusted input; do not load it from a world-writable location.
- The default config discovery does not search the current directory before an
  explicit path or environment override, reducing accidental shadowing.

## 15. Security Verification

Phase 1 verification includes:

- `gofmt`, `go vet`, `golangci-lint` (including `gosec`), unit tests, race
  detector, shuffled race tests, coverage, and cross-compilation.
- Dedicated redaction tests that inject bearer tokens, API keys, cookies,
  session values, registered secrets, URL userinfo/query strings, and
  secret-bearing errors into both text and JSON log handlers.
- Tests proving JSON command output remains valid while debug logs go to stderr.
- GitHub Actions CI and CodeQL executed successfully on pull request #1,
  including the pinned `govulncheck` step.
- `govulncheck` remains blocked in the local environment by HTTP 403 from
  `vuln.go.dev`; this is reported as a local environmental limitation, not a
  pass.

Later phases must add tests for header validation, redirect policy, response
limits, TLS verification, malformed responses, webhook retry state, and database
permissions. A security review is a `v1.0.0` release gate.

## 16. Reporting a Vulnerability

Do not open a public issue for a suspected vulnerability. Use GitHub's private
vulnerability reporting for
[Erfan-Abouei/Abouei_Checker](https://github.com/Erfan-Abouei/Abouei_Checker/security/advisories/new).

Include affected version/commit, reproduction steps, impact, and any suggested
fix. Do not include real credentials or production data. The maintainer will
acknowledge the report through GitHub and coordinate disclosure after a fix is
available. No response-time SLA is promised for this personal open-source
project.
