//go:build integration

package adminapi

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

const devKeyID = "key_01JDEV0RDERS0000000000000A"

// createKey mints a key for orders-service and returns its id and secret.
func createKey(a *adminRig) (string, string) {
	a.t.Helper()
	resp, body := a.do(RoleAdmin, http.MethodPost, "/api-keys", `{"tenant_id":"orders-service","description":"orders prod","scopes":["invoke","dry_run:int_evt_brandA"],"reason":"initial key"}`)
	if resp.Code != http.StatusCreated {
		a.t.Fatalf("create key: %d %s", resp.Code, resp.Body)
	}
	return body["api_key"].(map[string]any)["key_id"].(string), body["secret"].(string)
}

func init() {
	mutationCases["createApiKey"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/api-keys",
		body: `{"tenant_id":"orders-service","scopes":["invoke"],"reason":"new key"}`}
	mutationCases["revokeApiKey"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/api-keys/" + devKeyID + "/revoke",
		body: `{"reason":"leaked in a CI log"}`}
	mutationCases["rotateApiKey"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/api-keys/" + devKeyID + "/rotate",
		body: `{"overlap_ms":86400000,"reason":"scheduled rotation"}`, setup: restoreDevKey}
}

// restoreDevKey puts the dev tenant file back (another mutation case may have revoked its key).
func restoreDevKey(a *adminRig) {
	a.t.Helper()
	if _, err := a.pub.Edit(a.t.Context(), func(t control.Tree, _ *registry.Snapshot) (control.Change, error) {
		b, err := os.ReadFile("../../../config/tenants/orders-service.yaml")
		t.Put("tenants/orders-service.yaml", b)
		return control.Change{Actor: "test@example.com", Action: "updateTenant", ObjectType: "tenant", ObjectID: "orders-service", Reason: "reset"}, err
	}); err != nil {
		a.t.Fatal(err)
	}
}

// TestAPIKeySecretShownOnce: the plaintext is only in the create response; the tenant file
// keeps its hash; reads show the prefix only.
func TestAPIKeySecretShownOnce(t *testing.T) {
	a := newAdminRig(t)
	id, secret := createKey(a)
	if !strings.HasPrefix(secret, "hk_live_") || len(secret) < 36 {
		t.Fatalf("secret shape %q", secret)
	}
	_, snap, _ := a.pub.Current(t.Context())
	entry, ok := snap.KeyByHash(registry.HashKey(secret))
	if !ok || entry.Key.KeyID != id || entry.Tenant.Tenant != "orders-service" || entry.Key.Prefix != secret[:12] {
		t.Fatalf("snapshot key %+v %v", entry.Key, ok)
	}
	src, _, _ := a.pub.Current(t.Context())
	doc, _ := src.Config.Open("tenants/orders-service.yaml")
	raw := make([]byte, 1<<16)
	n, _ := doc.Read(raw)
	if strings.Contains(string(raw[:n]), secret) || !strings.Contains(string(raw[:n]), registry.HashKey(secret)) {
		t.Fatal("the tenant file must hold the hash, never the key")
	}
	for _, path := range []string{"/api-keys/" + id, "/api-keys?tenant_id=orders-service", "/api-keys?state=active", "/tenants/orders-service"} {
		resp, _ := a.do(RoleTenantViewer, http.MethodGet, path, "")
		if resp.Code != 200 || strings.Contains(resp.Body.String(), secret) || strings.Contains(resp.Body.String(), "sha256:") {
			t.Fatalf("%s leaks key material or failed: %d %s", path, resp.Code, resp.Body)
		}
	}
	resp, body := a.do(RoleViewer, http.MethodGet, "/api-keys?limit=1", "")
	if items, _ := body["items"].([]any); resp.Code != 200 || len(items) != 1 || body["next_cursor"] == nil {
		t.Fatalf("paged list: %s", resp.Body)
	}
	if resp, _ := a.do(RoleTenantViewer, http.MethodGet, "/api-keys?tenant_id=billing-svc", ""); resp.Code != 403 {
		t.Fatalf("out-of-scope tenant filter: %d", resp.Code)
	}
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/api-keys", `{"tenant_id":"no-such-tenant","scopes":["invoke"],"reason":"x tenant"}`); resp.Code != 404 {
		t.Fatalf("unknown tenant: %d", resp.Code)
	}
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/api-keys", `{"tenant_id":"orders-service","scopes":["invoke:int_nope_x"],"reason":"bad scope"}`); resp.Code != 400 {
		t.Fatalf("unknown integration scope: %d", resp.Code)
	}
}

// TestAPIKeyRotateAndRevoke: rotation mints a new key (secret once), the old one is expiring
// until the overlap ends, then unusable; revoke is immediate and a second revoke is 409.
func TestAPIKeyRotateAndRevoke(t *testing.T) {
	a := newAdminRig(t)
	id, oldSecret := createKey(a)
	before := time.Now()
	resp, body := a.do(RoleAdmin, http.MethodPost, "/api-keys/"+id+"/rotate", `{"overlap_ms":3600000,"reason":"scheduled rotation"}`)
	if resp.Code != 200 {
		t.Fatalf("rotate: %d %s", resp.Code, resp.Body)
	}
	newSecret := body["secret"].(string)
	oldKey, newKey := body["old_key"].(map[string]any), body["api_key"].(map[string]any)
	if strings.Contains(resp.Body.String(), oldSecret) || newSecret == oldSecret || oldKey["state"] != "expiring" ||
		oldKey["replaced_by"] != newKey["key_id"] || newKey["state"] != "active" || newKey["scopes"].([]any)[1] != "dry_run:int_evt_brandA" {
		t.Fatalf("rotate result %s", resp.Body)
	}
	_, snap, _ := a.pub.Current(t.Context())
	old, _ := snap.KeyByHash(registry.HashKey(oldSecret))
	fresh, _ := snap.KeyByHash(registry.HashKey(newSecret))
	if !old.Key.Usable(before.Add(59*time.Minute)) || old.Key.Usable(before.Add(61*time.Minute)) || !fresh.Key.Usable(before.Add(48*time.Hour)) {
		t.Fatalf("overlap window: old %+v new %+v", old.Key, fresh.Key)
	}
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/api-keys/"+id+"/rotate", `{"overlap_ms":0,"reason":"rotate again"}`); resp.Code != 409 {
		t.Fatalf("rotating an expiring key: %d", resp.Code)
	}
	if resp, body := a.do(RoleViewer, http.MethodGet, "/api-keys?state=expiring", ""); resp.Code != 200 || len(body["items"].([]any)) != 1 {
		t.Fatalf("state filter: %s", resp.Body)
	}

	newID := newKey["key_id"].(string)
	resp, body = a.do(RoleAdmin, http.MethodPost, "/api-keys/"+newID+"/revoke", `{"reason":"leaked"}`)
	if resp.Code != 200 || body["api_key"].(map[string]any)["state"] != "revoked" || strings.Contains(resp.Body.String(), newSecret) {
		t.Fatalf("revoke: %d %s", resp.Code, resp.Body)
	}
	_, snap, _ = a.pub.Current(t.Context())
	if k, _ := snap.KeyByHash(registry.HashKey(newSecret)); k.Key.Usable(time.Now()) {
		t.Fatal("a revoked key still authenticates")
	}
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/api-keys/"+newID+"/revoke", `{"reason":"again"}`); resp.Code != 409 {
		t.Fatalf("second revoke: %d", resp.Code)
	}
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/api-keys/key_01JDEV0RDERS00000000000ZZZ/revoke", `{"reason":"unknown"}`); resp.Code != 404 {
		t.Fatalf("unknown key: %d", resp.Code)
	}
}
