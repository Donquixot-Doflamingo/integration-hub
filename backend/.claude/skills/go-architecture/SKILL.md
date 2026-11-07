---
name: go-architecture
description: "Package boundaries, allowed import graph and wiring for the Integration Hub Go backend (DESIGN 18.1 and 18.3) - ports and adapters for Postgres, Redis and HTTP, where new code goes (new vendor, new API or integration, auth scheme, middleware, role), shared role binaries, config loading, constructor injection and graceful shutdown order. Use whenever you create a package or file under backend/internal or backend/cmd, add an import between internal packages, wire a dependency in main, add a role or binary, or wonder where something belongs."
---

# Go architecture for the Integration Hub

Source of truth: DESIGN §17–§18 and `docs/tech/TECH_SPEC.md` (if TECH_SPEC disagrees with this skill, TECH_SPEC wins; fix the skill). Any new top-level package or change to the import rules needs an ADR.

## Layout (DESIGN §18.1 + hubmcp)
```text
cmd/hub          gateway | worker | scheduler | admin   (one binary, role = first arg)
cmd/hubctl       plan, apply, transform test, dry-run, replay
cmd/mockvendor   programmable fake vendor (tests + local dev)
cmd/hubmcp       MCP server for Hub users (official Go SDK)
internal/core    (proposed leaf) shared value types + canonical error codes; stdlib only
internal/{edge,registry,transform,resolve,creds,upstream,queue,delivery,sessions,audit,callbacks,webhooks,control,telemetry}
pkg/hubclient    public Go SDK: Result / Queued types
config/          dev registry YAML (+ transformer fixtures)
migrations/      goose SQL
```

## Layers and allowed imports
Dependencies point **down** only. A package may import anything below it, never above or sideways at the same level unless listed.

| Layer | Packages | May import |
| --- | --- | --- |
| 0 leaf | `core` | stdlib |
| 1 platform | `telemetry` | core, OTel, slog |
| 2 pure domain | `transform` (pure, no I/O), `resolve` | core, telemetry, expr, jsonschema |
| 3 state + adapters | `registry`, `creds`, `queue` (+`queue/postgres`), `sessions`, `audit`, `upstream` | layers 0–2, pgx, go-redis, net/http (`upstream` only for vendors) |
| 4 orchestration | `delivery`, `callbacks`, `webhooks` | layers 0–3 |
| 5 entry | `edge`, `control` | layers 0–4 |
| 6 wiring | `cmd/*` | everything |

Hard rules:
- `transform` is pure: no `net/http`, `os`, `database/sql`, pgx, Redis, `time.Now` (use injected clock). Enforced with depguard (see `go-idioms/references/lint.md`).
- `registry` compiles schemas and transformers, so it imports `transform`; `transform` never imports `registry`.
- `edge` and `control` never import each other. Shared helpers go down a layer.
- No `internal/*` package imports `cmd/*`.
- `pkg/hubclient` imports **nothing** from `internal/` (it is public and must build alone).
- `cmd/hubmcp` talks to the Hub only through the contracted HTTP API (`pkg/hubclient` or a client generated from `contracts/`), never `internal/` — AGENTS.md §4: contracts are the only interface between backend and the MCP server.
- `cmd/mockvendor` imports nothing from `internal/` either: it must behave like an outside vendor.
- No cycles (the compiler enforces it; if you hit one, a type is in the wrong layer — move it to `core` or invert with an interface).

Check the graph:
```bash
go list -f '{{.ImportPath}}: {{join .Imports " "}}' ./internal/... | grep integration-hub/backend/internal
```
Add an architecture test (`internal/archtest`) that runs `go list -deps -json` per package and asserts the table above, so a violation fails `make test`.

## Ports and adapters
The §18.3 interfaces are the ports. Declare each **in the package that consumes it**, minimal method set; implement in the adapter package.

| Port | Consumer(s) | Adapter(s) |
| --- | --- | --- |
| `Registry` (`Snapshot()`) | edge, delivery, control | `registry.Store` (atomic pointer) |
| `Resolver` | edge, delivery | `resolve` (pure function over snapshot) |
| `Transformer` | edge, delivery, control (dry-run, goldens) | `transform.Engine` |
| `CredentialSource` | transform injection step in edge/delivery | `creds/localfile`, `creds/kms`, `creds/vault` + cache |
| `Upstream` | edge (sync), delivery (worker) | `upstream.Client` (pools, breaker, limiter, bulkhead) |
| `Breakers` | upstream, delivery, scheduler | `upstream/breaker` local cache + Redis sync |
| `AuditSink` | everyone on the request path | `audit.BufferedWriter` → Postgres |
| `Queue` | edge (wait/async), delivery, scheduler | `queue/postgres` (phase 1); NATS/Rabbit later |
| `IdempotencyStore`, `RateLimiter` | edge, upstream | Redis adapters |

Adapters own their SQL/Redis keys; nobody else touches those tables or keys. Swapping an adapter (e.g. NATS queue, Vault creds) must not change any consumer.

## Where new code goes
| Change | Where | Also |
| --- | --- | --- |
| New vendor | Shared environments: console or admin API (vendor, endpoints with `request_schema`, accounts, transformers with samples). Local dev: `config/vendors/<vendor>.yaml`, accounts in `config/accounts/`, transformers + fixtures in `config/transformers/` | **No Go code** unless it needs a new auth scheme or stdlib function. See `hub-transformers`. |
| New API for callers (new integration or operation) | Registry data, not code or contracts (ADR-0023): operations with their `request_schema` in the integration version (`config/integrations/<id>/versions/<n>.yaml` locally) | `interactive` on the integration; publish validation already generic (`schemas_compile`, `input_schema`, `endpoint_schema`) |
| New auth scheme | `internal/creds/auth/<scheme>.go` implementing the scheme interface, registered in the scheme table | Unit tests with fake creds; secrets only via `creds.*`; redaction registration |
| New stdlib expression function | `internal/transform/stdlib/` | Pure, bounded, typed for the checker; platform code release only (§7.3) |
| New HTTP middleware | `internal/edge/mw/` | Own duration metric, < 100 µs p99, bench, position in the §18.4 chain documented |
| New admin endpoint | `internal/control/` | Must exist in `contracts/openapi/` first (`api-contract-backend`) |
| New background job | `internal/delivery` (scheduler jobs) | Runs only on the leader; idempotent; metric + alert |
| New role | `cmd/hub/<role>.go` wiring only | Roles share all packages; a role never forks business logic |
| New table | `migrations/` + adapter package queries | `postgres-patterns` |

## Role binaries share code
`hub gateway|worker|scheduler|admin` run the same `transform`, `resolve`, `upstream` code (DESIGN §17). Role files in `cmd/hub` only build the dependency graph and start loops. If you are tempted to write `if role == "worker"` inside `internal/`, pass a different dependency or option instead.

## Config loading
- Process config (DSNs, ports, role, timeouts) comes from env + flags, parsed once in `main` into a typed struct. Registry config (vendors, integrations…) comes from snapshots, never from env.
- Make the loader pure and testable: no globals, inject args and env.
```go
type Config struct {
	Role         string
	HTTPAddr     string
	InternalAddr string // pprof, metrics; never public
	PostgresDSN  Secret
	RedisURL     Secret
	ShutdownWait time.Duration
	ReadyDrain   time.Duration // delay between readyz=503 and Shutdown
}

type Secret string

func (Secret) LogValue() slog.Value { return slog.StringValue("***") } // never logged

func Load(args []string, getenv func(string) string) (Config, error) {
	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	c := Config{}
	fs.StringVar(&c.HTTPAddr, "http-addr", envOr(getenv, "HUB_HTTP_ADDR", ":8080"), "public listen address")
	fs.StringVar(&c.InternalAddr, "internal-addr", envOr(getenv, "HUB_INTERNAL_ADDR", "127.0.0.1:9090"), "metrics/pprof address")
	fs.DurationVar(&c.ShutdownWait, "shutdown-wait", 25*time.Second, "graceful shutdown budget")
	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parse flags: %w", err)
	}
	c.PostgresDSN = Secret(getenv("HUB_POSTGRES_DSN"))
	// ... validate required fields, return errors.Join of all problems
	return c, nil
}
```
- Flags override env; env overrides defaults. Validate everything at startup and fail fast with all errors at once.

## Dependency injection
Plain constructors, wired by hand in `cmd/hub/<role>.go`. No DI framework, no service locator, no `init()` registration.
```go
func runWorker(ctx context.Context, cfg Config, log *slog.Logger) error {
	pool, err := pgxpool.New(ctx, string(cfg.PostgresDSN))
	if err != nil { return fmt.Errorf("postgres: %w", err) }
	defer pool.Close()

	reg := registry.NewStore(registry.NewLoader(...))
	q := pgqueue.New(pool)
	up := upstream.NewClient(upstream.Options{ /* ... */ })
	w := delivery.NewWorker(q, reg, resolve.New(), transform.NewEngine(), up, audit.NewWriter(pool, log), log, delivery.WorkerOptions{})
	return w.Run(ctx)
}
```
If wiring grows large, split into small `build*` helper functions in the same `cmd` package; still no framework.

## Graceful shutdown (order matters)
Use `signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)`. Total budget < the pod's `terminationGracePeriodSeconds`.

Gateway:
1. Flip `/readyz` to 503 (keep `/healthz` 200) so the load balancer stops routing.
2. Wait a short propagation delay (e.g. 5 s) while still serving.
3. `srv.Shutdown(shutdownCtx)` — stops accepting, waits for in-flight requests. Wait-mode requests are safe: the envelope is already in the outbox, the worker finishes them.
4. Cancel background loops (snapshot poller, breaker sync, credential warmer) and wait for them.
5. Flush the audit buffer (bounded time; count anything dropped).
6. Shut down OTel meter/tracer providers (flush).
7. Close Redis, then the Postgres pool **last** (everything above may still write).

Worker: stop claiming → finish or release in-flight envelopes (release = reset `claimed_until` so another worker picks it up now rather than after the visibility timeout) → flush audit → close stores. Never `Ack` work that did not finish.

Scheduler: stop jobs → release the advisory lock by closing its dedicated connection → standby takes over.

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()
var wg sync.WaitGroup
wg.Go(func() { snapLoader.Run(ctx) })        // Go 1.25 WaitGroup.Go
go func() { _ = srv.ListenAndServe() }()
<-ctx.Done()
ready.Store(false)
time.Sleep(cfg.ReadyDrain) //nolint:forbidigo // LB propagation delay; main only
sctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownWait)
defer cancel()
err := srv.Shutdown(sctx)
wg.Wait()
auditW.Close(sctx)
_ = mp.Shutdown(sctx); _ = tp.Shutdown(sctx)
rdb.Close(); pool.Close()
```

## Checklist for a new package
- [ ] Fits a layer in the table; imports only downward.
- [ ] Ports it needs are declared locally (small interfaces).
- [ ] No globals, no `init()`, constructor takes deps.
- [ ] Every goroutine it starts stops on ctx cancel and is awaited.
- [ ] Metrics/logs/spans listed in the feature doc and `docs/tech/OBSERVABILITY.md`.
- [ ] ADR if it is a new top-level package or changes this graph.
