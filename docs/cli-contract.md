# CLI Contract

| Field | Value |
| --- | --- |
| Status | Phase 5; `version`, `init`, `validate`, `check`, `monitor`, `status`, and `history` implemented; root help and global flags implemented |
| Executable | `abouei-checker` |
| Related | [Requirements](requirements.md), [Configuration](configuration.md), [Incident lifecycle](incident-lifecycle.md) |

The command surface, flags, output rules, and exit codes below are the stable
contract for v1. Commands not yet implemented are labeled **planned** and must
not be advertised as usable.

## 1. Command Surface

| Command | Status | Purpose |
| --- | --- | --- |
| `version` | Implemented | Print build metadata |
| `init` | Implemented | Write a commented configuration template |
| `validate` | Implemented | Load, resolve, and validate configuration without network or database access |
| `check` | Implemented (Phase 4, durable) | Probe each selected backend once, persist state, attempt due notifications, and exit |
| `monitor` | Implemented (Phase 4, durable) | Continuously schedule probes and notifications until graceful shutdown |
| `status` | Implemented (Phase 5, read-only) | Read current backend and incident state from SQLite |
| `history` | Implemented (Phase 5, read-only) | Read observations and incident transitions from SQLite |
| `tui` | Implemented (Phase 6, interactive) | Open the live fullscreen dashboard; requires a TTY, rejects `--output json` |
| root/help | Implemented | Show command help |

Running the root command with no arguments prints help and exits `0`. Running an
unknown command or flag is a usage error and exits `2`.

## 2. Global Flags

| Flag | Status | Default | Behavior |
| --- | --- | --- | --- |
| `--config PATH` | Implemented | discovery | Accept an explicit YAML configuration file. Used by `validate` and future operational commands. |
| `--output text\|json` | Implemented | `text` | Select human-readable or structured output |
| `--log-level LEVEL` | Implemented | `info` until configuration is loaded | Override `logging.level` case-insensitively; values are `debug`, `info`, `warn`, `error` |
| `--help`, `-h` | Implemented | — | Show help for the root or a subcommand |
| `help [COMMAND]` | Implemented via Cobra | — | Show command help |

Precedence:

- Configuration path: `--config` > `ABOUEI_CONFIG` > discovery defaults.
- Log level: `--log-level` > configuration `logging.level` > `info`. The flag
  only overrides configuration when it is supplied explicitly.
- Log format: configuration `logging.format`; there is no log-format flag.
- Output: default `text`; only `json` selects structured mode. Other values are
  usage errors.
- Global flags may appear before or after a subcommand where the framework
  permits. Scripts should prefer the documented position after the command.

`version` and `init` do not read YAML configuration. A supplied `--config` has no
effect on those commands. `validate` loads configuration and therefore honors
`--config`, `ABOUEI_CONFIG`, discovery, and `logging.level`/`logging.format`.

Logs always go to stderr. The logger supports text and JSON handlers. The log
format comes from configuration `logging.format` when configuration is loaded;
there is no log-format flag. Before configuration is loaded (for example in
`version` and `init`), text is used.

## 3. Command Contracts

### 3.1 `version` (implemented)

```text
abouei-checker version [--output text|json]
```

- Prints the binary name, version, commit, build date, Go version, and platform.
- Does not read configuration, environment secrets, or SQLite.
- Accepts global `--config` and `--log-level`; the config path has no effect and
  the log level controls only the logger. A successful run emits one `DEBUG`
  record to stderr when debug logging is enabled.
- Human format:
  `abouei-checker dev (commit none, built unknown, go1.27.3 linux/amd64)`
- JSON uses the envelope in [section 6](#6-json-output-contract).
- Extra positional arguments are a usage error (exit `2`).
- Output write failure is an internal error (exit `3`).

### 3.2 `init` (implemented)

```text
abouei-checker init [PATH] [--force]
```

- Writes a commented configuration template to `PATH`, defaulting to
  `./abouei.yaml`.
- Creates parent directories with owner-only permissions where supported.
- New files use owner-only permissions (`0600`) where the platform supports
  them. It does not create a database.
- Refuses to overwrite an existing file unless `--force` is supplied. It rejects
  directory and symbolic-link targets.
- Does not read configuration, resolve secret variables, probe endpoints, or
  open SQLite.
- Text output is `created PATH` or `overwrote PATH`. JSON data is
  `{"path": string, "overwritten": bool}` inside the standard envelope.
- Exit: `0` created or force-overwritten; `2` invalid path or existing file
  without `--force`; `3` filesystem write failure.

### 3.3 `validate` (implemented)

```text
abouei-checker validate [--output text|json]
```

- Discovers/loads the configuration, expands environment references, applies
  defaults, and runs all schema and cross-field validation.
- Does not contact endpoints, open SQLite, or deliver notifications. It does not
  create the database directory or file.
- Prints a safe summary: config schema version, backend count, and backend IDs.
  It never prints resolved header values, tokens, URLs, or the full file.
- Text mode writes the summary to stdout. JSON mode writes the standard envelope
  with `{"valid": true, "config_version": 1, "backend_count": N,
  "backend_ids": [...]}` as `data`.
- Exit: `0` valid; `2` missing/invalid configuration or missing secret variable;
  `3` unexpected internal failure.
- On failure in JSON mode, stdout contains one valid JSON error envelope with a
  stable `code` and safe `details`, and stderr contains exactly one `Error:`
  line.

### 3.4 `check` (implemented in Phase 4 with persistence)

```text
abouei-checker check [BACKEND_ID...] [--config PATH] [--output text|json]
```

- Performs one probe per selected backend. With no IDs, checks all configured
  backends. An unknown ID is a usage error (exit `2`); no request is performed
  in that case.
- Opens and migrates SQLite, reconciles durable state, probes concurrently
  under `runtime.max_concurrent_probes` in deterministic configuration order,
  and persists each result in a transaction (observation, streaks, incident
  transition, outbox insert). One backend's timeout or failure never blocks
  the others.
- Applies the incident and reminder policy. It is not a long-running
  scheduler: future reminders are scheduled for the next invocation or a
  running monitor, but an already-due reminder is enqueued in the same run.
- Attempts every due notification event once after the probes. Failed attempts
  remain durable for later retry; the command does not wait through the full
  retry backoff schedule.
- Exit `1` if any selected backend's probe fails (unhealthy or error outcome)
  in this invocation, even if the configured failure threshold has not yet
  opened an incident.
- Exit `4` if a required notification attempt fails, taking precedence over `1`.
- Output includes per-backend health results, persisted streaks, incident status
  (`none`, `open`, `still_open`, `recovered`), and a summary with real
  `incidents_opened`, `incidents_closed`, `notifications_attempted`, and
  `notifications_failed` counts.

One-shot `check` is suitable for cron and CI, but it does not provide continuous
scheduling or reminders while it is not running.

### 3.5 `monitor` (implemented in Phase 4 with persistence)

```text
abouei-checker monitor [--config PATH] [--output text|json]
```

- Runs continuously with one schedule per configured backend.
- On startup: opens/migrates SQLite, resets stale delivery claims, restores
  threshold streaks and open incidents, immediately probes all configured
  backends, reconciles open incidents, and starts the notification worker.
- Never runs overlapping probes for the same backend. Independent backends run
  concurrently under the global limit.
- Delivers Down, Recovery, and reminder notifications according to policy, with
  durable retries. The worker preserves per-incident sequence order; a dead
  event never blocks a later Recovery.
- On the first SIGINT/SIGTERM: stops scheduling, cancels in-flight contexts,
  waits up to `runtime.shutdown_timeout` for cleanup, stops the worker, closes
  SQLite, and exits `0`. Pending notification events survive for the next start.
- A second signal after the shutdown timeout forces termination and uses the
  conventional Unix `128+signal` status (`130` for SIGINT, `143` for SIGTERM).
  Windows signal behavior follows the host platform.
- Unhealthy backends do not terminate `monitor` and do not produce exit `1`.
- Fatal startup or scheduler failures exit `3`.
- JSON mode streams newline-delimited envelopes (one compact JSON object per
  line) with `data.kind` of `started`, `probe`, or `shutdown`. Human mode
  prints readable lifecycle events.

### 3.6 `status` (implemented in Phase 5)

```text
abouei-checker status [--backend ID] [--config PATH] [--output text|json]
```

- Reads configuration to locate the database and display backend names, then
  opens SQLite read-only and renders the persisted state. It does not probe,
  create the database, run migrations, reset delivery claims, or mutate
  incident state in any way. A write attempt fails loudly through the
  read-only handle instead of mutating data.
- Shows current health (`healthy`, `down`, `unknown`, or `orphaned`), last
  observation time, last status code, consecutive failure/recovery counts, and
  the open incident link (`open_incident_id`, `opened_at`) where present.
  A backend with a streak row but no observation yet reports `unknown`;
  streak counts are still shown so pending failures stay honest.
- `orphaned` means an open incident exists for a backend ID no longer present in
  configuration. Orphaned incidents are listed after configured backends; they
  are not probed, reminded, auto-closed, or counted as healthy.
- `--backend` selects one backend ID exactly. An unknown ID is a usage error
  (exit `2`).
- Reports `open_incidents` for the selected scope (all backends, or the single
  filtered backend), plus `pending_notifications` (pending/delivering/failed
  rows) and `failed_notifications` (failed rows). Filtered counts cover only
  the selected backend.
- Exit: `0` no open incidents; `1` at least one open incident; `2` config/usage
  error; `3` database error (a missing database fails with a hint to run
  `check` or `monitor` first).
- Because it is read-only, `status` may show state that is stale relative to the
  real endpoint until the next `check` or `monitor`.

### 3.7 `history` (implemented in Phase 5)

```text
abouei-checker history [--backend ID] [--since TIME] [--limit N]
                       [--config PATH] [--output text|json]
```

- Reads persisted observations and incident notification events from SQLite
  through a read-only handle. It does not probe or mutate state.
- `--backend` filters by exact stable backend ID. Unknown IDs return empty
  results, not an error, because history outlives configuration removal.
- `--since` accepts an RFC3339 timestamp or a Go duration interpreted backwards
  from now (for example `24h`). Invalid input, including negative durations,
  is a usage error.
- `--limit` defaults to `50`; allowed range `1..1000` (out-of-range values are
  usage errors). The limit bounds each list — observations and incident events —
  independently. Results are newest first with a deterministic tie-break
  (timestamp, then unique row identity), so equal timestamps never shuffle
  pagination.
- The JSON result reports `has_more`, true when either list was truncated.
  v1 has no cursor; callers increase the
  limit or narrow the time range.
- Incident entries carry `sequence` and delivery `status` in addition to the
  identity fields below, so failed and retrying notifications stay visible.
- Exit: `0` success; `2` config/usage error; `3` database error (a missing
  database fails with a hint to run `check` or `monitor` first). Empty history
  is a valid `0` result, not an error.

### 3.8 `tui` (implemented in Phase 6, interactive)

```text
abouei-checker tui [--config PATH]
```

- Opens the fullscreen live dashboard: seeds from `status`, then runs one
  continuous monitor and renders every probe. Quitting shuts monitoring down
  gracefully. See [TUI usage](tui.md).
- Human-only: `--output json` is a usage error (exit `2` with the standard
  JSON error envelope). Both stdin and stdout must be interactive terminals;
  otherwise exit `2` with a hint naming `check`, `status`, `history`, and
  `monitor` as scriptable alternatives.
- Exit: `0` clean quit (including SIGINT/SIGTERM cancellation); `2`
  configuration, usage, or non-interactive environment; `3` internal failure.
  The command never emits JSON documents or machine-readable streams.

## 4. One-Shot vs Continuous Behavior

| Aspect | `check` | `monitor` |
| --- | --- | --- |
| Lifetime | One evaluation, then exit | Until signal or fatal error |
| Scheduling | One probe per selected backend | Immediate probe, then per-backend intervals |
| Reminders | Due events only; no future scheduling | Full reminder policy while running |
| Notification retries | One attempt per due event per invocation | Backoff retries in a worker |
| Unhealthy result exit | `1` | No exit; keeps running |
| Notification failure exit | `4` | No exit; retry/log |
| Graceful stop | Not applicable | SIGINT/SIGTERM, cleanup, exit `0` |
| JSON stream | One document | Newline-delimited documents |
| Suitable for | CI, cron, manual checks | Long-running service/container |

## 5. Exit Codes

| Code | Name | Meaning |
| --- | --- | --- |
| `0` | Success | Command completed successfully; for `check`, all selected probes passed; for `status`, no open incidents |
| `1` | Unhealthy | `check`: at least one selected probe failed; `status`: at least one open incident |
| `2` | Usage or configuration | Unknown command/flag, invalid flag value, invalid/missing config, missing secret variable, unknown backend ID |
| `3` | Internal/operational | Database unavailable/corrupt/incompatible, I/O failure, fatal startup failure, unexpected internal error |
| `4` | Notification failure | A required webhook attempt failed during a one-shot command; event remains queued |
| `130` | Forced SIGINT | Continuous mode did not stop within shutdown timeout and was forced |
| `143` | Forced SIGTERM | Continuous mode did not stop within shutdown timeout and was forced |

Precedence for a single command result:

```text
3 (internal) > 2 (usage/config) > 4 (notification) > 1 (unhealthy) > 0
```

The shared implementation maps categories centrally: usage and configuration
both use `2`, internal uses `3`, notification uses `4`, and unhealthy uses `1`.
Unclassified errors default to internal. Cobra parse and argument-validation
errors are classified as usage at the execution boundary. Commands that do not
yet produce codes `1`, `4`, `130`, or `143` are covered by direct mapping tests;
the constants exist but no command claims those outcomes prematurely.

`monitor` uses only `0`, `2`, `3`, and forced signal codes during its lifetime.
Per-backend health and delivery failures are operational events, not process
termination reasons.

A probe failure (timeout, connection failure, non-accepted status, body mismatch)
is a health result and leads to exit `1` or an incident; it is not exit `3`.
Exit `3` is reserved for failures of the tool itself.

## 6. JSON Output Contract

### 6.1 Envelope

Non-streaming JSON commands write one JSON document to stdout:

```json
{
  "schema_version": "1",
  "command": "check",
  "generated_at": "2026-10-22T12:00:00Z",
  "status": "success",
  "data": {},
  "error": null
}
```

| Field | Type | Rules |
| --- | --- | --- |
| `schema_version` | string | `"1"` for the v1 output contract |
| `command` | string | Executed command name |
| `generated_at` | string | RFC3339 UTC timestamp |
| `status` | string | `success`, `unhealthy`, or `error` |
| `data` | object or null | Command-specific payload; null for errors without data |
| `error` | object or null | Null on success; otherwise `{code, message, details?}` |

Error object:

```json
{
  "code": "config_invalid",
  "message": "configuration is invalid: backends[0].url is required",
  "details": [
    {"field": "backends[0].url", "rule": "required"}
  ]
}
```

- `message` is safe for display and contains no secret values.
- `details` is optional and must also be secret-free.
- Initial stable codes: `usage_invalid`, `config_invalid`, `config_not_found`,
  `backend_not_found`, `database_unavailable`, `database_incompatible`,
  `notification_failed`, `internal`. New codes may be added; existing meanings
  do not change within schema version 1.

### 6.2 Command data shapes (`status`/`history` planned unless noted)

`version` (implemented):

```json
{
  "schema_version": "1",
  "command": "version",
  "generated_at": "2026-10-22T12:00:00Z",
  "status": "success",
  "data": {
    "name": "abouei-checker",
    "version": "dev",
    "commit": "none",
    "build_date": "unknown",
    "go_version": "go1.27.3",
    "platform": "linux/amd64"
  },
  "error": null
}
```

`validate`:

```json
{
  "valid": true,
  "config_version": 1,
  "backend_count": 2,
  "backend_ids": ["api", "web"]
}
```

`init`:

```json
{
  "path": "./abouei.yaml",
  "overwritten": false
}
```

`check`:

```json
{
  "summary": {
    "total": 2,
    "healthy": 1,
    "unhealthy": 1,
    "incidents_opened": 1,
    "incidents_closed": 0,
    "notifications_attempted": 1,
    "notifications_failed": 0
  },
  "results": [
    {
      "backend_id": "api",
      "name": "Example API",
      "status": "unhealthy",
      "http_status": 503,
      "duration_ms": 42,
      "observed_at": "2026-10-22T12:00:00Z",
      "failure_reason": "status_mismatch",
      "error": "accepted status codes do not include 503",
      "incident_status": "open"
    }
  ]
}
```

`incident_status` is one of `none`, `open`, `still_open`, `recovered`. The safe
`error` field is omitted or null for healthy results. `failure_reason` values are
`timeout`, `connection`, `tls`, `status_mismatch`, `body_mismatch`,
`response_too_large`, `malformed_response`, `redirect_rejected`, or
`invalid_response`.

`status`:

```json
{
  "backends": [
    {
      "backend_id": "api",
      "name": "Example API",
      "status": "down",
      "last_observed_at": "2026-10-22T12:00:00Z",
      "last_status_code": 503,
      "consecutive_failures": 3,
      "consecutive_successes": 0,
      "open_incident_id": "7c2c...",
      "opened_at": "2026-10-22T11:58:00Z"
    }
  ],
  "open_incidents": 1,
  "pending_notifications": 1,
  "failed_notifications": 0
}
```

`status` values for `status` are `healthy`, `down`, `unknown`, and `orphaned`.
`last_observed_at`, `last_status_code`, `open_incident_id`, and `opened_at`
are omitted when unknown rather than invented.

`history`:

```json
{
  "observations": [
    {
      "backend_id": "api",
      "observed_at": "2026-10-22T12:00:00Z",
      "outcome": "failed",
      "status_code": 503,
      "duration_ms": 42,
      "failure_reason": "status_mismatch",
      "error": "accepted status codes do not include 503"
    }
  ],
  "incidents": [
    {
      "incident_id": "7c2c...",
      "backend_id": "api",
      "event_type": "incident.opened",
      "sequence": 1,
      "status": "delivered",
      "occurred_at": "2026-10-22T11:58:00Z"
    }
  ],
  "has_more": false
}
```

`sequence` and delivery `status` are additive Phase 5 fields that keep failed
and retrying notifications visible; the identity fields above are unchanged.

### 6.3 Streaming JSON

`monitor --output json` writes newline-delimited JSON (NDJSON): exactly one
compact envelope per line, with no indentation and no human text. Each event has
the same top-level fields. `data.kind` identifies the event, for example:

```json
{"schema_version":"1","command":"monitor","generated_at":"2026-10-22T12:00:00Z","status":"success","data":{"kind":"started","backend_count":2},"error":null}
{"schema_version":"1","command":"monitor","generated_at":"2026-10-22T12:00:01Z","status":"unhealthy","data":{"kind":"probe","backend_id":"api","healthy":false,"http_status":503,"duration_ms":42,"failure_reason":"status_mismatch","incident_status":"open"},"error":null}
```

The event vocabulary is `started`, `probe`, and `shutdown`. Probe events
carry `incident_status` (`none`, `open`, `still_open`, `recovered`). New `kind`
values or fields may be added without changing the envelope.

### 6.4 Output discipline

- In JSON mode, stdout contains only JSON. Logs, progress, warnings, and
  diagnostics go to stderr.
- When `--output json` has been parsed, a command failure writes one JSON error
  envelope to stdout (`status: "error"`, `data: null`, safe `error`) and exactly
  one `Error:` line to stderr. Invalid configuration details are limited to safe
  field paths and rule descriptions.
- Human-readable output may use tables and plain text. ANSI color is not part of
  the v1 contract.
- If an error occurs before `--output` is parsed, the process may emit only a
  human-readable error to stderr; JSON framing cannot be guaranteed for
  framework-level parse failures. This limitation is explicit.
- Broken pipes or output write failures are internal errors (`3`) where the Go
  runtime allows the process to report them.
- The CLI prints exactly one safe `Error:` line for failures and does not dump
  usage or Cobra's own error output. Internal causes are not printed.

## 7. Help and Usage Conventions

- Every command provides `--help` with a short description, usage line, flags,
  and at least one example where behavior is non-obvious.
- `abouei-checker help COMMAND` is equivalent to `COMMAND --help`.
- Root help lists all commands, but planned commands remain clearly marked in
  documentation until implemented. Help text for an unimplemented command must
  not imply that it works.
- Usage errors are written to stderr and exit `2`. They do not print a stack
  trace or full configuration.
- Commands never call `os.Exit` directly; `internal/cli` maps errors to exit codes
  and `cmd/abouei-checker` exits once.

## 8. Compatibility

- Command names and exit-code meanings are stable within v1.
- Flags may be added compatibly; existing flag meanings are not silently changed.
- JSON fields may be added; existing field meanings remain stable within schema
  version `1`. Removing or renaming a field requires a schema version change.
- Human-readable formatting is not a machine contract; automation must use JSON
  and exit codes.
- The executable name and Docker image name remain `abouei-checker`.

## 9. Current Implementation Limitations

Phase 5 implements the operational read path and data management on top of the
Phase 4 foundation: `status` and `history` open SQLite read-only (`mode=ro`,
no migration, no delivery-claim reset) and never probe; retention deletes
only expired terminal rows in bounded batches, runs on startup for writable
commands and at most every 24 hours during `monitor`, and never deletes open
incidents, pending/delivering/failed events, or backend state. Threshold
streaks, incidents, notification intent, and history survive restarts; stale
`delivering` rows reset to `pending` on writable open with the same event ID.

No destructive repair exists: corrupt, inconsistent, or newer-schema databases
fail closed with upgrade guidance, and a missing database fails `status` and
`history` with a hint to run `check` or `monitor` first. There is no database
reset or unconditional vacuum. A future `backup` command remains deferred;
operators use SQLite online backup or `VACUUM INTO`, or copy the files while
the process is stopped.

The roadmap mentions a possible `--version` convenience flag. The CLI contract
defines the `version` command as the build-metadata interface, so it does not add
an undocumented flag.

Planned commands are not registered and must not be advertised as usable.
