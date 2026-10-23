# Governance

## Status

AboueiChecker is a single-maintainer open-source project led by Erfan Abouei.
There is no steering committee, technical board, or multi-party voting
process. This document describes how decisions are actually made so
contributors know what to expect.

## Roles

- **Maintainer (Erfan Abouei):** sets scope and roadmap, reviews all changes,
  makes releases, and has final say on technical and licensing decisions.
- **Contributors:** anyone who opens issues, proposes changes, or improves
  documentation. Contributors have no merge or release authority.

## Decision-making

- Routine fixes and documentation improvements are accepted by maintainer
  review of a pull request.
- Scope, CLI contract, configuration schema, exit codes, and release
  decisions are made by the maintainer, informed by open issues.
- Architectural decisions that constrain the implementation are recorded as
  ADRs under `docs/decisions/`; the maintainer approves new ADRs.
- If consensus cannot be reached, the maintainer decides. The reasoning is
  recorded in the relevant issue or ADR.

## Changes to this model

If additional maintainers join, this document and `MAINTAINERS.md` will be
updated before any shared authority is exercised. Until then, do not assume
collective ownership, rotating review duties, or quorum-based merges.
