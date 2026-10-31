---
name: go-testing
description: "How to test the Integration Hub Go backend - table-driven tests, -race, t.Parallel rules, transformer golden files with an -update flag, integration tests behind //go:build integration with testcontainers or docker compose, mockvendor, kin-openapi contract tests, fuzzing the DSL and JSON inputs, benchmarks with p99 budgets, goleak, testing/synctest for time, chaos scaffolding, naming and make targets. Use whenever you write or fix a test, add a benchmark or fuzz target, touch Makefile test targets, or need to prove an acceptance criterion from DESIGN 19."
---

# Testing the Hub backend

Non-negotiables (AGENTS.md §3): table-driven, `-race` always, never a real vendor (mockvendor only), goldens for every transformer, integration tests on real Postgres + Redis, contract tests for every handler, goleak.

## Make targets
```make
test:   ## unit, race, no external deps
	go test -race -count=1 -shuffle=on ./...
itest:  ## integration: real Postgres/Redis (testcontainers or deploy/docker-compose.yml)
	go test -race -count=1 -tags=integration -timeout=10m ./...
bench:  ## benchmarks, 10 runs for benchstat
	go test -run='^$$' -bench=. -benchmem -count=10 ./internal/... | tee bench.new.txt
```
Run one test: `go test -race -run 'TestClaim/expired_claim_is_reclaimed' ./internal/queue/postgres -tags=integration -v`.

## Naming and layout
- `TestType_Method` or `TestFunction`; subtests are lowercase sentences: `"429 maps to UPSTREAM_RATE_LIMITED"`.
- Black-box tests in `package foo_test` by default; white-box (`package foo`) only for unexported logic.
- Fixtures under `testdata/` (ignored by the go tool). Transformer goldens live with the transformer in `config/transformers/` (see `hub-transformers`).
- Helpers: `func newTestWorker(t testing.TB, opts ...) *Worker` with `t.Helper()` and `t.Cleanup`.

## Table-driven pattern
```go
func TestClassify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		status    int
		err       error
		wantCode  core.Code
		wantRetry bool
	}{
		{name: "2xx is success", status: 200},
		{name: "timeout is retryable", err: context.DeadlineExceeded, wantCode: core.CodeUpstreamTimeout, wantRetry: true},
		{name: "429 is rate limited", status: 429, wantCode: core.CodeUpstreamRateLimited, wantRetry: true},
		{name: "401 is auth failed and final", status: 401, wantCode: core.CodeUpstreamAuthFailed},
	}
	for _, tt := range tests { // Go 1.22+: tt is per-iteration, no copy needed
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := upstream.Classify(tt.status, tt.err)
			assert.Equal(t, tt.wantCode, got.Code)
			assert.Equal(t, tt.wantRetry, got.Retryable)
		})
	}
}
```
Use `require` for preconditions (stop the test), `assert` for the checks. Compare structs with `assert.Equal` or `cmp.Diff` for readable diffs.

## t.Parallel rules
- Default to `t.Parallel()` in unit tests and subtests.
- Not with `t.Setenv` or `t.Chdir` (they panic in parallel tests) or anything touching process-global state.
- Integration tests sharing one database: either give each test its own schema/database, or don't parallelise. Never depend on test order (`-shuffle=on` catches it).

## Time: testing/synctest (Go 1.25)
Backoff, TTL, breaker windows, drain ramps, tickers: run inside a bubble with a fake clock instead of sleeping.
```go
func TestDrainRamp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newTestDrainer(t)
		go d.Run(t.Context())
		time.Sleep(31 * time.Second) // fake time inside the bubble: instant
		synctest.Wait()              // until all bubble goroutines are blocked
		assert.InDelta(t, 0.4, d.Fraction(), 0.001)
	})
}
```
Real network I/O inside a bubble does not advance fake time; use in-memory fakes there. For code with an injected `Now func() time.Time`, a fake clock is also fine.

## goleak
Every package that starts goroutines has:
```go
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
```
Use `goleak.IgnoreTopFunction(...)` only for a named third-party goroutine with a comment saying why. A leak failure means a missing `cancel`, `Stop`, `Close` or an unread channel — see `go-memory-performance`.

## Golden files
Transformers (DESIGN §7.6) run all fixture cases through the engine and compare JSON semantically (unmarshal both, compare; on mismatch print a diff of pretty-printed JSON).
```go
var update = flag.Bool("update", false, "rewrite golden outputs")

func TestGoldens(t *testing.T) {
	for _, c := range goldentest.Discover(t, "../../config/transformers") {
		t.Run(c.Name, func(t *testing.T) {
			got := c.Run(t) // canonical JSON bytes, indented, keys sorted
			if *update {
				require.NoError(t, os.WriteFile(c.ExpectedPath, got, 0o644))
				return
			}
			want, err := os.ReadFile(c.ExpectedPath)
			require.NoError(t, err)
			assert.JSONEq(t, string(want), string(got))
		})
	}
}
```
`go test ./internal/transform -run TestGoldens -update`. **`-update` is for mechanical changes only**: review every changed `expected_request.json` against the vendor docs cited in the transformer before committing. `hubctl transform test` must use the same runner, so CI, publish and local agree.

## Integration tests
```go
//go:build integration

package postgres_test
```
- Real Postgres 16 and Redis 7. Prefer testcontainers-go modules (`postgres`, `redis`) started once in `TestMain`, migrated with goose, then shared; or point at `deploy/docker-compose.yml` via `HUB_TEST_POSTGRES_DSN`. Details and snippet: [references/integration-and-contract.md](references/integration-and-contract.md).
- Each test isolates its data (unique tenant/request ids, or a fresh schema).
- Never call real vendors: start `cmd/mockvendor` in-process (`httptest.Server` around its handler) or as a container, program behaviour per test, and assert on the requests it recorded.

## mockvendor usage
Program behaviour per route through its control API (latency fixed/random, status, error ratio, 429 + `Retry-After`, hard down, slow drip, malformed body; record requests; send inbound webhooks — DESIGN §19 M0). Use the client that ships with `cmd/mockvendor`; if a behaviour you need is missing, add it to mockvendor with its own test first. Reset between tests.

## Contract tests
Every handler test runs through a validator that checks request and response against `contracts/openapi/*.yaml` with kin-openapi. A response that is not in the contract fails the test. Snippet in the reference file; rules in `api-contract-backend`.

## Fuzzing
Targets: DSL parse/compile, expression evaluation with random `req` JSON, schema validation input, envelope decode, webhook signature verify.
```go
func FuzzCompileExpression(f *testing.F) {
	for _, s := range []string{`req.user.id`, `epochSeconds(req.event.timestamp ?? now())`, `len(res.body.unprocessed ?? []) == 0 ? "ok" : "partial"`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		_, _ = transform.CompileExpression(ctx, src, testEnv) // must not panic or hang
	})
}
```
`go test -run='^$' -fuzz=FuzzCompileExpression -fuzztime=60s ./internal/transform`. Commit crashers that `testdata/fuzz/` produces; they become regression tests.

## Benchmarks and budgets
Budgets from DESIGN §18.6: transform ≤ 0.5 ms p99 for 4 KB; each middleware < 100 µs p99; total overhead ≤ 5 ms p99. Go benchmarks report means, so record p99 explicitly:
```go
func BenchmarkTransform4KB(b *testing.B) {
	eng, ct, in := setupTransform4KB(b) // fixture with a 4 KB canonical body
	ctx := b.Context()
	durs := make([]time.Duration, 0, 1<<16)
	b.ReportAllocs()
	for b.Loop() { // Go 1.24+: body runs once, loop handles b.N
		start := time.Now()
		if _, err := eng.BuildRequest(ctx, ct, in); err != nil {
			b.Fatal(err)
		}
		durs = append(durs, time.Since(start))
	}
	slices.Sort(durs)
	p99 := durs[len(durs)*99/100]
	b.ReportMetric(float64(p99.Nanoseconds())/1e6, "p99-ms")
}
```
Gate budgets with a separate test behind `//go:build perf` that runs N iterations and fails when p99 > budget, run on a quiet CI runner. Compare runs with `benchstat bench.old.txt bench.new.txt` (see `go-memory-performance`).

## Chaos tests (M5 acceptance)
`//go:build integration` + a `chaos` subtest group. Scaffold: real Postgres/Redis, hub roles in-process (gateway, worker, scheduler wired as in `cmd/hub`), mockvendor in-process, breaker windows shortened via options. Script per scenario:
1. Program mockvendor (down / 5xx ratio / 429).
2. Drive traffic with `pkg/hubclient`.
3. Assert with `require.Eventually` on observable state (breaker state, `parked_count`, mockvendor request log timestamps for drain rate), never fixed sleeps.
4. Flip mockvendor back; assert recovery and the 20 %→100 % ramp within tolerance.
Cover the five M5 scenarios and the worker-kill scenario of M4 (cancel a worker's ctx mid-flight; assert one caller-visible outcome).

## Secret leak tests
Plant a canary secret (`fixture-canary-9f2c…`) in test credentials, run the e2e flow, then assert the canary appears in neither captured logs nor stored audit events (M3 acceptance). See `go-logging`.

## Checklist
- [ ] Table-driven, `t.Parallel()` where safe, `-race` green, `-shuffle=on` green.
- [ ] goleak in `TestMain` of packages with goroutines.
- [ ] No `time.Sleep` waits; `synctest` or `require.Eventually`.
- [ ] Golden/contract/fuzz/bench added where the change touches transform, handlers or hot path.
- [ ] Integration tests tagged and isolated; no real vendor anywhere.
