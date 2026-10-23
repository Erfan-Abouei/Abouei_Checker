# Support

## Where to get help

- **Usage questions:** open a GitHub issue with the `question` label, including
  the command you ran, the configuration schema version, and the observed
  output (redact secrets first).
- **Bug reports:** open a GitHub issue with reproduction steps, expected vs.
  actual behavior, the `abouei-checker version` output, and your OS/arch.
- **Feature requests:** open a GitHub issue describing the use case, not just
  the proposed flag. Check `docs/roadmap.md` first; v1 scope is frozen.
- **Security issues:** do not open a public issue. Follow `SECURITY.md` and
  use GitHub's private vulnerability reporting.

Repository: <https://github.com/Erfan-Abouei/Abouei_Checker>

## Before asking

1. Run `abouei-checker validate --config <path>` on your configuration.
2. Re-run the failing command with `--log-level debug` and check stderr.
3. Check `docs/configuration.md` and `docs/cli-contract.md` for the
   documented contract (commands, flags, exit codes, JSON schemas).
4. Look at `docs/troubleshooting` notes in the README for common failures.

## What to include

- `abouei-checker version` output (text is fine).
- Minimal redacted configuration that reproduces the problem.
- Full command line, exit code, and relevant stdout/stderr excerpts.
- Confirmation that no secrets, tokens, or private URLs are included.

Response times are best-effort; this is a single-maintainer project with no
SLA. Security reports are prioritized over general questions.
