# Architecture Decision Records

ADRs record decisions that materially constrain implementation. Approved
product requirements remain authoritative; an ADR explains how Phase 0 resolved a
technical question within those requirements.

| ADR | Title | Status |
| --- | --- | --- |
| [0001](0001-modular-layout-and-dependencies.md) | Modular layout, dependency direction, and dependency set | Accepted |
| [0002](0002-configuration-and-persistence-boundaries.md) | Configuration and persistence boundaries | Accepted |
| [0003](0003-webhook-delivery-and-idempotency.md) | Webhook delivery, outbox, and idempotency | Accepted |
| [0004](0004-secret-resolution-and-redaction.md) | Secret references and redaction | Accepted |

## Status meanings

- **Proposed** — under discussion; not binding.
- **Accepted** — implemented or required by the current design.
- **Superseded** — replaced by a later ADR; the replacement is linked.
- **Deprecated** — retained for history but should not guide new code.

## When to add an ADR

Add one when a decision:

- changes package boundaries or dependency direction;
- introduces a runtime dependency;
- changes persistence, migration, delivery, or identity semantics;
- constrains security or secret handling;
- changes a public contract such as exit codes, JSON schema, or webhook schema;
- resolves an ambiguity that later contributors would otherwise reopen.

Do not add an ADR for routine refactoring, formatting, or trivial naming.
