package roles

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// timeNow is the clock for snapshot BuiltAt.
var timeNow = time.Now

// Bounds for archive calls at boot and in background jobs, so a down database never blocks a role.
const (
	archiveTimeout = 3 * time.Second
	pruneEvery     = time.Hour
)

// followEvery is the archive poll backing up the hub:snapshot ping (a var: tests shorten it).
var followEvery = 5 * time.Second

// roleRegistry owns the role's snapshot store. In M1 every role builds its snapshot from the dev
// config directory; the admin role archives each new snapshot (hub.config_snapshots + blob) and
// every role keeps a local copy. When the config cannot be built, data-plane roles boot from the
// archive, then from the local copy (DESIGN §17: nodes boot from the last snapshot when the
// control plane is down). From M7 nodes fetch snapshots from the control plane (TECH_SPEC §7).
type roleRegistry struct {
	store    registry.Store
	src      registry.Sources
	cfg      Config
	log      *slog.Logger
	archive  *control.Archive
	version  prometheus.Gauge
	failures *prometheus.CounterVec
	origin   *prometheus.GaugeVec
	// beforeSwap and afterSwap let the role prepare for a snapshot (the gateway warms its
	// credential cache) and react to one (prune upstream pools). Both may be nil.
	beforeSwap func(context.Context, *registry.Snapshot)
	afterSwap  func(*registry.Snapshot)
	// archiveSeen is the newest archive version this node has considered (follow).
	archiveSeen atomic.Int64
	rdb         *redis.Client
}

func newRegistry(cfg Config, m *telemetry.Metrics, log *slog.Logger, pool *pgxpool.Pool, rdb *redis.Client) (*roleRegistry, error) {
	blobs, err := control.OpenBlobStore(cfg.BlobURL)
	if err != nil {
		return nil, err
	}
	r := &roleRegistry{
		src: registry.Sources{Config: os.DirFS(cfg.ConfigDir), Contracts: os.DirFS(cfg.ContractsDir)},
		cfg: cfg, log: log, archive: control.NewArchive(pool, blobs), rdb: rdb,
		version: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "snapshot_version", Help: "Archive version (hub.config_snapshots) of the config snapshot this node serves; 0 until the node knows its content is archived (DESIGN §16.2, HUB-401).",
		}),
		failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "snapshot_apply_failures_total", Help: "Snapshots that could not be applied; the last good snapshot kept serving (OBSERVABILITY §3.3).",
		}, []string{"reason"}),
		origin: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "snapshot_origin", Help: "1 for where the serving snapshot came from: config, archive or cache.",
		}, []string{"origin"}),
	}
	m.Registry.MustRegister(r.version, r.failures, r.origin)
	return r, nil
}

func (r *roleRegistry) cachePath() string { return filepath.Join(r.cfg.DataDir, "snapshot.json.gz") }

// load builds the first snapshot. A failure is logged with every problem; data-plane roles then
// fall back to the archive and the local cache. With no snapshot at all the role stays not ready
// (readiness check) instead of exiting, so the watcher or a later fetch can recover.
func (r *roleRegistry) load(ctx context.Context) {
	snap, err := registry.Build(r.src, timeNow())
	if err == nil {
		r.adopt(ctx, r.src, snap, "config")
		return
	}
	r.failed(ctx, err)
	if r.cfg.Role == Admin {
		return // the control plane is the source of truth; it never boots from a copy
	}
	actx, cancel := context.WithTimeout(ctx, archiveTimeout)
	defer cancel()
	if snap, src, v, err := r.archive.Latest(actx, timeNow()); err == nil {
		r.log.WarnContext(ctx, "booted from the archived snapshot", slog.Int64("archive_version", v))
		snap.Version = archiveVersion(v)
		r.adopt(ctx, src, snap, "archive")
		return
	} else if !errors.Is(err, control.ErrNoSnapshot) {
		r.failures.WithLabelValues("archive_error").Inc()
		r.log.WarnContext(ctx, "archived snapshot unavailable", slog.String("error", err.Error()))
	}
	data, err := os.ReadFile(r.cachePath())
	if err != nil {
		r.log.WarnContext(ctx, "no local snapshot cache", slog.String("path", r.cachePath()))
		return
	}
	snap, src, err := registry.Unpack(data, "", timeNow())
	if err != nil {
		r.failures.WithLabelValues("cache_corrupt").Inc()
		r.log.ErrorContext(ctx, "local snapshot cache refused", slog.String("error", err.Error()))
		return
	}
	r.log.WarnContext(ctx, "booted from the local snapshot cache", slog.String("path", r.cachePath()))
	r.adopt(ctx, src, snap, "cache")
}

// adopt publishes snap locally, refreshes the local cache and, on the admin role, archives it.
func (r *roleRegistry) adopt(ctx context.Context, src registry.Sources, snap *registry.Snapshot, origin string) {
	if r.beforeSwap != nil {
		r.beforeSwap(ctx, snap)
	}
	pub := r.store.Swap(snap)
	r.published(ctx, pub)
	if r.afterSwap != nil {
		r.afterSwap(pub)
	}
	for _, o := range []string{"config", "archive", "cache"} {
		v := 0.0
		if o == origin {
			v = 1
		}
		r.origin.WithLabelValues(o).Set(v)
	}
	r.writeCache(ctx, src, snap)
	if r.cfg.Role == Admin && origin == "config" && r.mayArchiveConfig(ctx) {
		actx, cancel := context.WithTimeout(ctx, archiveTimeout)
		defer cancel()
		v, published, err := r.archive.Publish(actx, src, snap, control.ConfigAuthorPrefix+r.cfg.ConfigDir, "config directory changed")
		switch {
		case err != nil:
			r.failures.WithLabelValues("archive_error").Inc()
			r.log.ErrorContext(ctx, "snapshot not archived", slog.String("error", err.Error()))
		default:
			if published {
				r.log.InfoContext(ctx, "snapshot archived", slog.Int64("archive_version", v))
			}
			r.label(ctx, snap.Checksum, v)
		}
	}
}

// archiveVersion converts an archive version (a positive BIGSERIAL-like number) for Snapshot.
func archiveVersion(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

// label marks the served snapshot as archive version v when it has checksum (HUB-401): the
// node built or cached the same content its peers serve as v, so it reports v too.
func (r *roleRegistry) label(ctx context.Context, checksum string, v int64) {
	if pub, changed := r.store.Label(checksum, archiveVersion(v)); changed {
		r.published(ctx, pub)
	}
}

func (r *roleRegistry) writeCache(ctx context.Context, src registry.Sources, snap *registry.Snapshot) {
	data, err := registry.Pack(src, snap)
	if err == nil {
		err = writeFileAtomic(r.cachePath(), data)
	}
	if err != nil {
		r.log.WarnContext(ctx, "snapshot cache not written", slog.String("error", err.Error()))
	}
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".snapshot-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (r *roleRegistry) watch(ctx context.Context) {
	r.log.InfoContext(ctx, "watching config for changes", slog.String("config_dir", r.cfg.ConfigDir))
	err := registry.Watch(ctx, r.src, &r.store, registry.WatchOptions{
		BeforeSwap: func(s *registry.Snapshot) {
			if r.beforeSwap != nil {
				r.beforeSwap(ctx, s)
			}
		},
		OnSwap: func(s *registry.Snapshot) {
			r.published(ctx, s)
			if r.afterSwap != nil {
				r.afterSwap(s)
			}
			r.writeCache(ctx, r.src, s)
			if r.cfg.Role == Admin && r.mayArchiveConfig(ctx) {
				actx, cancel := context.WithTimeout(ctx, archiveTimeout)
				defer cancel()
				if v, _, err := r.archive.Publish(actx, r.src, s, control.ConfigAuthorPrefix+r.cfg.ConfigDir, "config directory changed"); err != nil {
					r.failures.WithLabelValues("archive_error").Inc()
					r.log.ErrorContext(ctx, "snapshot not archived", slog.String("error", err.Error()))
				} else {
					r.label(ctx, s.Checksum, v)
				}
			}
		},
		OnError: func(err error) { r.failed(ctx, err) },
	})
	if err != nil {
		r.log.ErrorContext(ctx, "config watcher stopped", slog.String("error", err.Error()))
	}
}

// mayArchiveConfig: the config directory is a bootstrap (ADR-0010). It is archived while the
// archive is empty or holds only config-directory snapshots; once a published change is
// archived, archiving the directory would undo it.
func (r *roleRegistry) mayArchiveConfig(ctx context.Context) bool {
	actx, cancel := context.WithTimeout(ctx, archiveTimeout)
	defer cancel()
	meta, err := r.archive.LatestMeta(actx)
	if errors.Is(err, control.ErrNoSnapshot) {
		return true
	}
	if err != nil {
		return false
	}
	if !strings.HasPrefix(meta.Author, control.ConfigAuthorPrefix) {
		r.log.InfoContext(ctx, "archive holds published changes: config directory not archived (ADR-0010)", slog.Int64("archive_version", meta.Version))
		return false
	}
	return true
}

// follow keeps the node on the newest archived snapshot (DESIGN §6.4: nodes swap within 10 s of
// a publish): it checks on every hub:snapshot ping and every followEvery, adopts an archive
// version newer than the last one it considered when its checksum differs from what it serves,
// and keeps the last good snapshot when the archive cannot be read.
func (r *roleRegistry) follow(ctx context.Context) {
	ping := make(<-chan *redis.Message)
	if r.rdb != nil {
		sub := r.rdb.Subscribe(ctx, control.SnapshotChannel)
		defer func() { _ = sub.Close() }()
		ping = sub.Channel()
	}
	t := time.NewTicker(followEvery)
	defer t.Stop()
	r.syncArchive(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping:
		case <-t.C:
		}
		r.syncArchive(ctx)
	}
}

func (r *roleRegistry) syncArchive(ctx context.Context) {
	actx, cancel := context.WithTimeout(ctx, archiveTimeout)
	defer cancel()
	meta, err := r.archive.LatestMeta(actx)
	if err != nil {
		if !errors.Is(err, control.ErrNoSnapshot) && ctx.Err() == nil {
			r.log.DebugContext(ctx, "archive unreachable: keeping the current snapshot", slog.String("error", err.Error()))
		}
		return
	}
	if meta.Version <= r.archiveSeen.Load() {
		return
	}
	if cur := r.store.Snapshot(); cur != nil && cur.Checksum == meta.Checksum {
		r.archiveSeen.Store(meta.Version)
		r.label(ctx, meta.Checksum, meta.Version) // same content: report the archive's version
		return
	}
	snap, src, v, err := r.archive.Latest(actx, timeNow())
	if err != nil {
		r.failures.WithLabelValues("fetch_error").Inc()
		r.log.WarnContext(ctx, "archived snapshot not loaded: keeping the current one", slog.String("error", err.Error()))
		return
	}
	r.archiveSeen.Store(v)
	r.log.InfoContext(ctx, "snapshot.applied from the archive", slog.Int64("archive_version", v), slog.String("checksum", snap.Checksum))
	snap.Version = archiveVersion(v)
	r.adopt(ctx, src, snap, "archive")
}

// pruneLoop deletes archived snapshots older than 90 days, at start and then hourly (scheduler).
func (r *roleRegistry) pruneLoop(ctx context.Context) {
	t := time.NewTicker(pruneEvery)
	defer t.Stop()
	for {
		actx, cancel := context.WithTimeout(ctx, archiveTimeout)
		n, err := r.archive.Prune(actx, timeNow().Add(-control.SnapshotRetention))
		cancel()
		switch {
		case err != nil && ctx.Err() == nil:
			r.log.WarnContext(ctx, "snapshot prune failed", slog.String("error", err.Error()))
		case n > 0:
			r.log.InfoContext(ctx, "pruned archived snapshots", slog.Int("count", n))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
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
