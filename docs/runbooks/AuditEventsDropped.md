# AuditEventsDropped

**Severity:** warn · **Rule group:** `hub-platform` · **Labels:** `reason` (`buffer_full`, `write_error`)

## What fired
`sum by (reason) (increase(audit_events_dropped_total[5m])) > 0`, plus a branch for the first drop of a new `reason`. No `for`: one dropped event fires. Audit writes are non-blocking on the request path (DESIGN §18.5): when they cannot be queued or written, the rows are dropped and counted instead of slowing callers.
- `buffer_full`: the in-memory audit queue (10 000 batches per process, fixed in code) was full.
- `write_error`: a batch write to Postgres failed; all its rows count (and `audit_write_failures_total` goes up by one).

## Impact
- Callers see nothing: requests are served normally.
- The affected requests have an incomplete audit trail (missing events or final records). For `sealed` integrations (bureau, Aadhaar, PAN) this is a compliance gap. Delivery state is unaffected: the outbox, not the audit trail, drives async delivery.

## First 5 minutes
1. **Platform** dashboard (`hub-platform`), row *Audit, config and credentials*: **Audit drops and write failures** and **Audit buffer fill** (per instance).
2. Which nodes and how many: `sum by (instance, reason) (increase(audit_events_dropped_total[15m]))` and `max by (instance) (audit_buffer_fill_ratio)`.
3. Logs: `audit write failed` (ERROR, with `events`, `records`, `error`) and `audit partition maintenance failed`.
4. Postgres health: connections, locks, disk, slow inserts into `request_events` / `requests`.
5. Load: **Requests by mode** on the same dashboard; a traffic spike fills the buffer faster than one writer drains it.

## Likely causes → fix
| Cause | How you know | Fix |
| --- | --- | --- |
| Postgres slow | `buffer_full` with fill ratio near 1; slow writes, no errors | Fix Postgres (locks, I/O, connections). The buffer size is not configurable; spreading traffic over more gateway or worker processes also helps. |
| Postgres down or refusing writes | `write_error`, `audit write failed` with connection errors | Restore Postgres. Rows written during the outage are lost; record the window. |
| Missing daily partition | `write_error` with "no partition" style errors; `audit partition maintenance failed` earlier | The writer creates partitions 7 days ahead every 6 hours; fix what blocks it (permissions, DB down) and the next run (or a process restart) creates them. |
| Traffic spike | `buffer_full` only at peak, Postgres fine | Add processes for the role that drops (gateway or worker). |

## Escalate when
- Drops affect a `sealed` integration (tell compliance: which window, which integrations).
- `write_error` lasts more than 5 minutes.
- Drops continue with Postgres healthy.

## Related
- [HubOverheadP99High](HubOverheadP99High.md), [runbook index](README.md)
- DESIGN §13.4 (storage, bounded channel and drop counter), §18.5 rule 3
- [F-08 Audit trail](../features/F-08-audit-trail.md), [OBSERVABILITY §3.2, §9.1](../tech/OBSERVABILITY.md)
