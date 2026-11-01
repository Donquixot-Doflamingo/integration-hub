package idempotency

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// op label values of redis_op_duration_seconds owned by this package.
const (
	opBegin    = "idempotency_begin"
	opComplete = "idempotency_complete"
	opRelease  = "idempotency_release"
)

// storeMetrics holds pre-resolved series: nothing is looked up or allocated per request.
type storeMetrics struct {
	begin, complete, release prometheus.Observer

	hitMiss, hitInProgress, hitReplay, hitConflict, hitError prometheus.Counter

	completeOK, completeNotOwner, completeTooLarge, completeNotReplayable, completeError prometheus.Counter
	releaseOK, releaseMissing, releaseNotOwner, releaseError                             prometheus.Counter
}

func newStoreMetrics(m *telemetry.Metrics) storeMetrics {
	hits := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "idempotency_hits_total",
		Help: "Idempotency-Key lookups by result: miss (claimed), in_progress, replay, conflict, error (Redis unavailable).",
	}, []string{"result"})
	writes := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "idempotency_writes_total",
		Help: "Idempotency record writes by op (complete, release) and result.",
	}, []string{"op", "result"})
	m.Registry.MustRegister(hits, writes)
	return storeMetrics{
		begin:    m.RedisOpDuration.WithLabelValues(opBegin),
		complete: m.RedisOpDuration.WithLabelValues(opComplete),
		release:  m.RedisOpDuration.WithLabelValues(opRelease),

		hitMiss:       hits.WithLabelValues("miss"),
		hitInProgress: hits.WithLabelValues("in_progress"),
		hitReplay:     hits.WithLabelValues("replay"),
		hitConflict:   hits.WithLabelValues("conflict"),
		hitError:      hits.WithLabelValues("error"),

		completeOK:            writes.WithLabelValues("complete", "stored"),
		completeNotOwner:      writes.WithLabelValues("complete", "not_owner"),
		completeTooLarge:      writes.WithLabelValues("complete", "too_large"),
		completeNotReplayable: writes.WithLabelValues("complete", "not_replayable"),
		completeError:         writes.WithLabelValues("complete", "error"),
		releaseOK:             writes.WithLabelValues("release", "released"),
		releaseMissing:        writes.WithLabelValues("release", "missing"),
		releaseNotOwner:       writes.WithLabelValues("release", "not_owner"),
		releaseError:          writes.WithLabelValues("release", "error"),
	}
}

func (m *storeMetrics) hit(s State) prometheus.Counter {
	switch s {
	case Fresh:
		return m.hitMiss
	case InProgress:
		return m.hitInProgress
	case Completed:
		return m.hitReplay
	case Conflict:
		return m.hitConflict
	default:
		return m.hitError
	}
}
