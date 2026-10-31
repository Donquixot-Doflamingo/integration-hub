---
name: go-idioms
description: "Effective, idiomatic Go rules for the Integration Hub backend: naming, package design, error wrapping and the canonical error codes of DESIGN 4.4, context propagation, consumer-side interfaces, zero values, generics, options, immutable snapshots with atomic.Pointer, no globals, and lint/format expectations. Use whenever you write, refactor or review any Go code under backend/, add a new type, function, error or package, or are unsure how something should look in Go here."
---

# Go idioms for the Integration Hub

Go 1.25, module `github.com/Donquixot-Doflamingo/integration-hub/backend`. Code must pass `gofmt`, `goimports`, `go vet` and `golangci-lint run` (config sketch in [references/lint.md](references/lint.md)).

## Naming
- Packages: short, lower case, singular, no underscores: `edge`, `registry`, `upstream`. Never `util`, `common`, `helpers`, `models`, `types`.
- Do not stutter: `registry.Snapshot`, not `registry.RegistrySnapshot`. `queue.New`, not `queue.NewQueue` when the package has one main type.
- Initialisms keep case: `requestID`, `HTTPClient`, `TenantID`, `JSONSchema`, `TTL`.
- Receivers: one or two letters, consistent per type (`func (s *Server)`), never `this`/`self`.
- Getters without `Get`: `snap.Version()`. Interfaces with one method end in `-er` when natural (`Emitter`, `Claimer`).
- Error vars `ErrXxx`; error types `XxxError`.
- Test helpers take `t testing.TB` and call `t.Helper()`.

## Package design
- A package is a unit of behaviour, not a layer of files. Keep the exported surface small. Unexported until a second package needs it.
- No `init()` with side effects. No package-level mutable state (`var client = http.Client{}` is a global). Only constants, sentinel errors and pure lookup tables are allowed at package level.
- Constructors take dependencies explicitly: `func NewWorker(q Queue, up Upstream, log *slog.Logger, opts WorkerOptions) *Worker`.
- Return concrete types, accept interfaces.

## Errors
Rules:
1. Wrap with context using `%w` once per layer: `fmt.Errorf("claim outbox batch: %w", err)`. Message is lowercase, no trailing punctuation, no "failed to".
2. Inspect with `errors.Is` / `errors.As`. Never compare `err.Error()` strings. Never `err == ErrX` for wrapped errors.
3. Sentinel errors (`var ErrNotFound = errors.New("not found")`) for conditions callers branch on and that carry no data. Typed errors (`*CanonicalError`, `*ValidationError`) when the caller needs fields.
4. Handle an error once: either return it (wrapped) or log it and stop. Logging and returning means it is logged twice. Logging happens at the boundary (HTTP handler, worker loop) — see `go-logging`.
5. Never `panic` for expected failures. Panics are for programmer bugs; `edge` has a recover middleware that maps panics to a 500 and logs the stack.
6. `errors.Join` for collecting multiple validation errors (e.g. config validation with path-qualified errors, M1).

### Canonical errors (DESIGN §4.4)
Every error that can reach a caller ends up as one canonical code, and the mapping to HTTP + envelope happens **only in `edge`** (backend/AGENTS.md). Internal packages return wrapped errors. The two places that *decide* a code — the transform engine's `errors:` rules (vendor response → `UPSTREAM_*`) and the upstream client's timeout/connection classification — attach it as a typed `*core.CanonicalError` so the decision is not lost; everything else (`ErrNotFound`, `ErrPaused`, validation errors) stays a plain sentinel or typed error that `edge` translates. Codes, HTTP status and retryable flag live in one table in one leaf package (proposed `internal/core`; if it does not exist yet, create it and record it in an ADR since §18.1 does not list it; `docs/tech/TECH_SPEC.md` wins if it names another home).

```go
package core

type Code string

const (
	CodeValidationFailed    Code = "VALIDATION_FAILED"
	CodeUnauthorized        Code = "UNAUTHORIZED"
	CodeForbidden           Code = "FORBIDDEN"
	CodeIntegrationNotFound Code = "INTEGRATION_NOT_FOUND"
	CodeIntegrationPaused   Code = "INTEGRATION_PAUSED"
	CodeSessionNotFound     Code = "SESSION_NOT_FOUND"
	CodeTransformFailed     Code = "TRANSFORM_FAILED"
	CodeUpstreamTimeout     Code = "UPSTREAM_TIMEOUT"
	CodeUpstreamUnavailable Code = "UPSTREAM_UNAVAILABLE"
	CodeUpstreamRateLimited Code = "UPSTREAM_RATE_LIMITED"
	CodeUpstreamRejected    Code = "UPSTREAM_REJECTED"
	CodeUpstreamAuthFailed  Code = "UPSTREAM_AUTH_FAILED"
	CodeExpired             Code = "EXPIRED"
)

type codeInfo struct {
	HTTP      int
	Retryable bool
}

var codeTable = map[Code]codeInfo{ // pure lookup table: allowed at package level
	CodeValidationFailed:    {400, false},
	CodeUnauthorized:        {401, false},
	CodeForbidden:           {403, false},
	CodeIntegrationNotFound: {404, false},
	CodeIntegrationPaused:   {503, true},
	CodeSessionNotFound:     {404, false},
	CodeTransformFailed:     {500, false},
	CodeUpstreamTimeout:     {504, true},
	CodeUpstreamUnavailable: {503, true},
	CodeUpstreamRateLimited: {429, true},
	CodeUpstreamRejected:    {422, false},
	CodeUpstreamAuthFailed:  {502, false},
}

// CanonicalError is the only error type edge turns into the §4.3 envelope.
type CanonicalError struct {
	Code       Code
	Message    string        // safe for callers: no vendor/account names, no secrets
	RetryAfter time.Duration // 0 = not set
	Circuit    string        // "open" when the breaker caused it
	Upstream   *UpstreamInfo // vendor status/code, nil when no vendor call happened
	Details    []FieldError  // JSON pointers for VALIDATION_FAILED
	Err        error         // internal cause; never serialised
}

func (e *CanonicalError) Error() string   { return string(e.Code) + ": " + e.Message }
func (e *CanonicalError) Unwrap() error   { return e.Err }
func (e *CanonicalError) HTTPStatus() int { return codeTable[e.Code].HTTP }
func (e *CanonicalError) Retryable() bool { return codeTable[e.Code].Retryable }
```
Caller side:
```go
var ce *core.CanonicalError
if errors.As(err, &ce) && ce.Retryable() { /* schedule retry */ }
```
- A non-canonical error reaching `edge` is a bug: log it with stack context and return a 500. §4.4 has no generic `INTERNAL` code; if you need one, it is a contract change (see `api-contract-backend`).
- `TRANSFORM_FAILED` is produced only by the transform engine when an expression throws, never hand-picked in mapping rules.

## Context
- `ctx context.Context` is the first parameter of anything that does I/O, blocks or may be cancelled. Never store a context in a struct (exception: a per-request struct whose lifetime equals the request).
- Never pass `nil`; use `context.Background()` only in `main` and tests (`t.Context()` in tests, Go 1.24+).
- Derive deadlines, do not invent them: `ctx, cancel := context.WithTimeout(ctx, ep.Timeout); defer cancel()`. The vendor timeout is `min(endpoint timeout, X-Request-Deadline)`.
- Check `ctx.Err()` before expensive work in loops; select on `ctx.Done()` in every blocking `select`.
- Use `context.WithoutCancel(ctx)` for work that must outlive the request but keep its values (e.g. the wait-mode worker tail after the 202). Give it its own timeout.
- Context values only for request-scoped data crossing API boundaries (request id, tenant, trace). Use an unexported key type. Never pass optional parameters through ctx.

## Interfaces
- Define interfaces in the **consumer** package, with only the methods it uses. `delivery` declares `type claimer interface{ Claim(ctx context.Context, worker string, n int) ([]queue.Envelope, error) }`; it does not import a fat interface from `queue`.
- The §18.3 interfaces (`Registry`, `Resolver`, `Transformer`, `CredentialSource`, `Upstream`, `Breakers`, `AuditSink`, `Queue`) are the ports; see `go-architecture` for where each lives.
- No interface until there are two implementations or a test needs a fake. Don't create `FooInterface` + `fooImpl` pairs.
- Compile-time assertion for adapters: `var _ queue.Queue = (*postgres.Queue)(nil)`.

## Zero values and construction
- Make the zero value useful where cheap (`sync.Mutex`, `bytes.Buffer`, `strings.Builder`). Otherwise unexport fields and require the constructor.
- Options: required dependencies are positional params; optional tuning goes in an `Options` struct with documented defaults applied in the constructor:
```go
type WorkerOptions struct {
	BatchSize  int           // default 32
	Visibility time.Duration // default 30s
	Now        func() time.Time
}

func (o *WorkerOptions) withDefaults() {
	if o.BatchSize <= 0 { o.BatchSize = 32 }
	if o.Visibility <= 0 { o.Visibility = 30 * time.Second }
	if o.Now == nil { o.Now = time.Now }
}
```
Functional options (`WithX(...)`) are fine for public `pkg/hubclient` APIs; prefer a struct internally.
- Inject time (`Now func() time.Time`) and randomness instead of calling `time.Now()` / `rand` deep inside logic, so tests are deterministic. For timer-heavy code prefer `testing/synctest` (see `go-testing`).

## Immutable snapshots and atomic swap
The registry snapshot (§8.2, §18.3) is built off the request path, then published with one pointer store. Readers never lock.
```go
type Store struct{ cur atomic.Pointer[Snapshot] }

func (s *Store) Snapshot() *Snapshot     { return s.cur.Load() }
func (s *Store) swap(next *Snapshot)     { s.cur.Store(next) } // only the loader goroutine calls this
```
Rules:
- A published `*Snapshot` and everything reachable from it is **read-only**. No setters, no lazy caches inside it, no `map` writes after publish. If something must be mutable (breaker state, credentials cache), it lives outside the snapshot keyed by ID.
- Load the pointer **once per request** and pass `snap` down, so one request sees one consistent version (and records `snap.Version`).
- Build the next snapshot fully (compile schemas and transformers) before `Store`; on error keep the last good one.
- Test: concurrent readers + swapper under `-race` (M1 acceptance).

## Generics
Use them for containers and algorithms over many types: a bounded LRU, a `ring[T]` window, `slices`/`maps` helpers, a typed `sync.Pool` wrapper. Do not use them to abstract over domain behaviour — use interfaces. Prefer stdlib `slices`, `maps`, `cmp`, `min`/`max` builtins, and range-over-int / iterators (Go 1.22/1.23).
```go
type Pool[T any] struct{ p sync.Pool }

func NewPool[T any](newFn func() T) *Pool[T] {
	return &Pool[T]{p: sync.Pool{New: func() any { return newFn() }}}
}
func (p *Pool[T]) Get() T  { return p.p.Get().(T) }
func (p *Pool[T]) Put(v T) { p.p.Put(v) }
```

## Concurrency basics
- Every goroutine has an owner who can stop it and wait for it (`ctx` + `sync.WaitGroup`; Go 1.25 has `wg.Go(func(){...})`).
- Channels: the sender closes. Buffered channels are bounded by design; no unbounded queues in memory.
- Prefer `sync.Mutex` around small state over channels-as-locks. Keep critical sections free of I/O.
- Copying a struct that contains a `sync.Mutex` or `atomic.*` is a bug (`go vet` copylocks catches it).

## Misc rules
- `defer resp.Body.Close()` immediately after the error check; drain before close for connection reuse.
- `time.Duration` for durations in code; `_ms` ints only at the edges (JSON, config, metrics).
- JSON: `encoding/json` v1 (do not enable `GOEXPERIMENT=jsonv2` without an ADR). Use `json.RawMessage` to carry canonical bodies without re-encoding.
- No naked returns in functions longer than a few lines. Early returns over `else`.
- Comments on every exported identifier start with its name.

## Anti-patterns (reject in review)
- `log.Fatal` / `os.Exit` outside `main`.
- `interface{}`/`any` parameters where a concrete type is known.
- `context.TODO()` left in non-test code.
- Returning `nil, nil` for "not found"; return a sentinel.
- Mutating a snapshot, a shared slice, or a map read by other goroutines.
- `time.Sleep` for synchronisation in code or tests.
- A new `internal/*/util.go` grab bag.

## Commands
```bash
cd backend
gofmt -l . && goimports -l -local github.com/Donquixot-Doflamingo/integration-hub .
go vet ./...
golangci-lint run ./...
```
