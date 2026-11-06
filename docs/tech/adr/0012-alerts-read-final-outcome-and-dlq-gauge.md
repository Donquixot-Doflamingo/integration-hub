# ADR-0012: Alerts read the final-outcome counter and the DLQ gauge until attempt counters exist

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-06 |
| Deciders | backend-engineer (for lead review) |
| Tickets | HUB-232, HUB-245 |
| DESIGN refs | §16.2, §16.3, §10.2, §4.4 |

## Context
DESIGN §16.3 asks for pages on "> 50 DLQ entries / 10 min / account", "any `UPSTREAM_AUTH_FAILED`" and "any `TRANSFORM_FAILED`". OBSERVABILITY §11.1 planned these on two counters that are not in DESIGN §16.2 and that no code exports today: `vendor_attempts_total{code}` (attempt-level codes) and `dlq_entries_total{account}` (dead-letter writes). HUB-232 must ship rules that work on what the binary exports now (HUB-230 delivered the §16.2 set), so a rule never queries a metric that does not exist.

Two Prometheus facts shape the rules:
- `hub_requests_total` and `audit_events_dropped_total` create a series on its first increment. `increase()` needs two samples of a series, so a plain `increase(...) > 0` never sees the first failure of a new label set: exactly the "any" case.
- `dlq_count` is a gauge of unreplayed dead letters, sampled by the scheduler leader every 10 s and cleared when leadership moves. It cannot express "entries written" when replays or purges happen in the same window.

## Options considered
1. **Wait for the counters (HUB-245) before shipping these three alerts.** Exact, but leaves three paging alerts out of M8 for a metric-only change.
2. **Add the two counters inside HUB-232.** Grows an infra ticket into a delivery and queue change (new metrics, owners, tests), against the one-ticket-per-change rule.
3. **Rules on the metrics that exist now (chosen).** `UpstreamAuthFailed` and `TransformFailed` read `hub_requests_total{code=...}`; `DLQSurge` reads the net growth of `dlq_count` per account. Each "any" rule adds a new-series branch: a series that exists now and had no sample in the hour before the last 5 minutes is new, and its whole value is new events. `DLQSurge` compares the account total with 10 minutes ago and, for an account without dead letters in the 10 minutes before that, takes its whole backlog as new.

## Decision
Option 3. Rules use only exported names (enforced by `TestAlertRulesUseExportedMetrics`). HUB-245 adds `vendor_attempts_total` and `dlq_entries_total` and switches the three rules to them.

## Consequences
- Positive: all ten §16.3 alerts exist and are unit-tested now; the first failure of any label set pages (promtool test "first auth failure of a new label set pages").
- `UPSTREAM_AUTH_FAILED` is never retried (sync: returned; async: dead-lettered at once, `delivery.Retryable`), so the final counter sees every one. `TRANSFORM_FAILED` is also dead-lettered at once, except a transformer time-budget overrun on an async attempt, which retries and only counts if the request finally fails.
- `DLQSurge` measures net growth: dead letters replayed or purged within the same 10 minutes hide as many new ones. A missing baseline sample (leader change at exactly t−10m) suppresses the alert for that evaluation instead of paging on the whole backlog (promtool test "dlq backlog across a leader change does not page"). After a TSDB wipe or a scheduler scrape gap longer than 20 minutes, an account with a backlog over 50 can page once.
- The new-series branch can fire once for counters that already had a non-zero value when Prometheus starts with an empty TSDB.
- Follow-up: HUB-245.

## Deviation from DESIGN.md
None in behaviour: every §16.3 alert exists with its threshold and severity. The DLQ alert approximates "entries written" with net growth of the unreplayed backlog until HUB-245 (see Consequences). OBSERVABILITY §11.1 is updated to the shipped expressions.
