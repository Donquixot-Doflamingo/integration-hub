# ADR-0024: The 4 MB LLM body limit and the llm worker pool wait for an explicit integration setting

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-07 |
| Deciders | backend-engineer (for lead review) |
| Tickets | HUB-433 (change), HUB-439 (follow-up) |
| DESIGN refs | §16.5 (body limits), §17 (worker pools); ADR-0023 |

## Context
Two behaviours were keyed on the capability **name**:
- the edge allowed 4 MB request bodies instead of 1 MB when the integration's capability started with `llm.` (DESIGN §16.5);
- the gateway queued async requests of `llm.*` capabilities in the `llm` worker pool instead of `messaging` (DESIGN §17).

ADR-0023 removes capabilities, so there is no name left to key on. Contracts 2.0.0 (HUB-432) add no replacement field on the integration.

## Options considered
1. Key on the integration ID (for example `int_llm_*`). A naming convention with behaviour hidden in it; callers and operators cannot see it in the contract.
2. Add an integration field (`body_limit_bytes`, `pool`) without a contract. Not allowed: contract first.
3. **Every integration gets the 1 MB limit and the `messaging` pool until an explicit, contracted integration setting exists.** Envelopes keep their `pool` field, so the `llm` pool, its claims and `Replay` keep working for envelopes that name it.

## Decision
Option 3. `edge.DefaultBodyLimit` (1 MB) applies to every integration and the gateway queues every request as `messaging`. HUB-439 asks the contract steward for an explicit integration setting and wires it to both.

## Consequences
- No integration in the seed config, examples or tests used an `llm.*` capability, so nothing in this repo changes behaviour.
- An LLM integration with bodies over 1 MB gets `413` until HUB-439 lands, and its async work shares the `messaging` pool's concurrency.
- Envelopes queued or dead-lettered before the change keep their pool: migration 00011 writes `pool: llm` into every `llm.*` envelope before it drops the capability key, so they and their replays stay in the `llm` pool.
- The `llm` pool stays in the worker, queue and metrics (`queue_depth{pool}`); new work does not reach it until HUB-439.

## Deviation from DESIGN.md
- §16.5 "4 MB LLM" and §17's capability-driven pool choice are suspended until HUB-439.
