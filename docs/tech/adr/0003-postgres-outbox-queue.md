# ADR-0003: Postgres outbox behind a Queue interface (phase 1)

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-10-31 |
| Deciders | lead |
| DESIGN refs | D9, §11, §10.5, §10.9, §18.2 |

## Context
Async and wait modes promise zero loss of accepted requests: "accepted" must mean "committed". We need delayed retries, park/release per `(account, endpoint)`, TTL expiry, DLQ and guarded state transitions. Phase-1 volume target: 2,000 async msg/s (M8).

## Options considered
1. **Postgres outbox** with `FOR UPDATE SKIP LOCKED` — transactional with the ledger, native delays (`next_attempt_at`), park/release by `status, account_id, endpoint_id`; ~2–5k msg/s.
2. **NATS JetStream / RabbitMQ** now — higher throughput, but needs a transactional-outbox relay anyway for "accepted = committed", and delayed retry/park semantics are awkward.
3. **Kafka** — poor fit for retry-heavy, per-key park/release workloads (DESIGN §11.1).

## Decision
Option 1, behind the `Queue` interface in DESIGN §11.2 (`internal/queue`). Implementation rules (TECH_SPEC §6.3): outbox row deleted on terminal ack in the same TX as the `requests` status change; partial indexes for claim, release, reclaim and TTL; `pool` column for messaging vs LLM workers; visibility timeout 30 s; gateway wait-mode inserts rows pre-claimed by itself.

## Consequences
- Zero-loss is provable by a reconciler query (OBSERVABILITY S4).
- Headroom risk: 2,000 msg/s target vs ~2–5k msg/s ceiling, sharing the same Postgres as audit writes. Mitigations: audit `COPY` batching, small outbox (deletes), aggressive autovacuum, separate pgxpool sizes; load test L3 is the gate. If it fails, move audit events to a separate database before changing the queue.
- Phase 3 broker swap keeps the interface; the outbox becomes the relay source.

## Deviation from DESIGN.md
Adds a `pool` column and partial (status-filtered) indexes; same keys as §18.2.
