package callbacks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// SnapshotSource returns the current registry snapshot; *registry.Store implements it.
type SnapshotSource interface {
	Snapshot() *registry.Snapshot
}

// ErrNoSnapshot means no registry snapshot is loaded yet, so subscriptions are unknown. The
// caller's transaction should fail and be retried rather than drop the callback.
var ErrNoSnapshot = errors.New("callbacks: no registry snapshot loaded")

// Skip reasons: bounded label values of callback_enqueue_skipped_total.
const (
	skipUnknownVersion  = "unknown_version"
	skipUnknownEndpoint = "unknown_endpoint"
	skipTenantMismatch  = "tenant_mismatch"
)

// EnqueueOptions tune an Enqueuer. Zero values take the defaults.
type EnqueueOptions struct {
	// Validator, when set, refuses bodies that break contracts/schemas/events (an error from
	// Enqueue, so the bug surfaces instead of a malformed callback going out).
	Validator *SchemaValidator
	Log       *slog.Logger
	Now       func() time.Time
}

// Enqueuer writes callback deliveries inside the caller's transaction (TECH_SPEC §4.3: the
// owner's write joins a transaction composed by delivery/edge).
type Enqueuer struct {
	snaps    SnapshotSource
	opts     EnqueueOptions
	enqueued *prometheus.CounterVec
	skipped  *prometheus.CounterVec
}

// NewEnqueuer registers callback_enqueued_total and callback_enqueue_skipped_total on m.
func NewEnqueuer(snaps SnapshotSource, m *telemetry.Metrics, opts EnqueueOptions) (*Enqueuer, error) {
	if snaps == nil || m == nil {
		return nil, errors.New("callbacks: NewEnqueuer needs a snapshot source and metrics")
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	e := &Enqueuer{
		snaps: snaps, opts: opts,
		enqueued: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "callback_enqueued_total",
			Help: "Callback deliveries written (one per event and subscribed registrar endpoint).",
		}, []string{"event"}),
		skipped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "callback_enqueue_skipped_total",
			Help: "Subscriptions not enqueued: unknown version or endpoint, or an endpoint of another tenant.",
		}, []string{"reason"}),
	}
	for _, c := range []prometheus.Collector{e.enqueued, e.skipped} {
		if err := m.Registry.Register(c); err != nil {
			return nil, fmt.Errorf("callbacks: register metrics: %w", err)
		}
	}
	return e, nil
}

// target is one subscription matched by an event.
type target struct {
	endpoint     string
	subscription int
}

// targets returns the subscriptions of ev's integration version for ev's type whose registrar
// endpoint belongs to the request's tenant, one per endpoint.
func (e *Enqueuer) targets(ctx context.Context, snap *registry.Snapshot, ev *Event) []target {
	typ := string(ev.Data.Type())
	var v *registry.IntegrationVersion
	if in := snap.Integrations[ev.IntegrationID]; in != nil {
		v = in.Versions[ev.IntegrationVersion]
	}
	if v == nil {
		e.skip(ctx, ev, skipUnknownVersion, "")
		return nil
	}
	var out []target
	for i, w := range v.Webhooks {
		if w.Event != typ {
			continue
		}
		r := snap.Registrar[w.Endpoint]
		switch {
		case r == nil:
			e.skip(ctx, ev, skipUnknownEndpoint, w.Endpoint)
			continue
		case r.Tenant != ev.TenantID:
			// A granted tenant's result never goes to the owner's endpoint (registry validation
			// ties subscriptions to the owner tenant).
			e.skip(ctx, ev, skipTenantMismatch, w.Endpoint)
			continue
		}
		dup := false
		for _, t := range out {
			dup = dup || t.endpoint == w.Endpoint
		}
		if !dup {
			out = append(out, target{endpoint: w.Endpoint, subscription: i})
		}
	}
	return out
}

func (e *Enqueuer) skip(ctx context.Context, ev *Event, reason, endpoint string) {
	e.skipped.WithLabelValues(reason).Inc()
	e.opts.Log.WarnContext(ctx, "callback.enqueue_skipped",
		slog.String("reason", reason), slog.String("request_id", ev.RequestID),
		slog.String("integration_id", ev.IntegrationID), slog.Int("integration_version", ev.IntegrationVersion),
		slog.String("endpoint_id", endpoint), slog.String("event", string(ev.Data.Type())))
}

const insertDelivery = `
INSERT INTO hub.callback_deliveries (delivery_id, event_id, event_type, request_id, tenant_id,
    integration_id, integration_version, endpoint_id, subscription, payload)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (event_id, endpoint_id) DO NOTHING`

// Enqueue writes one pending callback_deliveries row per subscription of ev's integration
// version that matches ev's type (registry.IntegrationVersion.Webhooks), inside tx, and returns
// how many rows it wrote. Call it in the same transaction as the winning state transition
// (DESIGN §10.9): the callback then exists if and only if the transition commits, exactly once.
//
// It does no I/O other than tx and holds no lock beyond the inserted rows. An event without a
// matching subscription writes nothing and returns 0 without touching tx. ev.EventID and
// ev.OccurredAt are filled in when empty. Errors (invalid event, no snapshot, a database error)
// mean the caller should roll back.
func (e *Enqueuer) Enqueue(ctx context.Context, tx pgx.Tx, ev *Event) (int, error) {
	if ev == nil || ev.Data == nil {
		return 0, errors.New("callbacks: Enqueue needs an event with data")
	}
	snap := e.snaps.Snapshot()
	if snap == nil {
		return 0, ErrNoSnapshot
	}
	ts := e.targets(ctx, snap, ev)
	if len(ts) == 0 {
		return 0, nil
	}
	body, err := ev.Marshal(e.opts.Now())
	if err != nil {
		return 0, err
	}
	typ := ev.Data.Type()
	if e.opts.Validator != nil {
		if err := e.opts.Validator.Validate(typ, body); err != nil {
			return 0, err
		}
	}
	n := 0
	for _, t := range ts {
		tag, err := tx.Exec(ctx, insertDelivery, NewDeliveryID(), ev.EventID, string(typ), ev.RequestID, ev.TenantID,
			ev.IntegrationID, ev.IntegrationVersion, t.endpoint, t.subscription, body)
		if err != nil {
			return n, fmt.Errorf("callbacks: insert delivery for %s: %w", t.endpoint, err)
		}
		n += int(tag.RowsAffected())
	}
	e.enqueued.WithLabelValues(string(typ)).Add(float64(n))
	return n, nil
}
