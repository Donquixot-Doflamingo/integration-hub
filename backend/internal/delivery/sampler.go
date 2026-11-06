package delivery

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// DLQSeriesCap bounds dlq_count (OBSERVABILITY §3.2 estimates ≤ 5,000 series). Groups beyond it
// are folded into one series with every label set to telemetry.Overflow.
const DLQSeriesCap = 5_000

// queuePools and queueStatuses are the label values queue_depth always reports, so an empty
// queue reads 0 rather than a missing series.
var (
	queuePools    = []string{"messaging", "llm"}
	queueStatuses = []string{"accepted", "retry_scheduled", "in_flight"}
)

// QueueSampler publishes the outbox gauges of DESIGN §16.2 that only a database query can
// answer: queue_depth{pool,status}, queue_oldest_age_s{pool} and dlq_count{tenant,integration,
// account,reason}. Only the scheduler leader samples (Scheduler.SampleEvery, default 10 s); it
// clears the gauges when it stops leading so a former leader never reports stale values.
type QueueSampler struct {
	Queue *queue.Outbox
	Log   *slog.Logger
	Now   func() time.Time
	// DLQCap overrides DLQSeriesCap (tests).
	DLQCap int

	depth  *prometheus.GaugeVec
	oldest *prometheus.GaugeVec
	dlq    *prometheus.GaugeVec
}

// RegisterMetrics adds queue_depth, queue_oldest_age_s and dlq_count to reg.
func (q *QueueSampler) RegisterMetrics(reg prometheus.Registerer) {
	q.depth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "queue_depth",
		Help: "Outbox envelopes per pool and status (accepted, retry_scheduled, in_flight); parked ones are in parked_count.",
	}, []string{"pool", "status"})
	q.oldest = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "queue_oldest_age_s",
		Help: "Seconds the oldest due, unclaimed envelope of a pool has waited for a worker (0 when none is due).",
	}, []string{"pool"})
	q.dlq = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dlq_count",
		Help: "Dead letters not yet replayed, per tenant, integration, account and reason.",
	}, []string{"tenant", "integration", "account", "reason"})
	reg.MustRegister(q.depth, q.oldest, q.dlq)
}

func (q *QueueSampler) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

// Sample runs the three queries and replaces the gauges. A failed query leaves its gauge as it
// was and logs a warning; the next sample retries.
func (q *QueueSampler) Sample(ctx context.Context, db queue.DB) {
	if q.depth == nil {
		return
	}
	if rows, err := q.Queue.Depth(ctx, db); err != nil {
		q.Log.WarnContext(ctx, "queue depth sample failed", slog.String("error", err.Error()))
	} else {
		q.depth.Reset()
		for _, p := range queuePools {
			for _, s := range queueStatuses {
				q.depth.WithLabelValues(p, s).Set(0)
			}
		}
		for _, r := range rows {
			q.depth.WithLabelValues(r.Pool, r.Status).Set(float64(r.Count))
		}
	}

	now := q.now()
	if due, err := q.Queue.OldestDue(ctx, db, now); err != nil {
		q.Log.WarnContext(ctx, "queue age sample failed", slog.String("error", err.Error()))
	} else {
		q.oldest.Reset()
		for _, p := range queuePools {
			q.oldest.WithLabelValues(p).Set(0)
		}
		for pool, at := range due {
			q.oldest.WithLabelValues(pool).Set(max(now.Sub(at).Seconds(), 0))
		}
	}

	limit := q.DLQCap
	if limit <= 0 {
		limit = DLQSeriesCap
	}
	groups, err := q.Queue.DeadLetterCounts(ctx, db, limit)
	if err != nil {
		q.Log.WarnContext(ctx, "dlq sample failed", slog.String("error", err.Error()))
		return
	}
	shown := 0
	for _, g := range groups {
		shown += g.Count
	}
	rest := 0
	if len(groups) == limit {
		total, err := q.Queue.DeadLetterTotal(ctx, db)
		if err != nil {
			q.Log.WarnContext(ctx, "dlq total sample failed", slog.String("error", err.Error()))
			return
		}
		rest = total - shown
	}
	q.dlq.Reset()
	for _, g := range groups {
		q.dlq.WithLabelValues(g.Tenant, g.Integration, g.Account, g.Reason).Set(float64(g.Count))
	}
	if rest > 0 {
		o := telemetry.Overflow
		q.dlq.WithLabelValues(o, o, o, o).Set(float64(rest))
	}
}

// Clear drops every series (leadership lost).
func (q *QueueSampler) Clear() {
	if q.depth == nil {
		return
	}
	q.depth.Reset()
	q.oldest.Reset()
	q.dlq.Reset()
}
