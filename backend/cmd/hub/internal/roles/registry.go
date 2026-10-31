package roles

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// roleRegistry owns the role's snapshot store. In M1 every role builds its snapshot from the
// dev config directory; from M7 data-plane roles fetch it from the control plane (TECH_SPEC §7).
type roleRegistry struct {
	store    registry.Store
	src      registry.Sources
	cfg      Config
	log      *slog.Logger
	version  prometheus.Gauge
	failures *prometheus.CounterVec
}

func newRegistry(cfg Config, m *telemetry.Metrics, log *slog.Logger) *roleRegistry {
	r := &roleRegistry{
		src: registry.Sources{Config: os.DirFS(cfg.ConfigDir), Contracts: os.DirFS(cfg.ContractsDir)},
		cfg: cfg, log: log,
		version: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "snapshot_version", Help: "Version of the config snapshot this node serves (DESIGN §16.2).",
		}),
		failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "snapshot_apply_failures_total", Help: "Snapshots that could not be applied; the last good snapshot kept serving (OBSERVABILITY §3.3).",
		}, []string{"reason"}),
	}
	m.Registry.MustRegister(r.version, r.failures)
	return r
}

// load builds the first snapshot. A failure is logged with every problem and leaves the role
// not ready (the snapshot readiness check fails) instead of exiting, so the watcher (dev) or a
// later fetch (M7) can recover.
func (r *roleRegistry) load(ctx context.Context) {
	snap, err := registry.Build(r.src, timeNow())
	if err != nil {
		r.failed(ctx, err)
		return
	}
	r.published(ctx, r.store.Swap(snap))
}

func (r *roleRegistry) watch(ctx context.Context) {
	r.log.InfoContext(ctx, "watching config for changes", slog.String("config_dir", r.cfg.ConfigDir))
	err := registry.Watch(ctx, r.src, &r.store, registry.WatchOptions{
		OnSwap:  func(s *registry.Snapshot) { r.published(ctx, s) },
		OnError: func(err error) { r.failed(ctx, err) },
	})
	if err != nil {
		r.log.ErrorContext(ctx, "config watcher stopped", slog.String("error", err.Error()))
	}
}

func (r *roleRegistry) published(ctx context.Context, s *registry.Snapshot) {
	r.version.Set(float64(s.Version))
	r.log.InfoContext(ctx, "config snapshot published",
		slog.Uint64("snapshot_version", s.Version), slog.String("checksum", s.Checksum),
		slog.Int("integrations", len(s.Integrations)))
}

func (r *roleRegistry) failed(ctx context.Context, err error) {
	var problems registry.ConfigErrors
	if errors.As(err, &problems) {
		r.failures.WithLabelValues("invalid_config").Inc()
		for _, p := range problems {
			r.log.ErrorContext(ctx, "config problem", slog.String("file", p.File), slog.String("path", p.Path), slog.String("problem", p.Message))
		}
		return
	}
	r.failures.WithLabelValues("build_error").Inc()
	r.log.ErrorContext(ctx, "config snapshot build failed", slog.String("error", err.Error()))
}

// check is the readiness check: a snapshot must be loaded (DESIGN §19 M0 /readyz, TECH_SPEC §7).
func (r *roleRegistry) check() Check {
	return Check{Name: "snapshot", Failing: "snapshot not loaded", Fn: func(context.Context) error {
		if r.store.Snapshot() == nil {
			return errors.New("no valid config snapshot loaded")
		}
		return nil
	}}
}

// timeNow is the clock for snapshot BuiltAt.
var timeNow = time.Now
