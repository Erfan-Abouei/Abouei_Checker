# Public Roadmap

This is the public view of the authoritative internal roadmap
(`docs/roadmap.md`). Phase numbers and scope decisions are preserved here;
only the presentation is simplified.

| Phase | Name                                           | Status      |
| ----- | ---------------------------------------------- | ----------- |
| 0     | Project foundation & engineering standards     | Complete    |
| 1     | Shared CLI/logging/error/build foundation      | Complete    |
| 2     | Configuration & backend registry               | Complete    |
| 3     | Health probe & monitoring engine               | Complete    |
| 4     | Incident lifecycle, durable state, alerts      | Complete    |
| 5     | History, retention & recovery hardening        | Complete    |
| 6     | Integration, reliability & security validation | In progress |
| 7     | Cross-platform release & documentation (`v1.0.0`) | Planned  |

## What works today (pre-1.0)

- `version`, `init`, `validate`, `check`, `monitor`, `status`, `history`,
  and the interactive `tui` dashboard.
- Strict versioned YAML configuration with defaults, environment-secret
  resolution, and safe validation.
- Safe HTTP probing: timeouts, TLS verification, redirect policy, bounded
  bodies, per-backend intervals, concurrency limits, cancellation.
- Threshold-driven incidents persisted in SQLite with a durable webhook
  outbox (Down, Recovery, reminders), retries, idempotency keys, and
  duplicate prevention across restarts.
- Human-readable and JSON output with stable exit codes (0/1/2/3/4).
- Bare `abouei-checker` opens the dashboard in interactive terminals.
- Docker image (non-root) with persistent-volume guidance.

## What remains before `v1.0.0`

- Phase 6: finish integration evidence (done in `internal/integration/`),
  concurrency hardening review, security-scan sign-off, Docker hardening
  validation.
- Phase 7: release automation, checksums/SBOM, multi-platform runtime
  evidence, npm distribution (`npm install --global abouei-checker`),
  changelog, and the published `v1.0.0` tag — only with explicit owner
  approval.

## Non-goals for v1

Hosted monitoring, APM, distributed tracing, auto-remediation, and a web
UI are out of scope. AboueiChecker is a small self-hosted CLI, not a
monitoring platform.
