package edge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
)

const growthKey = "hk_test_growth_team_local_only"

// discoverySnapshot adds tenant growth-team, granted int_evt_brandB only.
func discoverySnapshot(t testing.TB) *registry.Snapshot {
	t.Helper()
	snap := withTenants(t, devSnapshot(t), &registry.Tenant{Tenant: "growth-team", APIKeys: []registry.APIKey{
		{KeyID: "key_growth", Hash: HashKey(growthKey), Scopes: []string{"invoke"}, State: "active"},
	}})
	snap.Integrations = map[string]*registry.Integration{}
	for id, in := range devSnapshot(t).Integrations {
		cp := *in
		if id == "int_evt_brandB" {
			cp.Grants = []string{"growth-team"}
		}
		snap.Integrations[id] = &cp
	}
	return snap
}

func get(t *testing.T, spec *contract.Spec, h http.Handler, key, target string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := spec.Serve(t, h, req) // validates status, headers and body against the contract
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: %v", target, err)
	}
	return rec, body
}

func ids(body map[string]any) []string {
	var out []string
	for _, it := range body["items"].([]any) {
		out = append(out, it.(map[string]any)["integration_id"].(string))
	}
	return out
}

func TestDiscoveryVisibility(t *testing.T) {
	spec := contract.Load(t, "hub-public.v1.yaml")
	h := New(Deps{Snapshots: staticSnapshots{discoverySnapshot(t)}}).Handler()

	if _, body := get(t, spec, h, devKey, "/v1/integrations"); strings.Join(ids(body), ",") != "int_evt_brandA,int_evt_brandB,int_evt_brandB_min" {
		t.Fatalf("owner sees %v", ids(body))
	}
	if _, body := get(t, spec, h, growthKey, "/v1/integrations"); strings.Join(ids(body), ",") != "int_evt_brandB" {
		t.Fatalf("grantee sees %v", ids(body))
	}
	if rec, body := get(t, spec, h, growthKey, "/v1/integrations/int_evt_brandB"); rec.Code != 200 || body["owned"] != false {
		t.Fatalf("grantee detail %d %v", rec.Code, body)
	}
	if rec, body := get(t, spec, h, growthKey, "/v1/integrations/int_evt_brandA"); rec.Code != 404 ||
		body["error"].(map[string]any)["code"] != "INTEGRATION_NOT_FOUND" {
		t.Fatalf("not granted must look unknown (no enumeration): %d %v", rec.Code, body)
	}
	if rec, _ := get(t, spec, h, devKey, "/v1/integrations/int_nope"); rec.Code != 404 {
		t.Fatalf("unknown: %d", rec.Code)
	}
	if rec, _ := get(t, spec, h, "hk_wrong", "/v1/integrations"); rec.Code != 401 {
		t.Fatalf("bad key: %d", rec.Code)
	}
}

func TestDiscoveryPagingAndFilter(t *testing.T) {
	spec := contract.Load(t, "hub-public.v1.yaml")
	h := New(Deps{Snapshots: staticSnapshots{devSnapshot(t)}}).Handler()
	var seen []string
	target := "/v1/integrations?limit=2"
	for range 3 {
		_, body := get(t, spec, h, devKey, target)
		seen = append(seen, ids(body)...)
		next, _ := body["next_cursor"].(string)
		if next == "" {
			break
		}
		target = "/v1/integrations?limit=2&cursor=" + next
	}
	if strings.Join(seen, ",") != "int_evt_brandA,int_evt_brandB,int_evt_brandB_min" {
		t.Fatalf("paged %v", seen)
	}
	for _, q := range []string{"limit=0", "limit=201", "limit=x", "cursor=%25%25"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/integrations?"+q, nil)
		req.Header.Set("Authorization", "Bearer "+devKey)
		if rec := spec.ServeInvalid(t, h, req); rec.Code != 400 || !strings.Contains(rec.Body.String(), `"reason":"query"`) {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
}

func TestDiscoveryShapes(t *testing.T) {
	spec := contract.Load(t, "hub-public.v1.yaml")
	snap := devSnapshot(t)
	h := New(Deps{Snapshots: staticSnapshots{snap}}).Handler()
	_, d := get(t, spec, h, devKey, "/v1/integrations/int_evt_brandA")
	if d["owned"] != true || d["delivery_mode"] == "" || d["capability_url"] != nil || d["capability"] != nil {
		t.Fatalf("detail %v", d)
	}
	// ADR-0023: each operation with the request schema callers follow, from the active version.
	ops := d["operations"].([]any)
	op := ops[0].(map[string]any)
	rs := op["request_schema"].(map[string]any)
	body := rs["body"].(map[string]any)
	if len(ops) != 1 || op["name"] != "default" || op["idempotent"] != true || op["description"] != "Track a single event for a user." ||
		body["required"].([]any)[0] != "user" || rs["headers"] != nil {
		t.Fatalf("operations %v", ops)
	}
	_, list := get(t, spec, h, devKey, "/v1/integrations")
	first := list["items"].([]any)[0].(map[string]any)
	if first["operations"].([]any)[0].(map[string]any)["request_schema"] == nil {
		t.Fatalf("list item without request schemas: %v", first)
	}
}

// TestDiscoveryLeaksNoVendorIdentity: no vendor, account or integration-version field or value
// in any discovery response (DESIGN §0).
func TestDiscoveryLeaksNoVendorIdentity(t *testing.T) {
	spec := contract.Load(t, "hub-public.v1.yaml")
	snap := devSnapshot(t)
	h := New(Deps{Snapshots: staticSnapshots{snap}}).Handler()
	var forbidden []string
	for v := range snap.Vendors {
		forbidden = append(forbidden, v)
	}
	for a := range snap.Accounts {
		forbidden = append(forbidden, a)
	}
	for ref := range snap.Transformers {
		forbidden = append(forbidden, `"`+ref.ID+`"`, ref.String())
	}
	forbidden = append(forbidden, `"version"`, `"active_version"`, `"vendor"`, `"account"`, `"targets"`)
	targets := []string{"/v1/integrations"}
	for id := range snap.Integrations {
		targets = append(targets, "/v1/integrations/"+id)
	}
	for _, target := range targets {
		rec, _ := get(t, spec, h, devKey, target)
		low := strings.ToLower(rec.Body.String())
		for _, f := range forbidden {
			if strings.Contains(low, strings.ToLower(f)) {
				t.Errorf("%s leaks %s", target, f)
			}
		}
	}
}
