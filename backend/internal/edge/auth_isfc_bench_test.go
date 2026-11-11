package edge

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// HUB-490: the auth stage with 10,000 ISFC caller keys (AUTH_PLAN §8.2, ADR-0028). The budget is
// DESIGN §18.5 rule 5 and §18.6: every middleware stays under 100 µs p99; no OPA on this path.
// Run: make bench-auth. Results are recorded in docs/tech/LATENCY.md §7.
const (
	benchKeys    = 10_000
	benchTenants = 200
	authBudget   = 100 * time.Microsecond
)

type discardWriter struct{ h http.Header }

func (w *discardWriter) Header() http.Header         { return w.h }
func (w *discardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *discardWriter) WriteHeader(int)             {}

func benchUUID(i int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", i) }

// isfcBench builds the edge with benchTenants ISFC-linked tenants and benchKeys ISFC keys: every
// 10th key covers three tenants (and its requests send X-Tenant-Id), the rest cover one.
func isfcBench(b testing.TB) (*Server, []*http.Request) {
	b.Helper()
	snap := devSnapshot(b)
	tenants := make([]*registry.Tenant, benchTenants)
	for i := range tenants {
		tenants[i] = isfcTenant(fmt.Sprintf("tenant-%03d", i), benchUUID(i), nil)
	}
	snap = withTenants(b, snap, tenants...)
	entries := make([]isfc.CallerEntry, benchKeys)
	reqs := make([]*http.Request, benchKeys)
	for i := range entries {
		covered := []string{benchUUID(i % benchTenants)}
		client := fmt.Sprintf("client-%05d", i)
		key := fmt.Sprintf("isfc_bench_key_%05d_xxxxxxxxxxxxxxxxxxxxxxxx", i)
		req := httptest.NewRequestWithContext(b.Context(), http.MethodPost, "/v1/integrations/int_evt_brandA/invoke", nil)
		req.Header.Set("x-isfc-client-id", client)
		req.Header.Set("x-isfc-api-key", key)
		if i%10 == 0 {
			covered = append(covered, benchUUID((i+1)%benchTenants), benchUUID((i+2)%benchTenants))
			req.Header.Set("X-Tenant-Id", benchUUID(i%benchTenants))
		}
		entries[i] = isfc.CallerEntry{KeyID: fmt.Sprintf("ik_%05d", i), Hash: HashKey(key), ClientID: client, TenantIDs: covered,
			Scopes: isfc.ScopesFromPermissions([]string{"HUB:INVOKE", "HUB:DRY_RUN", "HUB:REQUESTS_READ"})}
		reqs[i] = req
	}
	var km isfc.KeyMap
	km.Replace(entries)
	return New(Deps{Snapshots: staticSnapshots{snap}, ISFCKeys: &km}), reqs
}

// BenchmarkAuthISFC10k measures the whole auth stage (the TimedMiddleware wrapper, hash, lookup,
// tenant resolution, state checks, caller context) over 10,000 keys, one random-ish key per
// iteration so the map is not served from a hot cache line, and fails above 100 µs p99.
func BenchmarkAuthISFC10k(b *testing.B) {
	s, reqs := isfcBench(b)
	runAuthBench(b, s, reqs)
}

// BenchmarkAuthHubKeys10k is the comparison: the same stage with 10,000 Hub tenant keys.
func BenchmarkAuthHubKeys10k(b *testing.B) {
	snap := devSnapshot(b)
	tenants := make([]*registry.Tenant, benchTenants)
	reqs := make([]*http.Request, benchKeys)
	for i := range tenants {
		tenants[i] = &registry.Tenant{Tenant: fmt.Sprintf("tenant-%03d", i), APIKeys: []registry.APIKey{}}
	}
	for i := range reqs {
		key := fmt.Sprintf("hk_bench_key_%05d_xxxxxxxxxxxxxxxxxxxxxxxxxxx", i)
		t := tenants[i%benchTenants]
		t.APIKeys = append(t.APIKeys, registry.APIKey{KeyID: fmt.Sprintf("k_%05d", i), Hash: HashKey(key), Scopes: []string{"invoke"}, State: registry.KeyActive})
		req := httptest.NewRequestWithContext(b.Context(), http.MethodPost, "/v1/integrations/int_evt_brandA/invoke", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		reqs[i] = req
	}
	snap = withTenants(b, snap, tenants...)
	runAuthBench(b, New(Deps{Snapshots: staticSnapshots{snap}}), reqs)
}

func runAuthBench(b *testing.B, s *Server, reqs []*http.Request) {
	b.Helper()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	h := s.authenticate(next)
	w := &discardWriter{h: http.Header{}}
	durations := make([]time.Duration, 0, 1<<17)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		r := reqs[i%len(reqs)]
		i += 7919 // a prime stride: successive iterations hit unrelated keys
		start := time.Now()
		h.ServeHTTP(w, r)
		durations = append(durations, time.Since(start))
	}
	slices.Sort(durations)
	p := func(q int) float64 { return float64(durations[len(durations)*q/100].Nanoseconds()) / 1000 }
	b.ReportMetric(p(50), "p50-µs")
	b.ReportMetric(p(99), "p99-µs")
	if got := durations[len(durations)*99/100]; got > authBudget {
		b.Errorf("auth stage p99 %s over the %s budget with %d keys", got, authBudget, benchKeys)
	}
}

// TestAuthISFCBenchSetupIsValid keeps the benchmark honest: every request authenticates, and the
// multi-tenant keys (every 10th) resolve through X-Tenant-Id.
func TestAuthISFCBenchSetupIsValid(t *testing.T) {
	s, reqs := isfcBench(t)
	h := s.authenticate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, i := range []int{0, 1, 9, 10, 20, 4999, benchKeys - 1} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, reqs[i])
		if rec.Code != http.StatusNoContent {
			t.Fatalf("key %d: %d %s", i, rec.Code, rec.Body)
		}
	}
}
