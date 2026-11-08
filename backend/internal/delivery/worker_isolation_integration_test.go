//go:build integration

package delivery

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
)

// Worker isolation (HUB-412). int_evt_brandA delivers to account
// ct-brandA-in, int_evt_brandB to ct-brandB-us, both on clevertap.upload@1.

const endpointRef = "clevertap.upload@1"

var targetA = queue.Target{Account: "ct-brandA-in", Endpoint: endpointRef}

// accountLoader gives each account its own account_id credential, so a mockvendor route can
// match one account by its X-CleverTap-Account-Id header.
type accountLoader struct{}

func (accountLoader) Load(_ context.Context, ids []string) (map[string]creds.AccountSecrets, error) {
	out := map[string]creds.AccountSecrets{}
	for _, id := range ids {
		out[id] = creds.AccountSecrets{Active: creds.Slot{Version: 1, Values: map[string]string{"account_id": "acct-" + id, "passcode": "fake-pass-ISO1"}}}
	}
	return out, nil
}

// perAccount switches the rig to per-account credentials and programs each account's vendor.
func (r *rig) perAccount(a, b mockvendor.Behaviour) {
	r.t.Helper()
	cache, err := creds.NewCache(accountLoader{}, telemetry.NewSecretSet(), telemetry.NewMetrics("worker", "iso"), creds.CacheOptions{})
	if err != nil {
		r.t.Fatal(err)
	}
	if err := cache.Warm(r.t.Context(), creds.AccountIDs(r.snaps.Snapshot())); err != nil {
		r.t.Fatal(err)
	}
	r.w.Attempter.Creds = cache
	r.mv.Reset(r.t)
	for id, beh := range map[string]mockvendor.Behaviour{"ct-brandA-in": a, "ct-brandB-us": b} {
		r.mv.SetRoute(r.t, id, mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload",
			Headers: map[string]string{"X-Clevertap-Account-Id": "acct-" + id}}, Behaviour: beh})
	}
}

func slow(ms int) mockvendor.Behaviour {
	return mockvendor.Behaviour{Status: 200, Body: okBody, Latency: &mockvendor.Latency{Mode: "fixed", FixedMS: ms}}
}

// callsTo counts the vendor requests of one account.
func (r *rig) callsTo(account string) int {
	n := 0
	for _, rq := range r.mv.Requests(r.t, nil) {
		if v := rq.Headers["X-Clevertap-Account-Id"]; len(v) == 1 && v[0] == "acct-"+account {
			n++
		}
	}
	return n
}

// TestWorkerTargetCap is HUB-412: with a target cap of 1, the second envelope for the same
// (account, endpoint) is put back unattempted (attempt not counted, no vendor call, the target
// kept as its hint and counted in worker_target_cap_deferred_total), while another account's
// envelope in the same claim is delivered. While the target stays full the put-back envelope is
// not claimed again; once the slot frees it is delivered as a first attempt.
func TestWorkerTargetCap(t *testing.T) {
	r := newRig(t)
	r.perAccount(slow(300), slow(0))
	r.w.Concurrency, r.w.TargetCap = 4, 1
	reg := prometheus.NewRegistry()
	r.w.RegisterMetrics(reg)
	a1 := r.enqueue("int_evt_brandA", 6*time.Hour)
	a2 := r.enqueue("int_evt_brandA", 6*time.Hour)
	b := r.enqueue("int_evt_brandB", 6*time.Hour)
	if n := r.process(); n != 3 {
		t.Fatalf("claimed %d, want 3", n)
	}
	sa1, _ := r.status(a1.RequestID)
	sa2, _ := r.status(a2.RequestID)
	if sb, _ := r.status(b.RequestID); sb != "succeeded" {
		t.Fatalf("the other account's envelope is %s, want succeeded", sb)
	}
	deferred := a2
	if sa1 != "succeeded" {
		deferred = a1
		sa1, sa2 = sa2, sa1
	}
	if sa1 != "succeeded" || sa2 != "accepted" {
		t.Fatalf("A envelopes: %s and %s, want one succeeded and one put back", sa1, sa2)
	}
	if status, attempt, crashes, account := r.outboxRow(deferred.RequestID); status != "accepted" || attempt != 0 || crashes != 0 || account != targetA.Account {
		t.Fatalf("put-back envelope: %s attempt %d crashes %d hint %q", status, attempt, crashes, account)
	}
	if n := r.callsTo("ct-brandA-in"); n != 1 {
		t.Fatalf("A's vendor got %d calls, want 1 (the capped envelope must not be sent)", n)
	}
	if got := testutil.ToFloat64(r.w.metrics.capDeferred.WithLabelValues(targetA.Account, targetA.Endpoint)); got != 1 {
		t.Fatalf("worker_target_cap_deferred_total = %v, want 1", got)
	}

	// The target is full again (another attempt in flight): the backlog is skipped, not churned.
	r.advance(2 * time.Second)
	if !r.w.acquire(targetA) {
		t.Fatal("acquire")
	}
	if n := r.process(); n != 0 {
		t.Fatalf("claimed %d while the target was full, want 0", n)
	}
	r.w.release(targetA)
	if n := r.process(); n != 1 {
		t.Fatalf("claimed %d once the target freed, want 1", n)
	}
	if s, attempts := r.status(deferred.RequestID); s != "succeeded" || attempts != 1 {
		t.Fatalf("put-back envelope ended %s after %d attempts, want succeeded on its first", s, attempts)
	}
	if got := r.w.InFlight(targetA.Account, targetA.Endpoint); got != 0 {
		t.Fatalf("%d still in flight", got)
	}
}

// TestClaimDoesNotRetakeReservedSlots is HUB-444: the claim loop claims again as soon as a worker
// slot frees, before the previous claim's goroutines have taken their target slots. Each claimed
// envelope therefore reserves its target's slot from the claim on: a second claim right after the
// first takes nothing more of a full target. Without the reservation the second claim saw the
// same free room and took another cap's worth, which went straight back to the queue as
// "target at cap" and waited for a later claim (seen in CI as a healthy account's queue wait
// growing while another account refused connections).
func TestClaimDoesNotRetakeReservedSlots(t *testing.T) {
	r := newRig(t)
	r.w.Concurrency, r.w.TargetCap = 16, 2
	for range 5 { // with the hint the gateway writes at accept
		r.enqueueWith("int_evt_brandA", 6*time.Hour, func(e *core.Envelope) {
			e.TargetAccount, e.TargetEndpoint = targetA.Account, targetA.Endpoint
		}, false)
	}
	first, err := r.w.claim(t.Context(), 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("first claim took %d of A, want its cap 2", len(first))
	}
	for _, c := range first {
		if c.Target == nil || *c.Target != targetA {
			t.Fatalf("claimed envelope target hint %v, want %v", c.Target, targetA)
		}
	}
	r.w.reserve(first) // what Run does before starting the attempts
	second, err := r.w.claim(t.Context(), 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second claim before any attempt started took %d more of A, want 0", len(second))
	}
	// One claimed envelope ends without an attempt: its slot is free again for the next claim.
	r.w.unreserver(first[0])()
	third, err := r.w.claim(t.Context(), 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(third) != 1 {
		t.Fatalf("claim after one reservation ended took %d, want 1", len(third))
	}
}

// TestWorkerClaimsContinuously is HUB-412 (and HUB-407's claim model): while a slow attempt for
// account A is in flight, an envelope for account B accepted afterwards is claimed and delivered
// at once. The old loop waited for its whole batch before claiming again, so B finished after A.
func TestWorkerClaimsContinuously(t *testing.T) {
	r := newRig(t)
	r.perAccount(slow(1500), slow(0))
	r.w.Idle = 10 * time.Millisecond
	a := r.enqueue("int_evt_brandA", 6*time.Hour)
	r.start()
	waitFor(t, "A's attempt to reach its vendor", 2*time.Second, func() bool { return r.callsTo("ct-brandA-in") == 1 })
	b := r.enqueue("int_evt_brandB", 6*time.Hour)
	start := time.Now()
	waitFor(t, "B to be delivered", 3*time.Second, func() bool { s, _ := r.status(b.RequestID); return s == "succeeded" })
	took := time.Since(start)
	if s, _ := r.status(a.RequestID); s == "succeeded" {
		t.Fatalf("A finished before B (B took %s): B waited for A", took)
	}
	if took > 700*time.Millisecond {
		t.Fatalf("B took %s while A was in flight, want well under A's 1.5 s", took)
	}
	t.Logf("B delivered %s after it was accepted, while A's 1.5 s attempt was in flight", took.Round(time.Millisecond))
}

// TestWorkerShutdownFinishesInFlight: cancelling Run's context stops claiming, and Run returns
// only after the attempts in flight have settled (no claim is left to expire).
func TestWorkerShutdownFinishesInFlight(t *testing.T) {
	r := newRig(t)
	r.perAccount(slow(500), slow(0))
	r.w.Idle = 10 * time.Millisecond
	a := r.enqueue("int_evt_brandA", 6*time.Hour)
	stop := r.start()
	waitFor(t, "A's attempt to reach its vendor", 2*time.Second, func() bool { return r.callsTo("ct-brandA-in") == 1 })
	late := r.enqueue("int_evt_brandB", 6*time.Hour)
	stop()
	if s, attempts := r.status(a.RequestID); s != "succeeded" || attempts != 1 {
		t.Fatalf("in-flight envelope ended %s after %d attempts, want succeeded before Run returned", s, attempts)
	}
	_ = late // claimed or not, never left in flight:
	var inFlight int
	if err := r.db.QueryRow(t.Context(), `SELECT count(*) FROM hub.outbox WHERE status = 'in_flight' AND claimed_by = $1`, r.w.Name).Scan(&inFlight); err != nil {
		t.Fatal(err)
	}
	if inFlight != 0 {
		t.Fatalf("%d claims left in flight after shutdown", inFlight)
	}
}
