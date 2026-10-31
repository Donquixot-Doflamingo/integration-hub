# Pattern code sketches

Sketches: types and package names are proposals; library calls are real (Go 1.25, x/time/rate, go-redis v9, pgx v5). SQL lives in `postgres-patterns/references/queries.md`.

## Full-jitter backoff (§10.3)
```go
import "math/rand/v2"

var defaultSchedule = []time.Duration{
	time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute,
	10 * time.Minute, 30 * time.Minute, time.Hour, time.Hour,
}

// NextDelay returns the delay before retry number `failures` (0-based) and false
// when the schedule is exhausted (dead-letter). retryAfter comes from a 429.
func NextDelay(sched []time.Duration, failures int, retryAfter time.Duration) (time.Duration, bool) {
	if failures >= len(sched) {
		return 0, false
	}
	d := rand.N(sched[failures]) // full jitter: uniform in [0, sched[n])
	return max(d, retryAfter), true
}
```
The caller also checks `now+delay < expires_at`; otherwise expire instead of scheduling.

## Retry budget (≤ 20 % of first attempts, per account, per node)
```go
type window struct {
	sec          int64
	first, retry int
}

type RetryBudget struct {
	mu    sync.Mutex
	ratio float64 // 0.2
	floor int     // always allow this many retries per window, e.g. 10
	win   [10]window // 10 x 1 s
	now   func() time.Time
}

func (b *RetryBudget) bucket() *window { // b.mu held
	s := b.now().Unix()
	w := &b.win[s%int64(len(b.win))]
	if w.sec != s {
		*w = window{sec: s}
	}
	return w
}

func (b *RetryBudget) RecordFirst() {
	b.mu.Lock()
	b.bucket().first++
	b.mu.Unlock()
}

// AllowRetry reserves a retry slot if the budget allows it.
func (b *RetryBudget) AllowRetry() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.now().Unix()
	first, retry := 0, 0
	for i := range b.win {
		if w := &b.win[i]; s-w.sec < int64(len(b.win)) {
			first += w.first
			retry += w.retry
		}
	}
	if retry < b.floor || float64(retry) < b.ratio*float64(first) {
		b.bucket().retry++
		return true
	}
	return false
}
```

## CAS transition in Go
```go
var ErrLostRace = errors.New("status transition lost race")

func (s *Store) Transition(ctx context.Context, id string, from, to Status, worker string) error {
	if !canTransition(from, to) {
		return fmt.Errorf("illegal transition %s -> %s", from, to)
	}
	n, err := s.q.TransitionStatus(ctx, db.TransitionStatusParams{ // sqlc :execrows → int64
		RequestID: id, FromStatus: string(from), ToStatus: string(to), Worker: worker,
	})
	if err != nil {
		return fmt.Errorf("transition %s: %w", id, err)
	}
	if n == 0 {
		return ErrLostRace // caller does no side effects
	}
	return nil
}
```

## Breaker: local cache, off-path sync
```go
type key struct{ Account, Endpoint string }

type counters struct{ ok, fail, timeout, limited atomic.Int64 }

type Breakers struct {
	states atomic.Pointer[map[key]State] // copy-on-write, replaced by the sync loop
	counts sync.Map                      // key -> *counters
}

// State is the hot-path read: one atomic load + map lookup, no I/O.
func (b *Breakers) State(account, endpoint string) State {
	if m := b.states.Load(); m != nil {
		if st, ok := (*m)[key{account, endpoint}]; ok {
			return st
		}
	}
	return Closed
}

func (b *Breakers) Record(account, endpoint string, o Outcome) {
	v, _ := b.counts.LoadOrStore(key{account, endpoint}, &counters{})
	c := v.(*counters)
	switch o {
	case OK:
		c.ok.Add(1)
	case Timeout:
		c.timeout.Add(1)
	case RateLimited:
		c.limited.Add(1)
	default:
		c.fail.Add(1)
	}
}
```
The sync loop (every ~250 ms) `Swap(0)`s each counter, sends the deltas to a Redis Lua script that owns the window and transition logic (single writer), and receives current states. A Redis pub/sub subscriber plus a 1 s poll rebuild the `states` map and `Store` it. On Redis errors keep evaluating locally and export `breaker_sync_errors_total`.

Consecutive-timeout detection needs ordering, so the script also tracks a `consecutive_timeouts` field reset by any success.

## Shared token bucket (Redis Lua, server clock)
```go
const tokenBucketLua = `
local rate  = tonumber(ARGV[1])   -- tokens per second
local burst = tonumber(ARGV[2])
local want  = tonumber(ARGV[3])
local t     = redis.call('TIME')
local now   = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local b     = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(b[1]) or burst
local ts     = tonumber(b[2]) or now
tokens = math.min(burst, tokens + (now - ts) / 1000 * rate)
local granted = math.min(want, math.floor(tokens))
tokens = tokens - granted
redis.call('HSET', KEYS[1], 'tokens', tokens, 'ts', now)
redis.call('PEXPIRE', KEYS[1], math.ceil(burst / rate * 1000) + 1000)
return granted`

type SharedBucket struct {
	rdb    *redis.Client
	script *redis.Script // redis.NewScript(tokenBucketLua), built in the constructor
}

// Take asks for up to want tokens and returns how many were granted.
func (s *SharedBucket) Take(ctx context.Context, key string, rps, burst float64, want int) (int, error) {
	n, err := s.script.Run(ctx, s.rdb, []string{key}, rps, burst, want).Int()
	if err != nil {
		return 0, fmt.Errorf("token bucket %s: %w", key, err)
	}
	return n, nil
}
```
Lease: take a small batch (e.g. `max(1, rps/20)`) and spend it locally to avoid one Redis call per request. Key: `hub:rl:acct:{account}:{endpoint}` (braces keep it in one cluster slot).

## Local limiter (tenant fairness)
```go
lim := rate.NewLimiter(rate.Limit(cfg.RPS), cfg.Burst)
if !lim.Allow() { // edge: never wait on the sync path
	return &core.CanonicalError{Code: core.CodeUpstreamRateLimited, Message: "tenant rate limit"}
}
// worker: wait, bounded by ctx
if err := lim.Wait(ctx); err != nil { return err }
```
Keep limiters in a map keyed by (tenant, integration) rebuilt on snapshot swap; preserve existing limiter objects for unchanged keys so a swap does not reset buckets.

## Bulkhead
```go
type Bulkhead struct{ sem chan struct{} } // cap = max in flight (64)

func (b *Bulkhead) Acquire(ctx context.Context) (release func(), err error) {
	select {
	case b.sem <- struct{}{}:
		return func() { <-b.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (b *Bulkhead) Free() int { return cap(b.sem) - len(b.sem) } // worker claims at most this
```

## Ramped drain (20 % → 100 %, doubling every 30 s)
```go
func drainFraction(sinceClose time.Duration) float64 {
	steps := math.Floor(sinceClose.Seconds() / 30)
	return min(0.2*math.Pow(2, steps), 1.0) // 0.2, 0.4, 0.8, 1.0
}
```
Each scheduler tick (1 s): if the breaker is not Closed, stop. Else `limit := int(drainFraction(t) * accountRPS)` minus tokens live traffic used in the last tick (live has priority), then `queue.Release(ctx, account, endpoint, limit)`. Export the released rate so the chaos test can assert the ramp.

## Leader election with an advisory lock
```go
const schedulerLockKey int64 = 0x48554253 // "HUBS"; one constant per singleton

func RunAsLeader(ctx context.Context, dsn string, run func(ctx context.Context) error, log *slog.Logger) error {
	for ctx.Err() == nil {
		conn, err := pgx.Connect(ctx, dsn) // dedicated session, not from the pool
		if err != nil {
			log.WarnContext(ctx, "leader connect failed", slog.Any("error", err))
			if !sleepCtx(ctx, 5*time.Second) { return nil }
			continue
		}
		var got bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", schedulerLockKey).Scan(&got); err != nil || !got {
			_ = conn.Close(context.WithoutCancel(ctx))
			if !sleepCtx(ctx, 5*time.Second) { return nil }
			continue
		}
		log.InfoContext(ctx, "became scheduler leader")
		lctx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		wg.Go(func() { // lose the session = lose the lock = stop now
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-lctx.Done():
					return
				case <-t.C:
					if err := conn.Ping(lctx); err != nil {
						cancel()
						return
					}
				}
			}
		})
		err = run(lctx)
		cancel()
		wg.Wait()
		_ = conn.Close(context.WithoutCancel(ctx)) // releases the lock
		if err != nil && ctx.Err() == nil {
			log.ErrorContext(ctx, "leader jobs stopped", slog.Any("error", err))
		}
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
```
Jobs must still be idempotent and CAS-guarded: during a partition the old leader may run one more tick before noticing.

## Snapshot loader loop
```go
func (l *Loader) Run(ctx context.Context) {
	for {
		next, err := l.fetchNewer(ctx, l.store.Snapshot().Version) // long-poll, returns ctx error on shutdown
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			l.log.WarnContext(ctx, "snapshot fetch failed; keeping last good", slog.Any("error", err))
			sleepCtx(ctx, l.backoff())
			continue
		}
		compiled, err := l.compile(ctx, next) // schemas + transformers, off the request path
		if err != nil {
			l.log.ErrorContext(ctx, "snapshot compile failed; keeping last good", slog.Uint64("snapshot_version", next.Version), slog.Any("error", err))
			sleepCtx(ctx, l.backoff()) // otherwise we refetch the same bad version in a hot loop
			continue
		}
		l.store.swap(compiled)
	}
}
```
