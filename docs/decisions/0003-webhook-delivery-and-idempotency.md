# ADR-0003: Webhook Delivery, Outbox, and Idempotency

- **Status:** Accepted
- **Date:** 2026-10-22
- **Phase:** 0
- **Related:** [Incident lifecycle](../incident-lifecycle.md), [Persistence](../persistence.md)

## Context

Notifications must survive process crashes and webhook outages without
duplicating Down alerts merely because the process restarted. HTTP is an
unreliable transport: a sender cannot atomically commit both the remote
receiver's state and its own delivery status.

The approved requirement explicitly rejects a claim of exactly-once delivery and
asks for durable event identity, idempotency where supported, and a clear
retry/reconciliation strategy.

## Decision

- Use a generic HTTP `POST` webhook with a versioned JSON payload
  (`schema_version: "1"`).
- Event types: `incident.opened`, `incident.reminder`, `incident.recovered`.
- Generate a UUIDv4 `event_id` per event and use it as the `Idempotency-Key`.
- Assign a unique, monotonically increasing `sequence` per incident.
- Persist a transactional outbox: the incident transition and event insert commit
  together.
- Deliver outside the database transaction. Claim an event atomically
  (`pending`/`failed` → `delivering`), perform HTTP, then commit the result.
- Retry network errors, timeouts, `408`, `425`, `429`, and `5xx` with exponential
  backoff and full jitter, bounded by `max_attempts`.
- Treat other `4xx` and all `3xx` responses as permanent failures.
- Do not follow redirects for webhooks.
- Preserve per-incident order by sequence. A dead event is skipped so a later
  Recovery can still be delivered.
- Reset stale `delivering` rows to `pending` on startup. Keep the event ID stable
  across retries.
- Suppress new reminders while an earlier event for the same incident is pending
  or dead; cancel queued, not-yet-delivering reminders when the incident closes.
- Document at-least-once delivery. Consumers must deduplicate by `event_id`.
- Do not implement HMAC signing in v1. Custom authorization headers over TLS are
  supported; signing is deferred to a reviewed future change.

## Consequences

Positive:

- Incident state can never exist without its intended notification work if the
  transaction commits.
- Restart does not replay historical alerts; only non-terminal outbox rows are
  retried.
- Duplicate Down alerts are prevented independently of webhook reliability.
- Consumers have a stable deduplication key.

Negative and mitigations:

- A crash after remote success but before local status commit can duplicate a
  request. This is unavoidable; the contract documents it and supplies an
  idempotency key.
- The outbox adds a table and worker complexity. This is justified by restart and
  delivery requirements.
- A permanently failing receiver leaves dead events that require operational
  visibility. Logs/status must expose them.
- No cryptographic authenticity in v1. HTTPS and custom auth headers are the
  baseline; signing can be added compatibly via headers.

## Alternatives Considered

- **Fire webhook calls directly from incident transitions:** rejected; network
  failure would either roll back incidents or lose notifications.
- **Retry only in memory:** rejected; restart loses delivery.
- **Exactly-once claim:** rejected; impossible over HTTP without receiver
  cooperation.
- **One notification per probe:** rejected; would spam and conflate failed probes
  with incidents.
- **Provider-specific payloads (Slack/Discord):** rejected by the approved
  generic-webhook requirement.
- **HMAC signing in v1:** deferred to keep the initial contract small; it can be
  added without breaking the JSON schema.
