# Interactive TUI Dashboard

Launch it:

```sh
abouei-checker tui
abouei-checker tui --config ./abouei.yaml
```

The dashboard is the human, interactive counterpart to `monitor`: it seeds
from persisted status, then runs the same continuous engine and renders every
probe live. Quitting (`q`) shuts monitoring down gracefully and exits `0`,
like `monitor` on SIGINT.

## Requirements

- Both stdin and stdout must be interactive terminals. Otherwise `tui`
  exits `2` and names the scriptable alternatives (`check`, `status`,
  `history`, `monitor`).
- `--output json` is rejected (exit `2` with the standard JSON error
  envelope). The TUI is human-only by design.
- `NO_COLOR` selects a colorless theme; narrow terminals collapse
  lower-priority columns before critical status.

## Screens

| Key   | Screen        | Content |
| ----- | ------------- | ------- |
| `1`   | Dashboard     | Aggregate health, backend list, recent session events |
| `2`   | Backend       | Selected backend: state, timing, streaks, safe error detail |
| `3`   | Incidents     | Active open incidents + recoveries from this session |
| `4`   | Session events| This session's probe timeline, newest first |
| `?`   | Help          | All shortcuts |

`tab` / `shift+tab` cycle the four operational screens. `enter` opens the
selected backend; `←`/`→` step across backends; `esc` returns.

## Data honesty

- Statuses come from live engine events (`healthy`, `down`, `unknown`,
  `pending` before the first probe) — the same domain states as `status`.
- “Session events” are this run's timeline only. Durable observations and
  notification history live in `abouei-checker history`.
- The header pill shows `LIVE` while the engine feed runs and `STALE` if it
  ends; last-known state stays visible and is labeled stale, never presented
  as current.
- Errors shown are the engine's sanitized summaries. Secrets never appear:
  header values, tokens, URLs, and response bodies are never rendered.
- Application logs are muted while the dashboard runs so they cannot corrupt
  the display. Use `monitor` when you need the log stream.

## Branding

The header carries the ABOUEI wordmark with the product descriptor. The
genuine logo masters live in `assets/brand/logo/` (white artwork for dark
backgrounds, black for light, per the brand library manifest); terminals
render the restrained text wordmark fallback whose tokens are centralized in
`internal/tui/theme.go`.
