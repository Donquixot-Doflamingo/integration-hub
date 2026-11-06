# ADR-0016: A Go open-loop load tool (`hubload`) and laptop-scale load and soak runs

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-07 |
| Deciders | backend-engineer (for lead review) |
| Tickets | HUB-235, HUB-236, HUB-405, HUB-406 |
| DESIGN refs | §1 success metrics, §18.6, §19 M8; OBSERVABILITY §12; TESTING_STRATEGY §10 |

## Context
DESIGN §19 M8 and OBSERVABILITY §12 name **k6** (vegeta for ad-hoc checks) for the M8 load test, with accepted request ids recorded by the k6 script and reconciled against `requests`. Four things got in the way of doing that literally:

1. Neither k6 nor vegeta is installed in the dev container or CI, and the egress allowlist does not cover their release downloads. Adding them means a second toolchain (JavaScript for k6) next to Go.
2. The report needs more than client latency: per-step server metrics (`hub_overhead_ms`, stage durations, `queue_oldest_age_s`, `queue_depth`) as deltas between two scrapes, outbox sampling, per-process CPU, a callback receiver that counts deliveries per request, a chaos loop driving the mockvendor control API, and SQL reconciliation. In k6 that is several extensions or side scripts.
3. The product owner's question ("how much latency does the Hub add") needs a direct-to-vendor baseline with the same body and the same open-loop arrivals as the Hub path.
4. The target hardware is a 2-CPU machine shared by Postgres, Redis, mockvendor, the generator and the Hub roles (F-18 open question). The 10-minute / 2,000 msg/s and multi-hour soak runs of OBSERVABILITY §12 do not fit in the time a laptop run should take.

While building it, the run also found that the exported `hub_overhead_ms` histogram is computed with a timestamp taken before the vendor call (HUB-405). It reads "prepare time minus vendor time" and is negative whenever the vendor is slower than the edge's prepare stage. The audit ledger column `hub.requests.hub_overhead_ms` is computed at record time and is correct. Separately, a worker restarted while a backlog exists panics on a nil snapshot (HUB-406), so the soak cannot include worker restarts yet.

## Options considered
1. **k6 + side scripts** (as planned). Needs a pinned k6 binary, a JavaScript scenario, and shell/Go helpers for scraping, chaos, the receiver and reconciliation. Two languages, more moving parts.
2. **vegeta as a library.** It is constant-rate and Go, but it is still a new dependency and covers only the generator part.
3. **A small Go tool in the backend module (chosen).** `backend/cmd/hubload` with `rate`, `soak`, `report` and `settle` commands. It uses only modules already in `go.mod` (pgx, Prometheus client_model/common for the protobuf scrape). It is open loop: arrival i is sent at `start + i/rate` whether or not earlier ones have answered. Latency is measured from the intended send time, and arrivals beyond `-max-inflight` are counted as dropped, never delayed (no coordinated omission). A unit test proves a stalled server shows up as latency on every arrival behind the stall.

## Decision
- Option 3. `hubload` is an outside caller: it talks to the Hub only through the contracted invoke API, `/metrics`, and read-only SQL against `hub.requests`, `hub.outbox` and `hub.callback_deliveries` for reconciliation. The archtest keeps it out of `internal/` like `hubmcp` and mockvendor. The harness (`deploy/load/run.sh`, the registry overlay `deploy/load/config/`, `restart-worker.sh`) lives in `deploy/load/` as TESTING_STRATEGY §1 says.
- **Laptop scale by default**: 20 s steps for the comparison and the sync ramp, 15 s async steps with up to 100 s drain, a 4-minute soak with up to 6 minutes of reconciliation. Every size is an environment variable. `deploy/load/README.md` gives the full-length settings (10 min at 2,000 msg/s, 1 h+ soak) for proper hardware.
- **Server-side overhead is read from the audit ledger** (`hub.requests.hub_overhead_ms` of the requests created in the measured window, via `percentile_cont`) until HUB-405 fixes the metric. The tool still reports what the metric says, including its share of negative observations, and decodes native histogram buckets (positive and negative) so the reading is faithful.
- **The soak runs vendor outages only** (`SOAK_RESTARTS=0`) until HUB-406 is fixed. The restart hook is built and was exercised (a SIGTERM restart and a SIGKILL restart); switch the default to 2 afterwards.
- Before each measurement run the harness waits until the 1-minute load average is below 1.0 (30 s polls, up to 40 min), and every result records the load average at the start and end of its step.

## Consequences
- Positive: one language, no new dependency, one command (`make load`) produces every number in `docs/tech/LOAD_TEST_REPORT.md`, including the direct-vs-Hub comparison and zero-loss reconciliation that does not depend on Hub metrics.
- Costs: the generator shares the CPUs it measures (its own CPU is reported per step, and a send-lag column flags a starved generator). Laptop-scale durations cannot prove the 10-minute sustain criteria; those acceptance boxes stay open until a run on proper hardware.
- The ledger value excludes the account limiter's Redis round trip, which the Hub counts as a deliberate wait (HUB-408). The report therefore also shows the client-side difference between direct and through-the-Hub latency, which includes it.

## Deviation from DESIGN.md
DESIGN §19 M8 and OBSERVABILITY §12 say k6 (or vegeta). We use `hubload` instead, with the same scenarios (L1, L2 and L5/L6 in reduced form) and the same reconciliation idea. OBSERVABILITY §12 and TESTING_STRATEGY §10 are updated to say so.
