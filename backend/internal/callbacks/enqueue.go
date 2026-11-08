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

// ErrInvalidPayload marks an Enqueue error caused by the event's own content (it does not
// marshal or break the contract schema), as opposed to the database or the snapshot. No row was
// written and tx is untouched.
var ErrInvalidPayload = errors.New("callbacks: invalid event payload")

// Skip reasons: bounded label values of callback_enqueue_skipped_total.
const (
	skipUnknownVersion  = "unknown_version"
	skipUnknownEndpoint = "unknown_endpoint"
	skipTenantMismatch  = "tenant_mismatch"
	skipDisabled        = "endpoint_disabled"
	skipInvalidPayload  = "invalid_payload"
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
			Help: "Subscriptions not enqueued: unknown version or endpoint, an endpoint of another tenant or a disabled one, or an event whose payload is unusable (invalid_payload).",
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
		case r.Disabled: // the tenant switched the endpoint off (admin updateRegistrarEndpoint)
			e.skip(ctx, ev, skipDisabled, w.Endpoint)
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

// EnqueueTerminal is Enqueue for a request's terminal event: a problem with the event's own
// payload never fails the caller's transaction. A terminal transition that rolled back because
// of a callback would leave the request in flight, to be reclaimed and the vendor called again
// (HUB-449). The callback is then not enqueued: it is counted in callback_enqueue_skipped_total
// (reason invalid_payload) and logged by request and event type, never with the payload or the
// validator's message, which can quote it. Database and snapshot errors still fail the caller.
func (e *Enqueuer) EnqueueTerminal(ctx context.Context, tx pgx.Tx, ev *Event) (int, error) {
	n, err := e.Enqueue(ctx, tx, ev)
	if errors.Is(err, ErrInvalidPayload) {
		e.skip(ctx, ev, skipInvalidPayload, "")
		return 0, nil
	}
	return n, err
}

const insertDelivery = `
INSERT INTO hub.callback_deliveries (delivery_id, event_id, event_type, request_id, tenant_id,
    integration_id, integration_version, endpoint_id, subscription, payload, traceparent)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
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
		return 0, fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	typ := ev.Data.Type()
	if e.opts.Validator != nil {
		if err := e.opts.Validator.Validate(typ, body); err != nil {
			return 0, fmt.Errorf("%w: %w", ErrInvalidPayload, err)
		}
	}
	// The terminal transition's span (worker attempt or wait-mode edge): the delivery span links
	// back to it.
	traceparent := nullable(telemetry.Traceparent(ctx))
	n := 0
	for _, t := range ts {
		tag, err := tx.Exec(ctx, insertDelivery, NewDeliveryID(), ev.EventID, string(typ), ev.RequestID, ev.TenantID,
			ev.IntegrationID, ev.IntegrationVersion, t.endpoint, t.subscription, body, traceparent)
		if err != nil {
			return n, fmt.Errorf("callbacks: insert delivery for %s: %w", t.endpoint, err)
		}
		n += int(tag.RowsAffected())
	}
	e.enqueued.WithLabelValues(string(typ)).Add(float64(n))
	return n, nil
}
