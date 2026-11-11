package isfcsync

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Runner is what the loop runs (*Reconciler).
type Runner interface {
	Run(ctx context.Context, trigger string, opt RunOptions) (Result, error)
}

// Lease is leadership: Held reports whether it is still held, Release gives it up.
type Lease interface {
	Held(ctx context.Context) bool
	Release()
}

// Elector tries to become leader. ok false: someone else leads.
type Elector interface {
	TryLead(ctx context.Context) (Lease, bool, error)
}

// Loop is the worker's leader-only reconcile loop (AUTH_PLAN §6.2): it tries to lead, and while
// it leads it reconciles at start, every Interval and whenever Trigger is called (events, a Redis
// ping). Only the leader runs, so N workers make one ISFC call stream.
type Loop struct {
	Runner   Runner
	Elector  Elector
	Interval time.Duration // reconcile period (default 5 min)
	// Retry is how often a follower tries to take over, and how often the leader checks that it
	// still leads (default 10 s).
	Retry   time.Duration
	Metrics *Metrics
	Log     *slog.Logger

	trigger chan struct{}
}

// Trigger asks the leader for a reconcile soon. It never blocks, and several calls before the
// run collapse into one: duplicate events are harmless (the pull reads the full state).
func (l *Loop) Trigger() {
	l.init()
	select {
	case l.trigger <- struct{}{}:
	default:
	}
}

func (l *Loop) init() {
	if l.trigger == nil {
		l.trigger = make(chan struct{}, 1)
	}
}

// Run blocks until ctx ends.
func (l *Loop) Run(ctx context.Context) {
	l.init()
	if l.Interval <= 0 {
		l.Interval = 5 * time.Minute
	}
	if l.Retry <= 0 {
		l.Retry = 10 * time.Second
	}
	log := l.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	for ctx.Err() == nil {
		lease, ok, err := l.Elector.TryLead(ctx)
		if err != nil && ctx.Err() == nil {
			log.WarnContext(ctx, "isfc.sync leader election failed", slog.String("error", err.Error()))
		}
		if !ok {
			sleep(ctx, l.Retry)
			continue
		}
		log.InfoContext(ctx, "isfc.sync this node leads the reconcile loop")
		if l.Metrics != nil {
			l.Metrics.SetLeader(true)
		}
		l.lead(ctx, lease, log)
		if l.Metrics != nil {
			l.Metrics.SetLeader(false)
		}
		lease.Release()
		log.InfoContext(ctx, "isfc.sync this node no longer leads the reconcile loop")
		sleep(ctx, l.Retry) // let a follower take over before this node tries again
	}
}

func (l *Loop) lead(ctx context.Context, lease Lease, log *slog.Logger) {
	l.reconcile(ctx, TriggerTick, log) // at start: the state may have moved while nobody led
	tick := time.NewTicker(l.Interval)
	defer tick.Stop()
	check := time.NewTicker(l.Retry)
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-check.C:
			if !lease.Held(ctx) {
				return
			}
		case <-tick.C:
			l.reconcile(ctx, TriggerTick, log)
		case <-l.trigger:
			l.reconcile(ctx, TriggerEvent, log)
		}
	}
}

func (l *Loop) reconcile(ctx context.Context, trigger string, log *slog.Logger) {
	if _, err := l.Runner.Run(ctx, trigger, RunOptions{}); err != nil && !errors.Is(err, ErrBusy) && ctx.Err() == nil {
		log.WarnContext(ctx, "isfc.sync run failed", slog.String("trigger", trigger), slog.String("error", err.Error()))
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// PGElector elects the leader with a session-level Postgres advisory lock held on a dedicated
// connection: it is released when the leader stops or its connection dies.
type PGElector struct{ Pool *pgxpool.Pool }

type pgLease struct{ conn *pgxpool.Conn }

// TryLead implements Elector.
func (e PGElector) TryLead(ctx context.Context) (Lease, bool, error) {
	conn, ok, err := PGStore(e).TryLock(ctx, lockLeader)
	if err != nil || !ok {
		return nil, false, err
	}
	return &pgLease{conn: conn}, true, nil
}

// Held pings the lock's connection: a dead connection means the lock is gone.
func (l *pgLease) Held(ctx context.Context) bool {
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var held bool
	err := l.conn.QueryRow(pctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid()
		AND classid::bigint = ($1::bigint >> 32) AND objid::bigint = ($1::bigint & 4294967295) AND granted)`, lockLeader).Scan(&held)
	return err == nil && held
}

// Release gives the lock up.
func (l *pgLease) Release() { unlockConn(l.conn, lockLeader) }
