# Live latency demo: a public API, directly and through the Hub

`run.sh` sends the same small JSON `POST` to a free, open echo API ([Postman Echo](https://learning.postman.com/docs/reference/developer-resources/echo-api), `https://postman-echo.com/post`, no account, no key) **N times directly** and **N times through a local Hub** (`int_latency_echo`), then prints p50 / p95 / p99 for both, the difference, and the Hub's own measurement of its overhead. Ticket: HUB-353.

**Everything here is an example**, not production config. Every schema lives inline in the config (ADR-0023 in `docs/tech/adr/`).

## Run it

Prerequisites: Go, curl, python3, Postgres + Redis (`docker compose -f deploy/docker-compose.yml up -d`). From the repository root:

```bash
examples/latency-demo/run.sh                        # N=200 per path, C=4 workers, 4 alternating rounds
N=500 C=8 examples/latency-demo/run.sh              # more samples (it is a free shared service: stay modest)
LATENCY_TARGET=mock examples/latency-demo/run.sh    # offline: mockvendor stands in, answering in 50 ms
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `N` | 200 | requests per path (direct, via Hub); rounded down to a multiple of `C × ROUNDS` |
| `C` | 4 | concurrent workers per path; each keeps one connection alive |
| `ROUNDS` | 4 | direct and Hub runs alternate this many times, so both see the same internet weather |
| `WARMUP` | 2 | requests per worker and round that are sent but not counted (connection and TLS setup) |
| `LATENCY_TARGET` | `public` | `public` (the internet) or `mock` (mockvendor on this machine) |

Output from a `LATENCY_TARGET=mock` run on the 2-CPU build machine, ports shown as the defaults (yours will differ):

```text
Target: mock   direct: POST http://127.0.0.1:8099/post   via Hub: POST http://127.0.0.1:18080/v1/integrations/int_latency_echo/invoke
               n  errors    p50 ms    p95 ms    p99 ms   mean ms
direct       192       0      50.9      51.5      52.9      50.9
via Hub      192       0      51.6      53.5      54.6      51.7
difference                    +0.7      +2.1      +1.7      +0.9

Hub's own overhead (hub_overhead_ms over 224 requests, warm-up included): mean 0.28 ms, p50 ~0.18, p95 ~0.65, p99 ~1.79 ms (bucket estimates)
```

Raw samples (`http_code seconds`, one line per request) are kept in `.demo/latency-demo/results/`.

## How to read the numbers

- **Internet jitter dominates.** A round trip to a public API takes tens to hundreds of milliseconds and varies by several milliseconds from one request to the next, more at p95/p99. The Hub adds about a millisecond. On the `public` target the *difference* row can therefore be a few milliseconds either way, even negative: that is the internet, not the Hub. Compare **deltas**, not absolute numbers, and repeat the run before drawing conclusions.
- **p99 needs samples.** With N=200, p99 is the second-slowest request of 200. Use `N=500` or more if you care about the tail, and keep `C` small so you measure latency, not the service's rate limits.
- **What "via Hub" adds:** one extra local hop (curl → Hub on localhost), input validation against the operation's request schema, transform, breaker and limiter checks (no credential: auth `none`), response mapping and audit. The Hub then makes the same HTTPS call to the echo API over its own pooled connection. Both paths reuse connections after the warm-up, so TLS handshakes are not counted on either side.
- **The Hub's own overhead line** is the gateway's `hub_overhead_ms` histogram (server time minus vendor time and deliberate waits), scraped from its metrics endpoint before and after the run. It does not depend on the internet, so it is the stable number to quote. Its percentiles are bucket estimates; the per-request values are in the audit ledger (`hub.requests.hub_overhead_ms`).
- **For controlled numbers** (mockvendor at fixed latencies, load steps, CPU profiles) see [`docs/tech/LATENCY.md`](../../docs/tech/LATENCY.md) and the M8 load report [`docs/tech/LOAD_TEST_REPORT.md`](../../docs/tech/LOAD_TEST_REPORT.md). `LATENCY_TARGET=mock` here gives a quick local version of the same comparison.

## What is in this folder

| Path | What |
| --- | --- |
| `config/vendors/postman-echo.yaml` | Auth scheme `none`; regions `public` (`https://postman-echo.com`) and `mock` (mockvendor); endpoint `echo@1` (`POST /post`, 5 s) with its `request_schema` (`Content-Type`, body `message`, `seq`) |
| `config/accounts/echo-public.yaml` | The account, without credentials; `run.sh` switches its region for `LATENCY_TARGET=mock` |
| `config/tenants/latency-demo.yaml` | The caller; local test key `hk_test_example_latency_demo_local_only` |
| `config/integrations/int_latency_echo/` | Operation `default` with request schema `{"message", "seq"?}`; `sync`, `fail_fast`, `masked` audit, no `Idempotency-Key` needed (echoing moves nothing) |
| `config/transformers/ECHO-REQ`, `ECHO-RES` | The mapping with goldens; the response is `{"status": "echoed", "message", "seq"}` (the echoed request headers never reach the caller) |
| `mockvendor/echo.json` | The offline stand-in: same response shape, fixed 50 ms |
| `run.sh` | The demo (uses `../lib/local-hub.sh`) |

The echo API needs no credentials, so its vendor uses the auth scheme `none` and the account has no credentials at all (nothing is stored with `hubctl creds set`, nothing is injected). The account limiter is set to 100 requests/s so it never delays the demo (a delayed request would show up as "Hub latency" in the client-side numbers while the Hub's own overhead excludes it).

## Tests

`TestExampleLatencyConfigBuilds` (config and goldens) and `TestExampleLatencyEcho` (integration: the integration through the gateway against mockvendor with the same scenario file; the public region is redirected to mockvendor, so CI never calls the internet). `run.sh` itself talks to the internet and is run by hand.
