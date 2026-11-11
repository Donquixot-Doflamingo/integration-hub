package isfcsync

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are the sync signals of AUTH_PLAN §11 / OBSERVABILITY §15. Only the leader's lag and
// failure streak are meaningful: a follower reports 0 for both.
type Metrics struct {
	Runs     *prometheus.CounterVec // hub_isfc_sync_runs_total{trigger,result}
	Failures *prometheus.CounterVec // hub_isfc_sync_failures_total{trigger}
	// CallerKeys is hub_isfc_caller_keys: keys in the Hub's caller-key table by status.
	CallerKeys *prometheus.GaugeVec

	mu          sync.Mutex
	now         func() time.Time
	leader      bool
	since       time.Time // when this process became leader (baseline for the lag before a first success)
	lastSuccess time.Time
	streak      int
}

// NewMetrics creates and registers the sync metrics on reg (nil: not registered, for tests).
func NewMetrics(reg prometheus.Registerer, now func() time.Time) *Metrics {
	if now == nil {
		now = time.Now
	}
	m := &Metrics{now: now,
		Runs: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "hub_isfc_sync_runs_total",
			Help: "ISFC reconcile runs by trigger (event, tick, manual) and result (ok, partial, failed) (AUTH_PLAN §11)."}, []string{"trigger", "result"}),
		Failures: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "hub_isfc_sync_failures_total",
			Help: "ISFC reconcile runs that failed (ISFC unreachable or refused); the last good state is kept."}, []string{"trigger"}),
		CallerKeys: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "hub_isfc_caller_keys",
			Help: "ISFC caller keys known to this node by status (ACTIVE keys authenticate at the edge)."}, []string{"status"}),
	}
	lag := prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "hub_isfc_sync_lag_seconds",
		Help: "Seconds since the last successful ISFC reconcile on the leader (since it became leader before a first success); 0 on followers."}, m.lagSeconds)
	failing := prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "hub_isfc_sync_consecutive_failures",
		Help: "Failed ISFC reconcile runs in a row on the leader; 0 on followers."}, m.failureStreak)
	if reg != nil {
		reg.MustRegister(m.Runs, m.Failures, m.CallerKeys, lag, failing)
	}
	return m
}

// SetLeader records leadership. Becoming leader starts the lag baseline.
func (m *Metrics) SetLeader(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if on && !m.leader {
		m.since, m.streak = m.now(), 0
	}
	m.leader = on
}

// Observe counts one run. A failed run extends the streak; ok and partial runs end it and move
// the success time (a partial run still reached ISFC).
func (m *Metrics) Observe(trigger, result string) {
	m.Runs.WithLabelValues(trigger, result).Inc()
	m.mu.Lock()
	defer m.mu.Unlock()
	if result == "failed" {
		m.Failures.WithLabelValues(trigger).Inc()
		m.streak++
		return
	}
	m.streak, m.lastSuccess = 0, m.now()
}

func (m *Metrics) lagSeconds() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.leader {
		return 0
	}
	base := m.since
	if m.lastSuccess.After(base) {
		base = m.lastSuccess
	}
	return m.now().Sub(base).Seconds()
}

func (m *Metrics) failureStreak() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.leader {
		return 0
	}
	return float64(m.streak)
}
