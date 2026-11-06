# ADR-0019: Worker isolation: continuous claiming and a per-(account, endpoint) in-flight cap

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-07 |
| Deciders | backend-engineer (for lead review) |
| Tickets | HUB-412, HUB-407, HUB-406 |
| DESIGN refs | §10.4, §10.5, §10.8, §11, §18.4; TECH_SPEC §8.1, §12.1 |

## Context
The question to answer: "If one vendor goes down or gets slow while callers keep calling it, what happens to the other vendors?" The sync path was already isolated: each account has its own HTTP transport and connection bulkhead (64), breakers are per (account, endpoint), and a sync call waits only on its own vendor. The async worker was not:

1. **Batch barrier.** `Worker.ProcessOnce` claimed 32 envelopes, ran 16 at a time and waited for the whole batch before claiming again. One envelope for a vendor that answers in 8 s (under its timeout, so never a breaker failure) held the next claim for every other vendor for 8 s.
2. **Shared slots.** The 16 slots were shared by all vendors. A slow or hanging vendor with a backlog filled all of them, so a healthy vendor's due envelopes waited for a slot.

The breaker parks a vendor that is down only after enough failures, and never parks one that is slow but answering. The connection bulkhead caps connections per account, but a worker attempt waiting for a connection still holds a worker slot.

Measured with the noisy-neighbour chaos test (`TestNoisyNeighbour`, 16 slots, claims of 32, A at 20/s): account B's async p99 rose from 50–100 ms to 3.8–4.4 s while A was slow (1.3 s answers) and 2.2–2.4 s while A hung until its timeout. While A refused connections or answered 503 it was sometimes 0.8–1.1 s and sometimes unaffected (fast failures hold slots briefly and the breaker parks A within a second).

Constraints: at-least-once delivery with visibility timeouts and guarded transitions (§11, §10.9); re-resolution at send time (D10), so the outbox row does not know its target account until the worker resolves it; no DB reads on the sync path (§18.5, unaffected here).

## Options considered
1. **Tune the numbers** (more slots, smaller batches). Shrinks the problem, removes neither cause.
2. **One in-memory queue and goroutine pool per target.** Strong isolation, but claimed envelopes would wait in memory with their visibility timeout running, which needs bounded queues, heartbeats and a drain at shutdown. More moving parts than the problem needs.
3. **Store the target at accept time and skip full targets in the claim query only.** Needs the gateway to resolve and persist the account for every envelope. That partly contradicts D10 (the account can change before send) and does not cover envelopes accepted before a cutover.
4. **Rely on the upstream connection bulkhead.** It limits connections, not worker slots: a waiting attempt still holds a slot, so it does not help.
5. **Continuous claiming plus a per-target in-flight cap in the worker, with put-back and a claim hint (chosen).**

## Decision
- **Continuous claiming.** `Worker.Run` claims as soon as a slot frees, `min(HUB_WORKER_CLAIM_BATCH, free slots)` at a time (DESIGN §18.4 and go-system-design "claim n = free slots"). It claims only what can start at once, so there is no in-memory queue. Shutdown stops claiming and waits for the attempts in flight. Visibility timeout, guarded transitions and the poison counter are unchanged. `ProcessOnce` (handle one batch to completion) stays for tests and tools.
- **Per-(account, endpoint) cap.** After resolving an envelope (at send time, D10), the worker takes a slot of its target, the breaker key (account, vendor endpoint ref). If the target already has `HUB_WORKER_TARGET_CAP` attempts in flight in this process, the envelope is put back unattempted (`Outbox.DeferTarget`): `attempt` is decremented, the crash count is untouched, nothing is sent, and the breaker, the retry budget and the live-traffic count see nothing. It is due again at once; the claim filter below holds it back until its target has a free slot.
- **Claim hint and room-aware claims.** The put-back writes the target into the row's `account_id` / `endpoint_id` (columns that `Park` already uses, so drained envelopes carry it too). `Outbox.ClaimWithin` gets the worker's free slots per busy target: rows hinted with a target at its cap are left out before the `LIMIT` (not even locked), rows of a target with free slots are taken at most that many, rows of a target with nothing in flight at most the cap, rows without a hint as usual. A large backlog for a slow vendor is therefore neither claimed and put back over and over nor allowed to fill a claim ahead of other vendors' work, and a busy but healthy vendor's surplus simply stays due until one of its slots frees. The window function runs over at most one claim's rows. The hint is only a filter: the envelope is still resolved at send time.
- **Defaults.** `HUB_WORKER_CONCURRENCY` 256 (messaging) / 64 (llm), `HUB_WORKER_CLAIM_BATCH` 32, as TECH_SPEC §8.1 and §12.1 already promised. The target cap defaults to a quarter of the concurrency, at most 64 (the per-account connection bulkhead, DESIGN §10.8): 64 for messaging, 16 for LLM. Up to three misbehaving targets still leave a quarter of the slots to everyone else, and a target never holds more slots than it has connections.

Rule for future work: a worker must never wait for one envelope before it can start another, and nothing a single (account, endpoint) does may occupy more than its cap of worker slots.

## Consequences
- Positive: with the same 16 slots, B's async p99 while A misbehaves stays within about 60 ms of its baseline in all four scenarios in our runs (asserted bound: + 300 ms; numbers in `docs/tech/ISOLATION.md`). One worker's throughput is no longer bounded by its slowest envelope per batch, which is HUB-407's first acceptance criterion.
- Positive: new metrics `worker_inflight{pool}`, `worker_target_inflight{account, endpoint}` and `worker_target_cap_deferred_total{account, endpoint}` show which target holds the slots. Target labels are breaker keys, bounded like `parked_count`.
- Negative: the cap is per worker process, so a fleet of N workers allows N × cap attempts per target. That is the same scope as the per-process connection bulkhead.
- Negative: an envelope put back over the cap moves behind envelopes that became due meanwhile (its `next_attempt_at` becomes the put-back time). Ordering was never promised without `ordering_key`.
- Negative: a target with free slots but a large due backlog can fill the rest of one claim's window; that claim takes only its free slots of it, the target is then at its cap, and the next claim (immediately, since the loop only idles after an empty claim) skips it. Cost: one extra claim query per freed slot of a saturated target.
- Rejected on the way (measured): the first version put an over-cap envelope back for a fixed 1 s and only skipped targets already at their cap. Under `-race` on the shared 2-CPU runner a healthy B with a cap of 4 (16 slots) briefly had 4 in flight, its fifth envelope was put back for 1 s, and B's p99 jumped to 1.1 s in (a). Room-aware claims removed those put-backs (0 for B in the final runs).
- Negative: one healthy but very busy target gets at most cap concurrent attempts per process: 64 by default, four times the old total of 16.
- The default concurrency changes from 16 to 256 per worker process. Postgres load per process is bounded by the worker pool size (TECH_SPEC §12.2), not by the slot count: each attempt holds a connection only for its short transactions.
- Follow-ups: HUB-407's throughput box (re-run `hubload` async) and the TECH_SPEC §12.1 "claimers + work channel" text, updated here to the single claim loop.

## Deviation from DESIGN.md
- DESIGN §18.4's loop shows `queue.Claim(worker, 32)` followed by handling the batch. The worker now claims `min(batch, free slots)` continuously. This matches the go-system-design skill and TECH_SPEC §12.1's intent, not the literal pseudo-code.
- TECH_SPEC §12.1 described `HUB_WORKER_CLAIMERS` claim loops feeding a bounded work channel, with unstarted envelopes Nacked at shutdown. One claim loop that claims only what it can start is simpler and has nothing to Nack. TECH_SPEC is updated.
- New setting `HUB_WORKER_TARGET_CAP` and a new use of `hub.outbox.account_id` / `endpoint_id` on non-parked rows (a claim hint). No migration: the columns and `Park`'s use of them exist already.
