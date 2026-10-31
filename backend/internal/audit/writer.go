package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// Store persists audit rows (Postgres in production, fakes in tests).
type Store interface {
	Write(ctx context.Context, b Batch) error
	// EnsurePartitions creates the daily partitions of every ledger table for [day, day+days).
	EnsurePartitions(ctx context.Context, day time.Time, days int) error
}

// WriterOptions tune the Writer. Zero values take the defaults.
type WriterOptions struct {
	Buffer        int           // channel capacity in batches (default 10 000)
	MaxRows       int           // rows per Store.Write (default 500)
	FlushEvery    time.Duration // max time a row waits in memory (default 200 ms)
	WriteTimeout  time.Duration // per Store.Write (default 5 s)
	PartitionDays int           // partitions kept ahead (default 7)
	Log           *slog.Logger
	Now           func() time.Time
}

// Writer buffers audit batches off the request path and writes them in bulk. Emit never
// blocks: when the buffer is full the batch is dropped and audit_events_dropped_total counts its
// events (DESIGN §13.4, §18.5 rule 3).
type Writer struct {
	store   Store
	opts    WriterOptions
	ch      chan Batch
	dropped *prometheus.CounterVec
	fill    prometheus.GaugeFunc
	failed  prometheus.Counter
}

// NewWriter registers the audit metrics on m and returns a writer; call Run to start it.
func NewWriter(store Store, m *telemetry.Metrics, opts WriterOptions) *Writer {
	if opts.Buffer <= 0 {
		opts.Buffer = 10_000
	}
	if opts.MaxRows <= 0 {
		opts.MaxRows = 500
	}
	if opts.FlushEvery <= 0 {
		opts.FlushEvery = 200 * time.Millisecond
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = 5 * time.Second
	}
	if opts.PartitionDays <= 0 {
		opts.PartitionDays = 7
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	w := &Writer{store: store, opts: opts, ch: make(chan Batch, opts.Buffer)}
	w.dropped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "audit_events_dropped_total",
		Help: "Audit events dropped instead of blocking the request path.",
	}, []string{"reason"})
	w.fill = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "audit_buffer_fill_ratio",
		Help: "Fill ratio of the audit write buffer (0..1).",
	}, func() float64 { return float64(len(w.ch)) / float64(cap(w.ch)) })
	w.failed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "audit_write_failures_total",
		Help: "Audit store writes that failed; their rows count in audit_events_dropped_total{reason=write_error}.",
	})
	m.Registry.MustRegister(w.dropped, w.fill, w.failed)
	return w
}

// Emit queues b without blocking.
func (w *Writer) Emit(b Batch) {
	if b.len() == 0 {
		return
	}
	select {
	case w.ch <- b:
	default:
		w.dropped.WithLabelValues("buffer_full").Add(float64(b.len()))
	}
}

// Run writes queued batches until ctx is done, then drains what is buffered (bounded by
// WriteTimeout per write). It creates the partitions for the next PartitionDays days first, and
// again every 6 hours.
func (w *Writer) Run(ctx context.Context) {
	w.ensurePartitions(ctx)
	maint := time.NewTicker(6 * time.Hour)
	defer maint.Stop()
	flush := time.NewTicker(w.opts.FlushEvery)
	defer flush.Stop()
	var pending Batch
	for {
		select {
		case <-ctx.Done():
			w.drain(ctx, pending)
			return
		case b := <-w.ch:
			pending.append(b)
			if pending.len() >= w.opts.MaxRows {
				w.write(ctx, pending)
				pending = Batch{}
			}
		case <-flush.C:
			if pending.len() > 0 {
				w.write(ctx, pending)
				pending = Batch{}
			}
		case <-maint.C:
			w.ensurePartitions(ctx)
		}
	}
}

// drain writes what is still buffered; write detaches from the cancelled ctx.
func (w *Writer) drain(ctx context.Context, pending Batch) {
	for {
		select {
		case b := <-w.ch:
			pending.append(b)
		default:
			if pending.len() > 0 {
				w.write(ctx, pending)
			}
			return
		}
	}
}

func (w *Writer) write(ctx context.Context, b Batch) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.opts.WriteTimeout)
	defer cancel()
	if err := w.store.Write(ctx, b); err != nil {
		w.failed.Inc()
		w.dropped.WithLabelValues("write_error").Add(float64(b.len()))
		w.opts.Log.ErrorContext(ctx, "audit write failed", slog.Int("events", len(b.Events)),
			slog.Int("records", len(b.Records)), slog.String("error", err.Error()))
	}
}

func (w *Writer) ensurePartitions(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, w.opts.WriteTimeout)
	defer cancel()
	day := w.opts.Now().UTC().AddDate(0, 0, -1) // yesterday too: requests started before midnight
	if err := w.store.EnsurePartitions(ctx, day, w.opts.PartitionDays+1); err != nil {
		w.opts.Log.ErrorContext(ctx, "audit partition maintenance failed", slog.String("error", err.Error()))
	}
}
