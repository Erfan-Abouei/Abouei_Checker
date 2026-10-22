# Configuration Specification

| Field | Value |
| --- | --- |
| Status | Implemented and frozen for Phase 2 (`validate`, `init`, and loader) |
| Schema version | `1` |
| File format | YAML |
| Related | [Requirements](requirements.md), [Security](security.md), [Persistence](persistence.md), [ADR-0006](decisions/0006-yaml-decoder-and-config-schema.md) |

This is the canonical configuration contract. Other documents reference it
instead of restating fields. The loader, validator, `init`, and `validate` are
implemented in `internal/config` and `internal/cli`; fields that only take effect
in later phases are labeled explicitly.

## 1. Source of Truth

YAML is the only user-editable configuration source of truth. SQLite stores
monitoring state, not a duplicate of backend definitions or policy. Environment
variables supply `${NAME}` string substitutions and may override the
configuration file path; they are not a general second configuration system.

## 2. File Discovery

The first applicable source wins:

1. `--config PATH`
2. `ABOUEI_CONFIG` environment variable
3. `./abouei.yaml`
4. `<user-config-dir>/abouei-checker/config.yaml`, where
   `<user-config-dir>` is `os.UserConfigDir()` (`$XDG_CONFIG_HOME` or
   `$HOME/.config` on Linux, `%AppData%` on Windows, and the standard Library
   path on macOS).

Rules:

- An explicit `--config` path that does not exist is an error. There is no
  fallback.
- If `ABOUEI_CONFIG` is set but the file cannot be read, that is an error. There
  is no fallback. An empty `ABOUEI_CONFIG` value is treated as unset.
- If neither explicit source exists, the default locations are searched in
  order. If none exists, the command fails with a hint to run
  `abouei-checker init`.
- Relative paths are resolved against the current working directory.
- A configuration file is limited to 1 MiB. Reads stop at 1 MiB + 1 byte.
- YAML must contain exactly one document. Unknown fields, duplicate mapping
  keys, and unsupported schema versions are errors.
- `~` is not expanded by the application; an unquoted shell `~` is expanded by
  the shell before the argument reaches the process.
- The file must be a regular file. Directories and special files are rejected.
  Symbolic links are not followed for configuration loading; point `--config` at
  the real file instead.
- Discovery does not search parent directories or arbitrary locations. The
  working-directory candidate is exactly `./abouei.yaml`.

`init [PATH]` writes a template to `./abouei.yaml` by default. It does not create
or modify the database, read configuration, or resolve environment references.
Existing files are not overwritten without `--force`. New files use owner-only
permissions (`0600`) where the platform supports them, and created parent
directories use `0700`. `init` rejects symbolic-link targets.

## 3. Complete Schema

The following example shows every section. Optional sections may be omitted.
It is deliberately illustrative: hostnames, IDs, and values are examples.
Schema version 1 is frozen for the v1 application; see [ADR-0006](decisions/0006-yaml-decoder-and-config-schema.md).

```yaml
# Required. Only schema version 1 is accepted by the v1 application.
version: 1

storage:
  # Optional. Default: ./data/abouei.db
  # Relative paths resolve against the directory containing this file.
  database_path: ./data/abouei.db

runtime:
  # Optional. Range 1..64. Default: 8.
  max_concurrent_probes: 8
  # Optional. Range 1s..5m. Default: 30s.
  shutdown_timeout: 30s

logging:
  # Optional: debug | info | warn | error. Default: info.
  level: info
  # Optional: text | json. Default: text. Logs always go to stderr.
  format: text

# Base policy inherited by every backend unless overridden.
defaults:
  interval: 60s                 # 1s..24h
  timeout: 10s                  # 100ms..5m, and less than interval
  failure_threshold: 3          # 1..100
  recovery_threshold: 1         # 1..100
  max_response_bytes: 1048576   # 1..16777216; default 1 MiB
  follow_redirects: false       # default false
  # Omit accepted_status_codes for the 2xx default. If present, it must be a
  # non-empty list of integers in 100..599.
  accepted_status_codes: [200, 204]
  # Optional. Omit for no body matching.
  body_match:
    type: contains              # contains | regex (RE2)
    value: '"status":"ok"'
  notifications:
    enabled: false
    notify_on_down: true
    notify_on_recovery: true
    reminder_interval: 0s       # 0s disables reminders
    reminder_max: 0             # 0 means unlimited while the incident is open

notifications:
  webhook:
    # Required when any effective notification policy is enabled.
    # The value may be a literal or a ${NAME} reference.
    url: ${ABOUEI_WEBHOOK_URL}
    timeout: 10s                # 100ms..2m
    headers:
      Authorization: "Bearer ${ABOUEI_WEBHOOK_TOKEN}"
      X-Monitor: abouei-checker
  retry:
    max_attempts: 5             # 1..20
    initial_backoff: 5s         # 100ms..1h
    max_backoff: 5m             # 100ms..1h, at least initial_backoff

retention:
  observations: 720h            # default 30 days
  incidents: 4320h              # default 180 days
  delivered_events: 168h        # default 7 days

backends:
  - id: example-api             # stable, unique, lowercase identifier
    name: Example API           # optional display name; defaults to id
    url: https://api.example.com/health
    headers:
      Authorization: "Bearer ${EXAMPLE_API_TOKEN}"
      X-API-Key: ${EXAMPLE_API_KEY}
    interval: 30s
    timeout: 5s
    failure_threshold: 2
    recovery_threshold: 1
    max_response_bytes: 65536
    follow_redirects: false
    accepted_status_codes: [200]
    body_match:
      type: contains
      value: '"status":"ok"'
    notifications:
      enabled: true
      notify_on_down: true
      notify_on_recovery: true
      reminder_interval: 30m
      reminder_max: 6
```

## 4. Root Fields

| Field | Required | Type | Default | Rules |
| --- | --- | --- | --- | --- |
| `version` | Yes | integer | — | Must be exactly `1` |
| `storage` | No | object | see below | Database location |
| `runtime` | No | object | see below | Concurrency and shutdown |
| `logging` | No | object | see below | Log level and format |
| `defaults` | No | object | see below | Base monitoring policy |
| `notifications` | Conditional | object | — | Required if any effective policy is enabled |
| `retention` | No | object | see below | Data cleanup windows |
| `backends` | Yes | list | — | At least one backend; IDs unique |

## 5. Section Contracts

### 5.1 `storage`

| Field | Required | Default | Rules |
| --- | --- | --- | --- |
| `database_path` | No | `./data/abouei.db` | Non-empty file path; `:memory:` is not valid in user configuration |

- Missing parent directories are created with owner-only permissions when the
  database is initialized.
- The database file is created with owner-only permissions (`0600` on
  platforms that support Unix modes); the containing directory uses `0700`.
- Relative paths resolve against the directory containing the configuration
  file, not the process working directory.
- The path is not expanded from `~`.
- The database is not created by `validate`.
- A database path shared by concurrent processes is unsupported. See
  [persistence.md](persistence.md).

### 5.2 `runtime`

| Field | Required | Default | Rules |
| --- | --- | --- | --- |
| `max_concurrent_probes` | No | `8` | Integer 1..64 |
| `shutdown_timeout` | No | `30s` | Duration 1s..5m |

`shutdown_timeout` bounds graceful shutdown of continuous mode and in-flight
work. It does not extend HTTP request timeouts.

### 5.3 `logging`

| Field | Required | Default | Rules |
| --- | --- | --- | --- |
| `level` | No | `info` | `debug`, `info`, `warn`, or `error`; case-insensitive, rendered lowercase |
| `format` | No | `text` | `text` or `json`; case-insensitive, rendered lowercase |

`--log-level` overrides `logging.level` for one invocation. Logs always go to
stderr. Advanced file rotation and remote sinks are out of scope for v1.

### 5.4 `defaults` and per-backend policy

The `defaults` section is a base policy. Every backend inherits each field that
it does not specify. Per-backend values replace inherited values; they are not
merged recursively except for `notifications`, whose individual fields are
independently overridable.

| Field | Required | Default | Rules |
| --- | --- | --- | --- |
| `interval` | No | `60s` | Duration 1s..24h |
| `timeout` | No | `10s` | Duration 100ms..5m and strictly less than the effective interval |
| `failure_threshold` | No | `3` | Integer 1..100 |
| `recovery_threshold` | No | `1` | Integer 1..100 |
| `max_response_bytes` | No | `1048576` (1 MiB) | Integer 1..16777216 (16 MiB) |
| `follow_redirects` | No | `false` | Boolean |
| `accepted_status_codes` | No | 2xx | Omit for the 2xx default. If present, a non-empty list of integers 100..599 |
| `body_match` | No | none | Object with `type` and `value` |
| `notifications` | No | disabled | Notification policy object |

The effective timeout must be less than the effective interval. A backend that
sets `interval: 5s` but inherits `timeout: 10s` is invalid; set both explicitly.

### 5.5 Health criteria

#### Accepted status codes

- If `accepted_status_codes` is omitted at both the backend and defaults level,
  every final HTTP status in `200..299` is accepted.
- If present, only the listed status codes are accepted. The list must be
  non-empty and unique. `[]` is invalid rather than a synonym for the default.
- Redirects are not followed by default. A `3xx` response is therefore evaluated
  as the final status unless it is explicitly listed and redirects are enabled.
- A backend can reset an inherited explicit list to the 2xx default by listing
  `200..299` explicitly. Omitting the field inherits the defaults.
- Status `0` is reserved for transport failures and cannot be configured.

#### Body matching

`body_match` is optional. When present:

| Field | Required | Rules |
| --- | --- | --- |
| `type` | Yes | `contains` or `regex` |
| `value` | Yes | Non-empty string |

Semantics:

- `contains` performs a byte-substring search in the response body.
- `regex` compiles the value with Go's RE2 engine and searches for a match
  anywhere in the body. Invalid expressions are configuration errors.
- Body matching is evaluated only after the final status is accepted.
- A backend can clear a body matcher inherited from `defaults` with an explicit
  YAML null (`body_match: null`).
- A missing match is a failed health observation with reason `body_mismatch`.
- The body is read up to `max_response_bytes` + 1 bytes. If the limit is
  exceeded while body matching is configured, the observation fails with
  `response_too_large`; matching is not attempted on a truncated body.
- When no body matching is configured, the body is not read for matching and is
  closed after the configured bound.
- There are no JSONPath, header, or multi-condition matchers in v1.

#### Response size and malformed responses

- `max_response_bytes` is a hard safety limit for body reads.
- Transport errors, malformed HTTP responses, premature EOF during a required
  match, and oversized bodies are failed observations with safe reason codes.
  They are not process-internal errors.
- Response bodies are never logged.

### 5.6 Secret references

Any YAML **string value** may contain environment references:

```yaml
headers:
  Authorization: "Bearer ${API_TOKEN}"
notifications:
  webhook:
    url: ${WEBHOOK_URL}
```

Rules:

1. `${NAME}` is replaced with the environment variable value. `NAME` must match
   `[A-Za-z_][A-Za-z0-9_]*`.
2. References can be embedded in a larger string.
3. `$$` produces a literal `$`. A single `$` that is not followed by `{` or `$`
   is also literal.
4. A malformed `${...}` reference is a configuration error. This includes an
   empty or invalid name and a missing closing brace.
5. A missing or empty environment variable is a configuration error. There is
   no empty-string fallback.
6. Environment variable names are case-sensitive on Unix-like systems and
   follow the host operating system on Windows.
7. Expansion applies to YAML string-typed field values, including duration
   strings, and not to mapping keys, integers, booleans, or numeric lists.
   YAML anchors and aliases are permitted, but the 1 MiB file limit and strict
   decoding still apply. An aliased `body_match` is expanded once.
8. Resolved values are inserted verbatim and are not rescanned. A secret value
   containing `$` or `${...}` is preserved literally and cannot introduce a new
   reference.
9. Literal secret values are accepted but discouraged. Protect the file with
   filesystem permissions and avoid committing it.
10. Every value substituted from the environment is registered with the
    application redactor, including non-secret substitutions. This can
    over-redact a common value; prefer dedicated secret variables.

Error reporting rules:

- Errors identify the field path and environment variable name, for example
  `backends[0].headers.Authorization: environment variable API_TOKEN is not set`.
- Errors never include the resolved value, the raw scalar snippet from a YAML
  type error, or the full header map.
- `validate` does not print the configuration file contents.
- All resolved secret values are marked for redaction and must not appear in
  logs, JSON, diagnostics, notification payloads, or database rows.

### 5.7 `notifications`

The top-level `notifications` section defines the transport. Per-backend
`notifications` objects define policy. See
[incident-lifecycle.md](incident-lifecycle.md#6-notification-policy-and-reminders).

#### Transport

| Field | Required | Default | Rules |
| --- | --- | --- | --- |
| `webhook.url` | Conditional | — | Absolute `http` or `https` URL; maximum 2048 bytes; no userinfo; no fragment; required when any effective notification is enabled |
| `webhook.timeout` | No | `10s` | Duration 100ms..2m |
| `webhook.headers` | No | empty | Map of custom header names to string values; maximum 50 entries; each value at most 8 KiB and all header material at most 16 KiB |
| `retry.max_attempts` | No | `5` | Integer 1..20; includes the first attempt |
| `retry.initial_backoff` | No | `5s` | Duration 100ms..1h |
| `retry.max_backoff` | No | `5m` | Duration 100ms..1h; must be greater than or equal to `initial_backoff` |

Webhook headers are subject to secret handling. `Host`, `Content-Length`,
`Transfer-Encoding`, and `Connection` are reserved and invalid. `Content-Type`
is set to `application/json` by the application; configuring it is invalid.
`Idempotency-Key` is reserved for the event ID and cannot be overridden.

#### Policy

| Field | Required | Default | Rules |
| --- | --- | --- | --- |
| `enabled` | No | `false` | Boolean |
| `notify_on_down` | No | `true` | Boolean |
| `notify_on_recovery` | No | `true` | Boolean |
| `reminder_interval` | No | `0s` | `0s` disables reminders; otherwise 1m..168h |
| `reminder_max` | No | `0` | Integer 0..1000; `0` means unlimited while the incident is open |

If the effective policy is enabled, at least one of Down notification, Recovery
notification, or reminders must be active. If all are disabled, validation
fails rather than accepting a policy that can never notify.

Reminders require a non-zero `reminder_interval`. `reminder_max` is ignored when
reminders are disabled.

### 5.8 Backends

| Field | Required | Default | Rules |
| --- | --- | --- | --- |
| `id` | Yes | — | Unique; matches `^[a-z0-9][a-z0-9_-]{0,62}$`; stable identity |
| `name` | No | same as `id` | Non-empty display string; maximum 128 UTF-8 bytes |
| `url` | Yes | — | Absolute `http`/`https` URL; maximum 2048 bytes; host required; no userinfo; no fragment; no whitespace or control characters |
| `headers` | No | empty | Maximum 50 entries; valid HTTP token names; no CR/LF/NUL in values; each value at most 8 KiB and all header material at most 16 KiB |
| policy fields | No | inherited | `interval`, `timeout`, `failure_threshold`, `recovery_threshold`, `max_response_bytes`, `follow_redirects`, `accepted_status_codes`, `body_match`, `notifications` |

Backend IDs are the durable identity used in observations, incidents, webhook
payloads, and history. Changing an ID starts a new history and state stream.
Reusing an ID for a different service is prohibited by convention because it
would merge history.

Reserved probe headers: `Host`, `Content-Length`, `Transfer-Encoding`,
`Connection`, `Proxy-Authorization`, `Keep-Alive`, `TE`, `Trailer`, and
`Upgrade` are invalid. `User-Agent` may be overridden; the default is
`abouei-checker/<version>`.

A maximum of 100 backends is enforced in v1 to keep scheduling and SQLite load
predictable. This limit can be revisited with evidence.

### 5.9 `retention`

| Field | Required | Default | Rules |
| --- | --- | --- | --- |
| `observations` | No | `720h` (30 days) | Duration 1h..8760h |
| `incidents` | No | `4320h` (180 days) | Duration 24h..87600h |
| `delivered_events` | No | `168h` (7 days) | Duration 1h..8760h |

Retention applies to terminal data. Open incidents, pending notification events,
and the latest backend state are never removed by routine cleanup. See
[persistence.md](persistence.md#8-retention-and-backup).

## 6. Defaults Summary

| Setting | Default |
| --- | --- |
| Config schema version | `1` |
| Database path | `./data/abouei.db` |
| Max concurrent probes | `8` |
| Shutdown timeout | `30s` |
| Log level / format | `info` / `text` |
| Interval | `60s` |
| Timeout | `10s` |
| Failure threshold | `3` |
| Recovery threshold | `1` |
| Max response bytes | `1 MiB` |
| Follow redirects | `false` |
| Accepted status codes | `2xx` when omitted |
| Body matching | none |
| Notifications | disabled |
| Reminder interval | `0s` (disabled) |
| Reminder max | `0` (unlimited if reminders enabled) |
| Retry attempts / backoff | `5` / `5s` to `5m` |
| Observation retention | `30 days` |
| Incident retention | `180 days` |
| Delivered event retention | `7 days` |

## 7. Validation Responsibilities

Configuration validation happens before any database or network operation and
is a pure operation. It is responsible for:

- file size, regular-file checks, symlink rejection, YAML syntax,
  single-document enforcement, duplicate keys, unknown fields, and schema
  version;
- environment expansion, malformed references, and missing/empty variable
  failures;
- required fields, types, ranges, durations, and cross-field constraints;
- unique backend IDs and stable ID format;
- URL scheme, host, userinfo, fragment, length, whitespace, and control
  character restrictions;
- header name/value validity, reserved headers, per-value and total size limits;
- status-code list validity and body-match type/value/regex compilation;
- effective policy inheritance and notification enablement;
- webhook URL presence when required and retry backoff ordering;
- relative database path resolution;
- limits on backends and headers.

Validation is not responsible for:

- DNS resolution, connectivity, TLS trust of monitored endpoints, or HTTP
  health;
- webhook reachability or receiver idempotency;
- database schema compatibility;
- verifying that a configured backend is currently healthy.

`validate` resolves environment references to prove they exist, but it does not
render the resolved values. `validate` performs no network request and does not
create or open the SQLite database.

## 8. Failure Behavior

| Situation | Behavior |
| --- | --- |
| No configuration file found | Exit 2; message names discovery sources and suggests `init` |
| Explicit path missing | Exit 2; no fallback |
| Path is a directory, special file, or symbolic link | Exit 2; names the path and the rule |
| YAML syntax/duplicate key/unknown field | Exit 2; safe field path and line information where available |
| Unsupported `version` | Exit 2; states supported version `1` |
| Missing/empty environment variable | Exit 2; names variable and field path, never the value |
| Invalid URL/header/regex | Exit 2; names field and rule |
| Effective timeout not less than interval | Exit 2; names backend and both fields |
| Notification enabled without webhook URL | Exit 2 |
| File larger than 1 MiB | Exit 2 |
| Permission or unexpected I/O failure | Exit 2 for configuration loading; no partial configuration is used |
| `init` target already exists without `--force` | Exit 2; file is left untouched |
| `init` target is a directory or symbolic link | Exit 2; file is left untouched |
| `init` filesystem write failure | Exit 3 |
| Database missing/unreadable/corrupt | Exit 3 for commands that need persistence; never auto-recreate |
| Database schema newer than binary | Exit 3; requires a newer executable |
| Configuration valid but endpoint unhealthy | Not a configuration error; command reports health and uses exit 1 for `check` |

Validation errors are aggregated where practical so a user can fix multiple
fields in one pass. Errors contain field paths and rule descriptions; they never
dump the complete configuration or resolved secrets. At most 20 detailed
field errors are shown, followed by a count if more remain. Errors are reported
in a deterministic schema order. In `--output json` mode, `validate` writes a
JSON error envelope with `code`, `message`, and safe `details` to stdout while
writing exactly one human-readable error line to stderr.

## 9. Schema Versioning

- `version` is a required integer and identifies the complete accepted schema.
- The v1 application accepts exactly version `1`.
- Any breaking or additive schema change increments the version. Older binaries
  fail closed on a version they do not support rather than ignoring fields.
- The config schema version is independent of the application SemVer.
- There is no automatic configuration migration in v1. A future release may add
  an explicit migration command; it must never rewrite a file silently.
- Version 1 is frozen as of Phase 2. Changes after that require an ADR and tests
  for old/new version behavior; see [ADR-0006](decisions/0006-yaml-decoder-and-config-schema.md).

## 10. Secret-Handling Examples

Safe:

```yaml
headers:
  Authorization: "Bearer ${SERVICE_TOKEN}"
  X-API-Key: ${SERVICE_API_KEY}
```

Unsafe and discouraged even though syntactically valid:

```yaml
headers:
  Authorization: "Bearer literal-token-value"
```

Do not place secrets in backend IDs, names, URLs, query strings, webhook payload
templates, or documentation. If a URL must contain a token query parameter, the
token is still redacted from logs and errors by the implementation. Prefer an
`Authorization` header supplied through an environment reference.

## 11. Implementation Status

Phase 2 implements the complete loader in `internal/config`:

- discovery and bounded reads with regular-file and symlink checks;
- strict version-1 decoding with unknown-field and duplicate-key rejection;
- `${NAME}` expansion with `$$` escaping and secret registration;
- defaults, per-backend inheritance, validation, and normalization;
- `init` template generation in `internal/config` and `internal/cli`;
- `validate` with text and JSON output and no network or database access.

The monitoring policy fields (`interval`, `timeout`, `failure_threshold`,
`max_response_bytes`, `follow_redirects`, `accepted_status_codes`,
`body_match`) are consumed by the Phase 3 probe engine and scheduler for
one-shot `check` and continuous `monitor`, with failure streaks kept in memory
only. Incident state, persistence, and webhook delivery remain Phase 4–5 work.
No SQLite database is opened by
this phase.
