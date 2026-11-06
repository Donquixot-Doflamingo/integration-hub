# ADR-0012: Trace context across the outbox, callbacks and to vendors

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-06 |
| Deciders | backend-engineer (for lead review) |
| DESIGN refs | §11.3, §16.4, §18.5; TECH_SPEC §22 q9; OBSERVABILITY §5 |

## Context
DESIGN §16.4 asks for spans on the edge, transform, upstream and callback, to accept the caller's `traceparent` and to forward it "to vendors that accept it". HUB-231 must show one trace linking caller → gateway → worker → vendor → callback for an async request. Four questions were open in practice:

1. **Which vendors accept `traceparent`?** DESIGN has no vendor-definition field for it (TECH_SPEC §22 q9). The vendor shape is part of the admin API (`contracts/openapi/hub-admin.v1.yaml`), so a new field is a contract change.
2. **What does the envelope store?** Before HUB-231 the edge copied the caller's raw `traceparent` header into `envelope.trace`, so the worker could only point at the caller's span, never at the Hub's own, and stored nothing when the caller sent no header.
3. **How does a callback find its request's trace?** `hub.callback_deliveries` rows are claimed later by a dispatcher with no request context; the row had no trace field, and the callback body is contract-defined, so it cannot carry one.
4. **Sampling across the hop.** OBSERVABILITY §5 makes the worker's attempt a new root *linked* to the accepting trace (so a retry hours later does not stretch it). A parent-based sampler ignores links: with the default 5 % root ratio, a sampled async request would lose its worker and callback spans 95 % of the time.

## Options considered
For (1):
- **A. Vendor-definition flag** (`propagate_trace: true`) — the right long-term home, but it needs an admin contract change (and a frontend regeneration) before any code.
- **B. Process config** `HUB_TRACE_FORWARD_VENDORS` (comma-separated vendor names) feeding the existing `upstream.Options.ForwardTraceparent` allowlist; default empty (no vendor gets our trace ids).

For (3):
- **C. New nullable column** `hub.callback_deliveries.traceparent`, written by `callbacks.Enqueue` from the transaction's context.
- **D. Look the trace up from `hub.requests.trace_id`** at delivery time — one extra read per callback, and a trace id alone cannot form a span link (no span id).

## Decision
- **B** for vendor forwarding, until a vendor-definition field is contracted (change request in the HUB-231 report). The upstream client still strips any `traceparent`/`tracestate` a transformer maps, and sends only the Hub's own `traceparent` (never `tracestate`) to listed vendors.
- The envelope stores the traceparent of a new **`hub.queue.enqueue` producer span** (child of the edge server span), written in the same transaction as the outbox row. It is the Hub's own context, present whether or not the caller sent one.
- **C** for callbacks (migration `00010_callback_trace.sql`, append-only, nullable, format-checked). `hub.callback.deliver` is a new root linked to the terminal transition's span (the worker attempt, or the edge span for wait mode).
- `telemetry.Sampler` = parent-based with a **link-aware root**: a new root is sampled when any of its links is sampled, else the root ratio applies.
- The edge server span keeps the skill's `{method} {route}` name (otelhttp + route pattern; the edge sets it itself because otelhttp never sees the mux's request copy), and the upstream span keeps `upstream {vendor}.{endpoint}`; OBSERVABILITY §5.1 now lists those names instead of `hub.edge` / `hub.upstream`.
- `hub.requests.trace_id` is filled from the edge span, so the request explorer links to the trace.

## Consequences
- An async request is one linked chain: caller span → edge → `hub.queue.enqueue` ⇢ (link) `hub.worker.attempt` → transform / upstream (→ vendor via `traceparent`) ⇢ (link) `hub.callback.deliver`. Every span carries `hub.request_id`. Proven by `TestAsyncTraceLinksCallerToCallback`.
- Retries and drains link to the same enqueue span; each attempt is its own short trace.
- Forwarding is per process, not per vendor definition: every gateway and worker must get the same `HUB_TRACE_FORWARD_VENDORS`. When the contract gains a vendor field, the config becomes a fallback and is then removed.
- The callback row grows by at most 55 bytes. Rows written by the scheduler (expiry) carry no traceparent and their delivery span is an unlinked root.
- Head sampling of linked roots follows the accepting trace; collector tail sampling (errors, > 1 s) is unchanged.
