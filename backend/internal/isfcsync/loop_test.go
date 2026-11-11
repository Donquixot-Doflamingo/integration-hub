package isfcsync

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

type fakeLease struct {
	held     atomic.Bool
	released atomic.Bool
}

func (l *fakeLease) Held(context.Context) bool { return l.held.Load() }
func (l *fakeLease) Release()                  { l.released.Store(true) }

// fakeElector grants leadership to one named node at a time, like the advisory lock.
type fakeElector struct {
	mu     *sync.Mutex
	leader *string
	name   string
	lease  *fakeLease
}

func (e *fakeElector) TryLead(context.Context) (Lease, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if *e.leader != "" && *e.leader != e.name {
		return nil, false, nil
	}
	*e.leader = e.name
	e.lease = &fakeLease{}
	e.lease.held.Store(true)
	return &releasing{fakeLease: e.lease, e: e}, true, nil
}

type releasing struct {
	*fakeLease
	e *fakeElector
}

func (r *releasing) Release() {
	r.fakeLease.Release()
	r.e.mu.Lock()
	defer r.e.mu.Unlock()
	if *r.e.leader == r.e.name {
		*r.e.leader = ""
	}
}

type countingRunner struct {
	mu       sync.Mutex
	triggers []string
	ran      chan struct{}
}

func newRunner() *countingRunner { return &countingRunner{ran: make(chan struct{}, 100)} }

func (r *countingRunner) Run(_ context.Context, trigger string, _ RunOptions) (Result, error) {
	r.mu.Lock()
	r.triggers = append(r.triggers, trigger)
	r.mu.Unlock()
	r.ran <- struct{}{}
	return Result{}, nil
}

func (r *countingRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.triggers)
}

func waitRun(t *testing.T, r *countingRunner) {
	t.Helper()
	select {
	case <-r.ran:
	case <-time.After(5 * time.Second):
		t.Fatal("no reconcile ran")
	}
}

func TestOnlyTheLeaderReconciles(t *testing.T) {
	var mu sync.Mutex
	var leader string
	a, b := newRunner(), newRunner()
	ea := &fakeElector{mu: &mu, leader: &leader, name: "a"}
	eb := &fakeElector{mu: &mu, leader: &leader, name: "b"}
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	la := &Loop{Runner: a, Elector: ea, Interval: 20 * time.Millisecond, Retry: 10 * time.Millisecond}
	lb := &Loop{Runner: b, Elector: eb, Interval: 20 * time.Millisecond, Retry: 10 * time.Millisecond}
	wg.Go(func() { la.Run(ctx) })
	waitRun(t, a) // a leads
	wg.Go(func() { lb.Run(ctx) })
	for range 3 {
		waitRun(t, a) // ticks keep coming on the leader
	}
	if n := b.count(); n != 0 {
		t.Fatalf("follower ran %d times", n)
	}
	// The leader loses its lease: b takes over, a stops reconciling.
	ea.lease.held.Store(false)
	waitRun(t, b)
	if !ea.lease.released.Load() {
		t.Fatal("old leader did not release")
	}
	cancel()
	wg.Wait()
}

func TestTriggersCollapseAndDuplicatesAreHarmless(t *testing.T) {
	r := newRunner()
	var mu sync.Mutex
	var leader string
	l := &Loop{Runner: r, Elector: &fakeElector{mu: &mu, leader: &leader, name: "a"}, Interval: time.Hour, Retry: time.Hour}
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Go(func() { l.Run(ctx) })
	waitRun(t, r) // the run at start
	for range 50 {
		l.Trigger() // a burst of duplicate events
	}
	waitRun(t, r)
	time.Sleep(50 * time.Millisecond)
	if n := r.count(); n > 3 {
		t.Fatalf("50 duplicate triggers caused %d runs, want them collapsed", n)
	}
	r.mu.Lock()
	got := r.triggers[1]
	r.mu.Unlock()
	if got != TriggerEvent {
		t.Fatalf("trigger label %q", got)
	}
	cancel()
	wg.Wait()
}

func TestLagAndFailureStreakFollowTheLeader(t *testing.T) {
	now := t0
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg, func() time.Time { return now })
	if gaugeValue(t, reg, "hub_isfc_sync_lag_seconds") != 0 {
		t.Fatal("a follower reports a lag")
	}
	m.SetLeader(true)
	now = now.Add(20 * time.Minute) // no success since becoming leader
	if lag := gaugeValue(t, reg, "hub_isfc_sync_lag_seconds"); lag != 1200 {
		t.Fatalf("lag %v, want 1200 (counted from becoming leader)", lag)
	}
	m.Observe(TriggerTick, OutcomeFailed)
	m.Observe(TriggerTick, OutcomeFailed)
	m.Observe(TriggerManual, OutcomeFailed)
	if s := gaugeValue(t, reg, "hub_isfc_sync_consecutive_failures"); s != 3 {
		t.Fatalf("streak %v", s)
	}
	m.Observe(TriggerEvent, OutcomeOK)
	if s, lag := gaugeValue(t, reg, "hub_isfc_sync_consecutive_failures"), gaugeValue(t, reg, "hub_isfc_sync_lag_seconds"); s != 0 || lag != 0 {
		t.Fatalf("after a success: streak %v lag %v", s, lag)
	}
	if n := testutil.ToFloat64(m.Failures.WithLabelValues(TriggerTick)); n != 2 {
		t.Fatalf("failures{tick} %v", n)
	}
	if n := testutil.ToFloat64(m.Runs.WithLabelValues(TriggerEvent, OutcomeOK)); n != 1 {
		t.Fatalf("runs{event,ok} %v", n)
	}
	m.SetLeader(false)
	now = now.Add(time.Hour)
	if lag := gaugeValue(t, reg, "hub_isfc_sync_lag_seconds"); lag != 0 {
		t.Fatalf("a former leader reports lag %v", lag)
	}
}

func gaugeValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatalf("metric %s not registered", name)
	return 0
}
