package delivery

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/sessions"
)

// SchedulerLock is the Postgres advisory lock key of the scheduler leader ("hubsched").
const SchedulerLock int64 = 0x6875627363686564

// DeadLetterRetention is how long dead letters are kept (DESIGN §10.6).
const DeadLetterRetention = 14 * 24 * time.Hour

// Scheduler is the leader-elected singleton of DESIGN §17: TTL expiry of waiting envelopes and
// pruning. Due retries need no promotion (workers claim by next_attempt_at). Leadership is a
// session advisory lock held on one dedicated connection: when the leader dies its session
// ends, the lock is released and a standby (polling every Standby) takes over.
type Scheduler struct {
	DB          *pgxpool.Pool
	Queue       *queue.Outbox
	Transitions *Transitions
	Ledger      audit.Ledger
	Log         *slog.Logger
	Now         func() time.Time
	Tick        time.Duration // expiry sweep while leading (default 1 s)
	Standby     time.Duration // leadership poll while standing by (default 5 s)
	PruneEvery  time.Duration // default 1 h
	// SessionSweepEvery deletes expired vendor sessions (default 30 s). Get already hides them;
	// the sweep keeps the table and the retire guard's count small.
	SessionSweepEvery time.Duration
	// Resilience (probes, drain) runs every Tick while leading; nil skips it.
	Resilience *Resilience

	leader prometheus.Gauge
}

// RegisterMetrics adds scheduler_leader (1 on the leader) to reg.
func (s *Scheduler) RegisterMetrics(reg prometheus.Registerer) {
	s.leader = prometheus.NewGauge(prometheus.GaugeOpts{Name: "scheduler_leader", Help: "1 while this process holds scheduler leadership."})
	reg.MustRegister(s.leader)
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// Run competes for leadership until ctx ends, and leads when it wins.
func (s *Scheduler) Run(ctx context.Context) {
	standby := orDefault(s.Standby, 5*time.Second)
	for ctx.Err() == nil {
		if err := s.lead(ctx); err != nil && ctx.Err() == nil {
			s.Log.WarnContext(ctx, "scheduler leadership attempt failed", slog.String("error", err.Error()))
		}
		t := time.NewTimer(standby)
		select {
		case <-ctx.Done():
			t.Stop()
		case <-t.C:
		}
	}
}

// lead holds the lock and runs the sweeps until ctx ends or the lock connection fails.
func (s *Scheduler) lead(ctx context.Context) error {
	conn, err := s.DB.Acquire(ctx)
	if err != nil {
		return err
	}
	var won bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, SchedulerLock).Scan(&won); err != nil || !won {
		conn.Release()
		return err
	}
	// The session must end to release the lock; never return this connection to the pool.
	defer func() { _ = conn.Hijack().Close(context.WithoutCancel(ctx)) }()
	s.setLeader(1)
	defer s.setLeader(0)
	s.Log.InfoContext(ctx, "scheduler leadership acquired")

	tick := time.NewTicker(orDefault(s.Tick, time.Second))
	defer tick.Stop()
	prune := time.NewTicker(orDefault(s.PruneEvery, time.Hour))
	defer prune.Stop()
	sweep := time.NewTicker(orDefault(s.SessionSweepEvery, 30*time.Second))
	defer sweep.Stop()
	s.prune(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if err := conn.Ping(ctx); err != nil { // lost the session, so lost the lock
				return err
			}
			s.expire(ctx)
			if s.Resilience != nil {
				s.Resilience.Tick(ctx, s.DB, orDefault(s.Tick, time.Second))
			}
		case <-prune.C:
			s.prune(ctx)
		case <-sweep.C:
			s.sweepSessions(ctx)
		}
	}
}

func (s *Scheduler) setLeader(v float64) {
	if s.leader != nil {
		s.leader.Set(v)
	}
}

// expire finishes every waiting envelope past its TTL as expired (request.expired callback).
func (s *Scheduler) expire(ctx context.Context) {
	for {
		envs, err := s.Queue.DueExpiries(ctx, s.DB, 100)
		if err != nil {
			s.Log.ErrorContext(ctx, "expiry sweep failed", slog.String("error", err.Error()))
			return
		}
		for _, env := range envs {
			if err := s.Transitions.Expire(ctx, env, "", env.Attempt); err != nil && !IsLost(err) {
				s.Log.ErrorContext(ctx, "expire failed", slog.String("request_id", env.RequestID), slog.String("error", err.Error()))
			}
		}
		if len(envs) < 100 {
			return
		}
	}
}

func (s *Scheduler) sweepSessions(ctx context.Context) {
	if n, err := sessions.Expire(ctx, s.DB, s.now()); err != nil {
		s.Log.WarnContext(ctx, "session sweep failed", slog.String("error", err.Error()))
	} else if n > 0 {
		s.Log.DebugContext(ctx, "expired vendor sessions deleted", slog.Int64("count", n))
	}
}

func (s *Scheduler) prune(ctx context.Context) {
	if n, err := s.Queue.PruneDeadLetters(ctx, s.DB, DeadLetterRetention); err != nil {
		s.Log.WarnContext(ctx, "dead-letter prune failed", slog.String("error", err.Error()))
	} else if n > 0 {
		s.Log.InfoContext(ctx, "pruned dead letters", slog.Int64("count", n))
	}
	if _, err := s.Ledger.PruneResults(ctx, s.DB, s.now()); err != nil {
		s.Log.WarnContext(ctx, "result prune failed", slog.String("error", err.Error()))
	}
}
