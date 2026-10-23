# ADR-0007: Interactive TUI Dashboard

- **Status:** Accepted
- **Date:** 2026-10-23
- **Phase:** 6
- **Related:** [CLI contract](../cli-contract.md), [TUI usage](../tui.md), [Architecture](../architecture.md)

## Context

Operators want a live, navigable terminal view of backend health without
tailing NDJSON or polling `status`. The engine already owns continuous
scheduling (`app.Monitor`), durable state (SQLite), and sanitized events
(`app.MonitorEvent`). Any interactive surface must reuse that engine — a
second monitoring loop would double HTTP traffic and risk divergent state —
while leaving every existing command, flag, JSON schema, and exit code
untouched.

## Decision

- Add an explicit `tui` command launching a fullscreen Bubble Tea dashboard.
  The established `monitor` command keeps its text/NDJSON contract unchanged.
- New runtime dependencies, kept minimal: `bubbletea` (Elm runtime) and
  `lipgloss` (adaptive styling). No widget library; all components are
  project-owned in `internal/tui`.
- New package `internal/tui` owns theme, model, navigation, screens, and
  engine wiring. Domain, persistence, and notification packages do not import
  it. Only `internal/cli` depends on it.
- Data flow: seed once from `App.Status`, then run exactly one `App.Monitor`
  loop. `OnEvent` pushes into a bounded (64) channel; a `waitCmd` delivers
  each event as a message. Overflow drops the oldest queued event because a
  newer event for the same backend supersedes it. The UI never blocks a probe.
- `App.MuteLogs` (additive) discards log records while the TUI runs so
  per-probe logs cannot corrupt the fullscreen display. Probe outcomes remain
  available as engine events; redaction boundaries are unchanged.
- Guards: `--output json` is a usage error (exit 2) with a valid JSON error
  envelope; non-TTY stdin/stdout is a usage error naming `check`/`status`/
  `history` as alternatives. `NO_COLOR` selects a colorless theme.
- Screens (dashboard, backend detail, incidents, session events, help) render
  only real state: live `MonitorEvent` fields, seeded `Status` snapshot, and
  the session timeline. Uptime percentages and invented history are forbidden;
  session-scoped panels are labeled as such and point at `history`.
- Backend order is fixed from configuration; probes never reorder rows, and
  selection is clamped so state churn cannot strand the cursor.

## Consequences

Positive:

- One engine serves text, NDJSON, JSON, and interactive consumers; no
  divergent monitoring logic.
- Automation contracts are untouched: JSON schemas, stdout/stderr separation,
  and exit codes are preserved and covered by tests.
- Rendering degrades gracefully: narrow terminals collapse columns, plain
  themes stay readable, stale feeds are labeled instead of hidden.

Negative and mitigations:

- Two Charm dependencies enter the module. They are pinned, widely used,
  and isolated to `internal/tui`; `go mod tidy` keeps them explicit.
- `App.Monitor` owns the SQLite write connection while the TUI runs, so the
  dashboard cannot re-read `Status`/`History` mid-session; the design avoids
  this by deriving all live views from the event stream plus the pre-launch
  snapshot.
- Fullscreen rendering cannot be scripted; the guards fail fast with
  actionable messages instead.

## Alternatives Considered

- **Extend `monitor` with a `--tui` flag:** rejected; it would complicate the
  stable NDJSON/text contract. A separate command keeps `monitor` automatable.
- **Poll `Status` on a timer instead of consuming events:** rejected; it adds
  a second database reader beside the writer and shows coarser data than the
  events already in memory.
- **Bubbles widget library:** rejected; custom rendering is smaller, matches
  the restrained visual system, and avoids overlapping component frameworks.
