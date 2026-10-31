package edge

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// authServer serves one protected test route that runs authorize with scope.
func authServer(t testing.TB, snap *registry.Snapshot, scope string) http.Handler {
	t.Helper()
	s := New(Deps{Snapshots: staticSnapshots{snap}})
	mux := http.NewServeMux()
	mux.Handle("POST /v1/integrations/{integration_id}/invoke", s.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := CallerFrom(r.Context())
		if _, ce := authorizeIntegration(c, r.PathValue("integration_id"), scope); ce != nil {
			WriteError(w, r, ce)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	return withRequestID(mux)
}

func call(t testing.TB, h http.Handler, id, authz string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/integrations/"+id+"/invoke", strings.NewReader("{}"))
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthenticateAndAuthorize(t *testing.T) {
	snap := devSnapshot(t)
	// A second tenant with an invoke-only key, a revoked key and a suspended tenant.
	billing := &registry.Tenant{Tenant: "billing", APIKeys: []registry.APIKey{{KeyID: "k_b", Hash: HashKey("hk_test_billing"), Scopes: []string{"invoke"}, State: "active"}}}
	gone := &registry.Tenant{Tenant: "gone", Status: "suspended", APIKeys: []registry.APIKey{{KeyID: "k_g", Hash: HashKey("hk_test_gone"), Scopes: []string{"invoke"}, State: "active"}}}
	revoked := &registry.Tenant{Tenant: "rev", APIKeys: []registry.APIKey{{KeyID: "k_r", Hash: HashKey("hk_test_revoked"), Scopes: []string{"invoke"}, State: "revoked"}}}
	snap = withTenants(t, snap, billing, gone, revoked)

	tests := []struct {
		name, id, authz, scope string
		status                 int
		code, reason           string
	}{
		{"no header", "int_evt_brandA", "", "invoke", 401, "UNAUTHORIZED", ""},
		{"not bearer", "int_evt_brandA", "Basic abc", "invoke", 401, "UNAUTHORIZED", ""},
		{"unknown key", "int_evt_brandA", "Bearer hk_test_nope", "invoke", 401, "UNAUTHORIZED", ""},
		{"revoked key", "int_evt_brandA", "Bearer hk_test_revoked", "invoke", 401, "UNAUTHORIZED", ""},
		{"suspended tenant", "int_evt_brandA", "Bearer hk_test_gone", "invoke", 403, "FORBIDDEN", "tenant_suspended"},
		{"unknown integration", "int_nope", "Bearer " + devKey, "invoke", 404, "INTEGRATION_NOT_FOUND", ""},
		{"not owner or grantee", "int_evt_brandA", "Bearer hk_test_billing", "invoke", 403, "FORBIDDEN", "not_granted"},
		{"missing scope", "int_evt_brandA", "Bearer " + devKey, "admin", 403, "FORBIDDEN", "missing_scope"},
		{"owner with scope", "int_evt_brandA", "Bearer " + devKey, "invoke", 204, "", ""},
		{"scheme is case-insensitive", "int_evt_brandA", "bearer " + devKey, "invoke", 204, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := call(t, authServer(t, snap, tt.scope), tt.id, tt.authz)
			if rec.Code != tt.status || !strings.Contains(rec.Body.String(), tt.code) || !strings.Contains(rec.Body.String(), tt.reason) {
				t.Fatalf("status %d body %s", rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "hk_test") {
				t.Fatal("key echoed in the response")
			}
		})
	}
}

func TestNotReadyWithoutSnapshot(t *testing.T) {
	if rec := call(t, authServer(t, nil, "invoke"), "int_evt_brandA", "Bearer "+devKey); rec.Code != 503 || !strings.Contains(rec.Body.String(), "NOT_READY") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestIntegrationScopedKey(t *testing.T) {
	k := registry.APIKey{Scopes: []string{"dry_run:int_evt_brandA"}}
	if !hasScope(k, ScopeDryRun, "int_evt_brandA") || hasScope(k, ScopeDryRun, "int_evt_brandB") || hasScope(k, ScopeInvoke, "int_evt_brandA") {
		t.Fatal("integration-scoped key mismatch")
	}
}

// withTenants adds tenants (and their keys) to a copy of snap through a rebuilt key index.
func withTenants(t testing.TB, snap *registry.Snapshot, tenants ...*registry.Tenant) *registry.Snapshot {
	t.Helper()
	cp := *snap
	cp.Tenants = map[string]*registry.Tenant{}
	for k, v := range snap.Tenants {
		cp.Tenants[k] = v
	}
	for _, tn := range tenants {
		cp.Tenants[tn.Tenant] = tn
	}
	return registry.ReindexKeys(&cp)
}

// BenchmarkAuthMiddleware: < 100 µs p99 per middleware (DESIGN §18.5 rule 5).
func BenchmarkAuthMiddleware(b *testing.B) {
	h := authServer(b, devSnapshot(b), "invoke")
	durations := make([]time.Duration, 0, 1<<16)
	for b.Loop() {
		start := time.Now()
		if rec := call(b, h, "int_evt_brandA", "Bearer "+devKey); rec.Code != 204 {
			b.Fatal(rec.Code)
		}
		durations = append(durations, time.Since(start))
	}
	slices.Sort(durations)
	p99 := durations[len(durations)*99/100]
	b.ReportMetric(float64(p99.Microseconds()), "p99-µs")
	if p99 > 100*time.Microsecond {
		b.Errorf("p99 %s over 100 µs", p99)
	}
}
