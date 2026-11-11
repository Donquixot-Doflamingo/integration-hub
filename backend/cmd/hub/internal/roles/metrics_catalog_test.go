package roles

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/callbacks"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/idempotency"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfcsync"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// designMetrics is the DESIGN §16.2 list, verbatim. HUB-230: every one must be exported.
var designMetrics = []string{
	"hub_requests_total", "hub_overhead_ms", "vendor_latency_ms", "breaker_state", "breaker_transitions_total",
	"queue_depth", "queue_oldest_age_s", "parked_count", "parked_oldest_age_s", "dlq_count",
	"ratelimit_rejections_total", "retry_budget_exhausted_total", "snapshot_version", "credential_expiry_days",
	"llm_tokens_total", "llm_cost_usd_total", "audit_events_dropped_total",
}

type noLoader struct{}

func (noLoader) Load(context.Context, []string) (map[string]creds.AccountSecrets, error) {
	return map[string]creds.AccountSecrets{}, nil
}

type noStore struct{}

func (noStore) Write(context.Context, audit.Batch) error { return nil }

func (noStore) EnsurePartitions(context.Context, time.Time, int) error { return nil }

// TestDesignMetricsAreRegistered builds every metric owner the data plane and scheduler wire
// (dataplane.go, registry.go) on one registry and checks the registry describes each §16.2 name.
// Describe lists vector metrics before their first observation, so nothing has to be emitted.
func TestDesignMetricsAreRegistered(t *testing.T) {
	got := describedNames(catalogRegistry(t))
	for _, name := range designMetrics {
		if !slices.Contains(got, name) {
			t.Errorf("DESIGN §16.2 metric %q is not registered", name)
		}
	}
}

// catalogRegistry registers every metric owner the roles wire (dataplane.go, registry.go) on one
// registry, the way the data plane, scheduler and callback dispatcher do, without starting
// anything. It is the source of truth for the names alert rules and dashboards may use.
func catalogRegistry(t *testing.T) *prometheus.Registry {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	m := telemetry.NewMetrics("test", "dev")

	if _, err := upstream.New(m, upstream.Options{}); err != nil {
		t.Fatal(err)
	}
	upstream.NewBreakers(nil, m, log)
	upstream.NewAccountLimiter(nil, m, log)
	delivery.NewBudget(m.Registry)
	(&delivery.Resilience{}).RegisterMetrics(m.Registry)
	(&delivery.QueueSampler{}).RegisterMetrics(m.Registry)
	(&delivery.Scheduler{}).RegisterMetrics(m.Registry)
	(&delivery.Worker{}).RegisterMetrics(m.Registry)
	delivery.NewShadower(nil, nil, log, m.Registry)
	audit.NewWriter(noStore{}, m, audit.WriterOptions{})
	if _, err := creds.NewCache(noLoader{}, telemetry.NewSecretSet(), m, creds.CacheOptions{}); err != nil {
		t.Fatal(err)
	}
	idempotency.New(nil, m, idempotency.Options{})
	isfcsync.NewMetrics(m.Registry, nil)
	if _, err := callbacks.NewEnqueuer(noSnapshots{}, m, callbacks.EnqueueOptions{}); err != nil {
		t.Fatal(err)
	}
	// The dispatcher only stores the pool; nothing is queried before Run.
	if _, err := callbacks.NewDispatcher(callbacks.DispatcherDeps{
		Pool: new(pgxpool.Pool), Snapshots: noSnapshots{}, Secrets: noSecrets{}, Audit: noSink{}, Metrics: m, Log: log,
	}, callbacks.DispatcherOptions{ID: "catalog"}); err != nil {
		t.Fatal(err)
	}
	if _, err := newRegistry(Config{BlobURL: "file://" + t.TempDir()}, m, log, nil, nil); err != nil {
		t.Fatal(err)
	}
	return m.Registry
}

type noSnapshots struct{}

func (noSnapshots) Snapshot() *registry.Snapshot { return nil }

type noSecrets struct{}

func (noSecrets) SigningSecrets(context.Context, string) ([][]byte, error) { return nil, nil }

type noSink struct{}

func (noSink) Emit(audit.Batch) {}

// describedNames returns the fqName of every descriptor the registry's collectors describe.
func describedNames(reg *prometheus.Registry) []string {
	ch := make(chan *prometheus.Desc)
	go func() { reg.Describe(ch); close(ch) }()
	var names []string
	for d := range ch {
		s := d.String() // Desc{fqName: "name", help: ...}
		if i := strings.Index(s, `fqName: "`); i >= 0 {
			rest := s[i+len(`fqName: "`):]
			names = append(names, rest[:strings.IndexByte(rest, '"')])
		}
	}
	return names
}
