package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Admin breaker holds (DESIGN §10.4, §16.1, HUB-209): force-open, force-close and clear. A
// hold is part of the breaker's shared snapshot, so it reaches every node the way a trip does
// (Redis SET + PUBLISH hub:breaker, adopted within about a second by pub/sub or the 1 s
// reconcile). The admin role has no Breakers of its own: it writes the shared record with
// ForceShared and reads it with ReadShared / SharedStates.

// Force is an admin hold on a breaker.
type Force int8

const (
	ForceNone   Force = iota
	ForceOpen         // refuse every call until cleared or expired
	ForceClosed       // admit every call; failures are counted but never trip
)

func (f Force) String() string {
	switch f {
	case ForceOpen:
		return "open"
	case ForceClosed:
		return "closed"
	case ForceNone:
	}
	return "none"
}

// ParseForce is the inverse of String ("" and unknown values are ForceNone).
func ParseForce(s string) Force {
	switch s {
	case "open":
		return ForceOpen
	case "closed":
		return ForceClosed
	}
	return ForceNone
}

// Held reports whether an admin hold is in force at now.
func (s BreakerSnapshot) Held(now time.Time) bool {
	return s.Forced != ForceNone && (s.ForcedUntil.IsZero() || now.Before(s.ForcedUntil))
}

func (b *Breaker) forceActive(now time.Time) bool { return b.snap.Held(now) }

// expireForce ends a hold whose time passed: a forced-open breaker continues as an open
// breaker whose window has elapsed (the next call is a half-open probe), a forced-closed one
// as a plain closed breaker. Every node expires it the same way, so nothing is published.
func (b *Breaker) expireForce() {
	b.snap.Forced, b.snap.ForcedUntil = ForceNone, time.Time{}
}

// Held computes the snapshot after an admin hold (or its clearing) on prev. until is zero for
// a hold without end. The version always moves forward, so every node adopts it.
func Held(prev BreakerSnapshot, force Force, until, now time.Time) BreakerSnapshot {
	v := max(now.UnixMilli(), prev.Version+1)
	switch force {
	case ForceOpen:
		return BreakerSnapshot{State: Open, OpenUntil: until, Reopens: prev.Reopens, Version: v, Forced: ForceOpen, ForcedUntil: until}
	case ForceClosed:
		return BreakerSnapshot{State: Closed, Version: v, Forced: ForceClosed, ForcedUntil: until}
	case ForceNone:
	}
	next := BreakerSnapshot{State: prev.State, OpenUntil: prev.OpenUntil, Reopens: prev.Reopens, Version: v}
	if prev.Forced == ForceOpen { // back to automatic control: probe at once
		next.State, next.OpenUntil = HalfOpen, time.Time{}
	}
	return next
}

// Hold applies an admin hold on this node's breaker and reports the transition like any other
// (Breakers publishes it). Breakers.Force is the cluster-wide entry point.
func (b *Breaker) Hold(now time.Time, force Force, until time.Time) BreakerSnapshot {
	b.mu.Lock()
	from := b.snap
	b.snap = Held(from, force, until, now)
	b.reset()
	to := b.snap
	b.mu.Unlock()
	if b.onChange != nil {
		b.onChange(from, to, true)
	}
	return to
}

// ErrNoRedis means a shared operation was asked of a node without Redis.
var ErrNoRedis = errors.New("upstream: no Redis for shared breaker state")

// ReadShared reads one breaker's shared record (ok false when Redis has none).
func ReadShared(ctx context.Context, rdb redis.UniversalClient, key BreakerKey) (BreakerSnapshot, bool, error) {
	if rdb == nil {
		return BreakerSnapshot{}, false, ErrNoRedis
	}
	v, err := rdb.Get(ctx, breakerPrefix+key.String()).Result()
	if errors.Is(err, redis.Nil) {
		return BreakerSnapshot{}, false, nil
	}
	if err != nil {
		return BreakerSnapshot{}, false, fmt.Errorf("upstream: read breaker %s: %w", key, err)
	}
	var snap BreakerSnapshot
	if err := json.Unmarshal([]byte(v), &snap); err != nil {
		return BreakerSnapshot{}, false, fmt.Errorf("upstream: breaker %s record: %w", key, err)
	}
	return snap, true, nil
}

// SharedStates lists every shared (account, endpoint) breaker record in Redis.
func SharedStates(ctx context.Context, rdb redis.UniversalClient) (map[BreakerKey]BreakerSnapshot, error) {
	if rdb == nil {
		return nil, ErrNoRedis
	}
	out := map[BreakerKey]BreakerSnapshot{}
	iter := rdb.Scan(ctx, 0, breakerPrefix+"*", 200).Iterator()
	for iter.Next(ctx) {
		k, ok := parseKey(strings.TrimPrefix(iter.Val(), breakerPrefix))
		if !ok || k.Integration != "" {
			continue
		}
		if snap, ok, err := ReadShared(ctx, rdb, k); err == nil && ok {
			out[k] = snap
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("upstream: scan breakers: %w", err)
	}
	return out, nil
}

// ForceShared writes an admin hold (or its clearing, force = ForceNone) to the shared record
// and announces it on hub:breaker in one transaction; every node adopts it. It returns the
// previous and the new snapshot. The record of a hold without end never expires; others keep
// the usual 24 h (or the hold's end plus 24 h).
func ForceShared(ctx context.Context, rdb redis.UniversalClient, key BreakerKey, force Force, until, now time.Time) (prev, next BreakerSnapshot, err error) {
	prev, _, err = ReadShared(ctx, rdb, key)
	if err != nil {
		return prev, next, err
	}
	next = Held(prev, force, until, now)
	ttl := 24 * time.Hour
	switch {
	case force != ForceNone && until.IsZero():
		ttl = 0
	case force != ForceNone:
		ttl = until.Sub(now) + 24*time.Hour
	}
	body, _ := json.Marshal(next)
	msg, _ := json.Marshal(published{Key: key.String(), Snap: next})
	pipe := rdb.TxPipeline()
	pipe.Set(ctx, breakerPrefix+key.String(), body, ttl)
	pipe.Publish(ctx, BreakerChannel, msg)
	if _, err := pipe.Exec(ctx); err != nil {
		return prev, next, fmt.Errorf("upstream: force breaker %s: %w", key, err)
	}
	return prev, next, nil
}

// Force holds key cluster-wide from this node: the local breaker (if any) adopts the hold at
// once and the shared record is written and announced.
func (bs *Breakers) Force(ctx context.Context, key BreakerKey, force Force, until time.Time) (BreakerSnapshot, error) {
	now := bs.now()
	if bs.rdb == nil {
		b, ok := bs.Lookup(key)
		if !ok {
			return BreakerSnapshot{}, ErrNoRedis
		}
		return b.Hold(now, force, until), nil
	}
	_, next, err := ForceShared(ctx, bs.rdb, key, force, until, now)
	if err != nil {
		return next, err
	}
	bs.adopt(key.String(), next)
	return next, nil
}
