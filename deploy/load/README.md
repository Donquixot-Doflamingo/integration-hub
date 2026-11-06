# deploy/load — M8 load and soak tests (HUB-235, HUB-236)

Drives a local Hub (gateway + worker + scheduler on Postgres and Redis) and `mockvendor` with
`backend/cmd/hubload`, then writes every result to a run directory. Design and trade-offs:
ADR-0016. Latest results: `docs/tech/LOAD_TEST_REPORT.md`.

**mockvendor is the only vendor.** No request leaves the machine; the real vendor APIs are
never called (AGENTS §3.3). mockvendor answers `POST /1/upload` with a configured fixed latency.

| File | What |
| --- | --- |
| `run.sh` | Builds, resets the load database's outbox and Redis, starts mockvendor and the roles, runs the phases, profiles, writes `report.md` |
| `restart-worker.sh` | Restart hook of the soak (`term` = SIGTERM, `kill` = SIGKILL), restarts worker 1 with the same command |
| `config/` | Registry overlay copied on top of `backend/config`: tenant `load-tenant` (test key `hk_test_load_tenant_local_only`), account `ct-load`, integrations `int_load_sync` (sync), `int_load_async` (async + polling), `int_load_cb` (async_callback → receiver `127.0.0.1:28797`), vendor `clevertap` pointed at `127.0.0.1:28799` with quotas high enough that the Hub, not a quota, is measured |
| `results/` | Run directories (git-ignored) |

## Run

```bash
export HUB_PG_DSN='postgres://hub:hub@localhost:5432/hub_load?sslmode=disable'  # name must contain "load"
export HUB_REDIS_ADDR=localhost:6385                                           # a Redis only this run uses
cd backend && make load                     # all phases, about 25 min on 2 CPUs
PHASES=soak make load                       # or: make soak
PHASES="compare sync" ../deploy/load/run.sh # any subset, in order
```

The script **truncates `hub.outbox` and `hub.callback_deliveries` and flushes the Redis
database**, so it refuses a database whose name does not contain `load`. It waits for the
1-minute load average to drop below `SETTLE_LOAD` (1.0) before the first phase (30 s polls, up
to `SETTLE_MAX`, 40 min) and up to `STEP_SETTLE_MAX` (20 s) before each step. Every result
records the load average at the start and end of its step.

Ports: gateway `28780` (ops `28790`), worker ops `28791`, scheduler ops `28792`, mockvendor
`28799` (control `28798`), soak callback receiver `28797`.

## Phases and knobs (defaults sized for a 2-CPU machine)

| Phase | What it does | Variables (default) |
| --- | --- | --- |
| `compare` | Same 4 KB call, open loop, direct to mockvendor vs through the Hub (sync), for each vendor latency | `CMP_LATENCIES` (`0 50 200`), `CMP_RPS` (100), `CMP_DURATION` (20s) |
| `sync` | L1 ramp at mockvendor 20 ms, then a gateway CPU profile at `SYNC_PROFILE_RPS` | `SYNC_STEPS` (`100 200 300 400 500`), `SYNC_LATENCY_MS` (20), `SYNC_DURATION` (20s) |
| `async` | L2 ramp (async + polling), waits for the queue to drain after each step, then worker and gateway CPU profiles at `ASYNC_PROFILE_RPS` | `ASYNC_STEPS` (`250 500 1000 2000`), `ASYNC_DURATION` (15s), `ASYNC_DRAIN` (100s), `ASYNC_PROFILE_RPS` (1000) |
| `soak` | L5/L6: async_callback traffic while the vendor goes down at random (connection refused, reset, 503, timeouts), then reconciliation | `SOAK_RPS` (100), `SOAK_DURATION` (4m), `SOAK_DRAIN` (6m), `SOAK_RESTARTS` (0 until HUB-406), `SOAK_ARGS` (extra flags, e.g. `-seed 42 -min-up 30s -max-up 60s -min-down 10s -max-down 40s`) |
| all | Profiles last `PROFILE_SECONDS` (20); `WORKERS` (1) worker processes | |

## Full-length run on proper hardware

Give Postgres, the Hub roles and the generator their own cores (separate hosts or at least
`taskset`), Postgres `max_connections` ≥ 200, and set the pool size in the DSN
(`?pool_max_conns=32`) if the default (`max(4, NumCPU)`) is the limit, which it is on 2 CPUs. Then:

```bash
SYNC_STEPS="250 500 750 1000" SYNC_DURATION=10m \
ASYNC_STEPS="1000 2000 3000" ASYNC_DURATION=10m ASYNC_DRAIN=10m WORKERS=4 \
SOAK_RPS=1000 SOAK_DURATION=1h SOAK_DRAIN=30m SOAK_RESTARTS=6 \
  deploy/load/run.sh
```

That is OBSERVABILITY §12 L1 (500 RPS, 10 min), L2 (2,000 msg/s, 10 min) and a 1-hour L5/L6 soak
at half the L2 rate. The weekly staging soak (24 h) uses the same command with `SOAK_DURATION=24h`.

## What a run writes

`<run>/<label>.json` per step (`hubload rate` / `hubload soak` results), `*.cpu.pprof` plus
`*.cpu.top.txt` / `*.cpu.cum.txt` profiles, `soak.ids` (accepted request ids), `run.log`,
`state/` (role logs, generated config and credentials of the run) and `report.md`
(`hubload report -dir <run>`).

Overhead numbers come from three places, and the report shows them side by side:

- **Hub adds** = through-the-Hub latency minus direct latency, measured by the client. Includes the extra network hop and the account limiter's Redis call.
- **Audit ledger** = `hub.requests.hub_overhead_ms` of the requests created in the measured window (server-side, excludes vendor time and the limiter wait).
- **Metric** = the `hub_overhead_ms` histogram delta. Broken until HUB-405 (computed before the vendor call, so it is mostly negative); shown only to track the fix.
