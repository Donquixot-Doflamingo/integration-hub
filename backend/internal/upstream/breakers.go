package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// BreakerKey names one breaker: the shared (account, endpoint) breaker when Integration is
// empty, else an integration's tightened view of it (DESIGN §10.4 overrides).
type BreakerKey struct {
	Account     string
	Endpoint    string // vendor endpoint ref, "clevertap.upload@1"
	Integration string
}

func (k BreakerKey) String() string {
	s := k.Account + "|" + k.Endpoint
	if k.Integration != "" {
		s += "|" + k.Integration
	}
	return s
}

func parseKey(s string) (BreakerKey, bool) {
	parts := strings.Split(s, "|")
	switch len(parts) {
	case 2:
		return BreakerKey{Account: parts[0], Endpoint: parts[1]}, true
	case 3:
		return BreakerKey{Account: parts[0], Endpoint: parts[1], Integration: parts[2]}, true
	}
	return BreakerKey{}, false
}

// BreakerChannel is the Redis pub/sub channel of breaker transitions (TECH_SPEC §11).
const BreakerChannel = "hub:breaker"

const breakerPrefix = "hub:breaker:"

// Breakers holds every breaker of a process. The request path reads only local state; a
// background loop publishes local transitions to Redis and adopts newer ones from other nodes
// (pub/sub plus a 1 s reconcile), so all nodes agree within about a second. Without Redis
// (nil client or an outage) each node keeps its own view and never blocks.
type Breakers struct {
	rdb redis.UniversalClient
	log *slog.Logger
	now func() time.Time

	mu      sync.RWMutex
	m       map[BreakerKey]*Breaker
	pending map[BreakerKey]BreakerSnapshot // states of breakers this node has not used yet

	out         chan published
	state       *prometheus.GaugeVec
	transitions *prometheus.CounterVec
}

type published struct {
	Key  string          `json:"key"`
	Snap BreakerSnapshot `json:"snap"`
}

// MarshalJSON keeps the wire form readable for operators (redis-cli GET hub:breaker:...).
func (s BreakerSnapshot) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		State     string    `json:"state"`
		OpenUntil time.Time `json:"open_until"`
		Reopens   int       `json:"reopens"`
		Version   int64     `json:"version"`
	}{s.State.String(), s.OpenUntil, s.Reopens, s.Version})
}

// UnmarshalJSON is the inverse of MarshalJSON.
func (s *BreakerSnapshot) UnmarshalJSON(b []byte) error {
	var w struct {
		State     string    `json:"state"`
		OpenUntil time.Time `json:"open_until"`
		Reopens   int       `json:"reopens"`
		Version   int64     `json:"version"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*s = BreakerSnapshot{State: ParseBreakerState(w.State), OpenUntil: w.OpenUntil, Reopens: w.Reopens, Version: w.Version}
	return nil
}

// NewBreakers registers breaker_state and breaker_transitions_total on m. rdb may be nil.
func NewBreakers(rdb redis.UniversalClient, m *telemetry.Metrics, log *slog.Logger) *Breakers {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	bs := &Breakers{rdb: rdb, log: log, now: time.Now, m: map[BreakerKey]*Breaker{}, pending: map[BreakerKey]BreakerSnapshot{}, out: make(chan published, 1024)}
	bs.state = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "breaker_state", Help: "Circuit state per (account, endpoint) as this node sees it: 0 closed, 1 half_open, 2 open.",
	}, []string{"vendor", "account", "endpoint"})
	bs.transitions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "breaker_transitions_total", Help: "Circuit state transitions seen by this node.",
	}, []string{"vendor", "account", "endpoint", "from", "to"})
	m.Registry.MustRegister(bs.state, bs.transitions)
	return bs
}

func endpointLabels(k BreakerKey) (vendor, endpoint string) {
	vendor, endpoint, _ = strings.Cut(k.Endpoint, ".")
	return vendor, endpoint
}

// Get returns the breaker of key, creating it with policy on first use.
func (bs *Breakers) Get(key BreakerKey, policy BreakerPolicy) *Breaker {
	bs.mu.RLock()
	b := bs.m[key]
	bs.mu.RUnlock()
	if b != nil {
		return b
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if b = bs.m[key]; b != nil {
		return b
	}
	b = NewBreaker(policy, func(from, to BreakerSnapshot, local bool) { bs.changed(key, from, to, local) })
	bs.m[key] = b
	if key.Integration == "" {
		v, ep := endpointLabels(key)
		bs.state.WithLabelValues(v, key.Account, ep).Set(0)
	}
	if snap, ok := bs.pending[key]; ok {
		delete(bs.pending, key)
		b.Adopt(snap) // before the first call sees it (the hook does not take bs.mu)
	}
	return b
}

// Lookup returns an existing breaker (admin views, the scheduler).
func (bs *Breakers) Lookup(key BreakerKey) (*Breaker, bool) {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	b, ok := bs.m[key]
	return b, ok
}

// Keys lists every known shared breaker.
func (bs *Breakers) Keys() []BreakerKey {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	out := make([]BreakerKey, 0, len(bs.m))
	for k := range bs.m {
		out = append(out, k)
	}
	return out
}

func (bs *Breakers) changed(key BreakerKey, from, to BreakerSnapshot, local bool) {
	if key.Integration == "" {
		v, ep := endpointLabels(key)
		bs.state.WithLabelValues(v, key.Account, ep).Set(float64(to.State))
		if from.State != to.State {
			bs.transitions.WithLabelValues(v, key.Account, ep, from.State.String(), to.State.String()).Inc()
		}
	}
	if from.State != to.State {
		attrs := []any{slog.String("breaker", key.String()), slog.String("from", from.State.String()), slog.String("to", to.State.String()), slog.Bool("local", local)}
		if to.State == Open {
			bs.log.Warn("breaker.opened", append(attrs, slog.Time("open_until", to.OpenUntil))...) //nolint:sloglint // transition hook has no request context
		} else {
			bs.log.Info("breaker."+to.State.String(), attrs...) //nolint:sloglint // transition hook has no request context
		}
	}
	if local && bs.rdb != nil {
		select {
		case bs.out <- published{Key: key.String(), Snap: to}:
		default: // never block the request path; the reconcile loop republishes nothing, so log
			bs.log.Warn("breaker publish queue full", slog.String("breaker", key.String())) //nolint:sloglint // see above
		}
	}
}

// Run publishes local transitions, subscribes to other nodes' and reconciles every second,
// until ctx ends. It returns at once without Redis.
func (bs *Breakers) Run(ctx context.Context) {
	if bs.rdb == nil {
		return
	}
	var wg sync.WaitGroup
	wg.Go(func() { bs.publishLoop(ctx) })
	wg.Go(func() { bs.subscribeLoop(ctx) })
	wg.Go(func() { bs.reconcileLoop(ctx) })
	wg.Wait()
}

func (bs *Breakers) publishLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-bs.out:
			body, _ := json.Marshal(p.Snap)
			msg, _ := json.Marshal(p)
			cctx, cancel := context.WithTimeout(ctx, time.Second)
			// Keep the record long enough to outlive the longest open window.
			pipe := bs.rdb.TxPipeline()
			pipe.Set(cctx, breakerPrefix+p.Key, body, 24*time.Hour)
			pipe.Publish(cctx, BreakerChannel, msg)
			_, err := pipe.Exec(cctx)
			cancel()
			if err != nil && ctx.Err() == nil {
				bs.log.WarnContext(ctx, "breaker publish failed (local state kept)", slog.String("breaker", p.Key), slog.String("error", err.Error()))
			}
		}
	}
}

func (bs *Breakers) subscribeLoop(ctx context.Context) {
	for ctx.Err() == nil {
		sub := bs.rdb.Subscribe(ctx, BreakerChannel)
		ch := sub.Channel()
	loop:
		for {
			select {
			case <-ctx.Done():
				break loop
			case msg, ok := <-ch:
				if !ok {
					break loop
				}
				var p published
				if err := json.Unmarshal([]byte(msg.Payload), &p); err == nil {
					bs.adopt(p.Key, p.Snap)
				}
			}
		}
		_ = sub.Close()
		sleep(ctx, time.Second)
	}
}

func (bs *Breakers) reconcileLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			bs.Reconcile(ctx)
		}
	}
}

// Reconcile reads the shared state of every known breaker and adopts newer ones (a missed
// pub/sub message is repaired within a second).
func (bs *Breakers) Reconcile(ctx context.Context) {
	keys := bs.Keys()
	if len(keys) == 0 || bs.rdb == nil {
		return
	}
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = breakerPrefix + k.String()
	}
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	vals, err := bs.rdb.MGet(cctx, names...).Result()
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			bs.log.DebugContext(ctx, "breaker reconcile failed (local state kept)", slog.String("error", err.Error()))
		}
		return
	}
	for i, v := range vals {
		s, ok := v.(string)
		if !ok {
			continue
		}
		var snap BreakerSnapshot
		if json.Unmarshal([]byte(s), &snap) == nil {
			bs.adopt(keys[i].String(), snap)
		}
	}
}

// SharedOpen lists every shared (account, endpoint) breaker that Redis says is open or
// half-open (the scheduler probes them, whichever node tripped them).
func (bs *Breakers) SharedOpen(ctx context.Context) (map[BreakerKey]BreakerSnapshot, error) {
	out := map[BreakerKey]BreakerSnapshot{}
	if bs.rdb == nil {
		for _, k := range bs.Keys() {
			if b, _ := bs.Lookup(k); b != nil && k.Integration == "" {
				if s := b.Snapshot(); s.State != Closed {
					out[k] = s
				}
			}
		}
		return out, nil
	}
	iter := bs.rdb.Scan(ctx, 0, breakerPrefix+"*", 200).Iterator()
	for iter.Next(ctx) {
		name := iter.Val()
		k, ok := parseKey(strings.TrimPrefix(name, breakerPrefix))
		if !ok || k.Integration != "" {
			continue
		}
		if snap, ok := bs.SharedState(ctx, k); ok && snap.State != Closed {
			out[k] = snap
		}
	}
	return out, iter.Err()
}

// SharedState reads one breaker's shared state from Redis (the scheduler's drain and probes
// act on breakers this node may never have used).
func (bs *Breakers) SharedState(ctx context.Context, key BreakerKey) (BreakerSnapshot, bool) {
	if bs.rdb == nil {
		if b, ok := bs.Lookup(key); ok {
			return b.Snapshot(), true
		}
		return BreakerSnapshot{}, false
	}
	v, err := bs.rdb.Get(ctx, breakerPrefix+key.String()).Result()
	if err != nil {
		return BreakerSnapshot{}, false
	}
	var snap BreakerSnapshot
	if json.Unmarshal([]byte(v), &snap) != nil {
		return BreakerSnapshot{}, false
	}
	return snap, true
}

func (bs *Breakers) adopt(key string, snap BreakerSnapshot) {
	k, ok := parseKey(key)
	if !ok {
		return
	}
	bs.mu.Lock()
	b := bs.m[k]
	if b == nil { // not used here yet: applied when Get creates it with the vendor's policy
		if cur, ok := bs.pending[k]; !ok || snap.Version > cur.Version {
			bs.pending[k] = snap
		}
		bs.mu.Unlock()
		return
	}
	bs.mu.Unlock()
	b.Adopt(snap)
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
