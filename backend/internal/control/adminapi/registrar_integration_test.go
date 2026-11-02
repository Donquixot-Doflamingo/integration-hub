//go:build integration

package adminapi

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/callbacks"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// fakeResolver answers fixed addresses, so the SSRF tests need no DNS.
type fakeResolver map[string]string

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if ip, ok := f[host]; ok {
		return []netip.Addr{netip.MustParseAddr(ip)}, nil
	}
	return nil, fmt.Errorf("no such host %s", host)
}

func init() {
	guard := callbacks.NewGuard(callbacks.GuardOptions{Resolver: fakeResolver{
		"hooks.example.com": "93.184.215.14", "internal.example.com": "10.1.2.3", "meta.example.com": "169.254.169.254"}})
	checkRegistrarURL = guard.CheckURL

	mutationCases["createRegistrarEndpoint"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/registrar-endpoints",
		body:  `{"endpoint_id":"reg_mut_new","tenant_id":"orders-service","url":"https://hooks.example.com/cb","reason":"callbacks for async"}`,
		setup: func(a *adminRig) { withRotator(a) }}
	mutationCases["updateRegistrarEndpoint"] = mutationCase{role: RoleAdmin, method: http.MethodPatch, path: "/registrar-endpoints/reg_orders_cb",
		body: `{"description":"Orders callbacks","reason":"describe it"}`}
	mutationCases["deleteRegistrarEndpoint"] = mutationCase{role: RoleAdmin, method: http.MethodDelete, path: "/registrar-endpoints/reg_mut_del?reason=replaced",
		setup: func(a *adminRig) {
			withRotator(a)
			createEndpoint(a, "reg_mut_del")
		}}
	mutationCases["rotateRegistrarSecret"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/registrar-endpoints/reg_orders_cb/rotate-secret",
		body: `{"overlap_ms":86400000,"reason":"scheduled rotation"}`, setup: func(a *adminRig) { withRotator(a) }}
}

func createEndpoint(a *adminRig, id string) string {
	a.t.Helper()
	resp, body := a.do(RoleAdmin, http.MethodPost, "/registrar-endpoints", `{"endpoint_id":"`+id+`","tenant_id":"orders-service","url":"https://hooks.example.com/`+id+`","description":"test","reason":"test endpoint"}`)
	if resp.Code != http.StatusCreated {
		a.t.Fatalf("create %s: %d %s", id, resp.Code, resp.Body)
	}
	return body["signing_secret"].(string)
}

func TestRegistrarSSRFAndCRUD(t *testing.T) {
	a := newAdminRig(t)
	store := withRotator(a)
	for _, tc := range []struct{ name, url string }{
		{"private range", "https://internal.example.com/cb"},
		{"link-local metadata", "https://meta.example.com/cb"},
		{"loopback literal", "https://127.0.0.1/cb"},
		{"private literal", "https://192.168.1.10/cb"},
		{"unresolvable", "https://nowhere.example.com/cb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a.t = t
			resp, body := a.do(RoleAdmin, http.MethodPost, "/registrar-endpoints", `{"endpoint_id":"reg_bad","tenant_id":"orders-service","url":"`+tc.url+`","reason":"should fail"}`)
			if e, _ := body["error"].(map[string]any); resp.Code != 400 || e["code"] != CodeValidationFailed {
				t.Fatalf("%d %s", resp.Code, resp.Body)
			}
		})
	}
	a.t = t
	if all, _ := store.ReadAll(); len(all) != 3 {
		t.Fatal("refused registrations stored a secret")
	}

	secret := createEndpoint(a, "reg_billing")
	if !strings.HasPrefix(secret, "whsec_") {
		t.Fatalf("secret %q", secret)
	}
	all, _ := store.ReadAll()
	if all["reg_billing"].Active.Values[creds.SigningSecretKey] != secret {
		t.Fatal("the store does not hold the signing secret")
	}
	_, snap, _ := a.pub.Current(t.Context())
	if e := snap.Registrar["reg_billing"]; e == nil || e.SecretRef != "localfile://reg_billing" || e.SecretVersion != 1 {
		t.Fatalf("snapshot endpoint %+v", e)
	}
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/registrar-endpoints", `{"endpoint_id":"reg_billing","tenant_id":"orders-service","url":"https://hooks.example.com/x","reason":"again"}`); resp.Code != 409 {
		t.Fatalf("duplicate: %d", resp.Code)
	}
	for _, p := range []string{"/registrar-endpoints/reg_billing", "/registrar-endpoints?tenant_id=orders-service"} {
		resp, _ := a.do(RoleTenantViewer, http.MethodGet, p, "")
		if resp.Code != 200 || strings.Contains(resp.Body.String(), secret) || strings.Contains(resp.Body.String(), accessSecrets[4]) {
			t.Fatalf("%s: %d %s", p, resp.Code, resp.Body)
		}
	}
	resp, body := a.do(RoleViewer, http.MethodGet, "/registrar-endpoints/reg_orders_cb", "")
	if resp.Code != 200 || body["subscribed_by"].(float64) < 1 {
		t.Fatalf("subscribed endpoint: %s", resp.Body)
	}

	if resp, _ := a.do(RoleAdmin, http.MethodPatch, "/registrar-endpoints/reg_billing", `{"url":"https://internal.example.com/cb","reason":"move it"}`); resp.Code != 400 {
		t.Fatalf("update to a private host: %d", resp.Code)
	}
	resp, body = a.do(RoleAdmin, http.MethodPatch, "/registrar-endpoints/reg_billing", `{"url":"https://hooks.example.com/v2","description":null,"disabled":true,"reason":"new route"}`)
	ep, _ := body["endpoint"].(map[string]any)
	if resp.Code != 200 || ep["url"] != "https://hooks.example.com/v2" || ep["disabled"] != true || ep["description"] != nil || body["signing_secret"] != nil {
		t.Fatalf("update: %d %s", resp.Code, resp.Body)
	}

	if resp, _ := a.do(RoleAdmin, http.MethodDelete, "/registrar-endpoints/reg_orders_cb?reason=in+use", ""); resp.Code != 409 {
		t.Fatalf("delete subscribed endpoint: %d", resp.Code)
	}
	// A pending delivery to the deleted endpoint is dead-lettered.
	dlv := "dlv_" + core.NewULID(time.Now())
	if _, err := a.s.d.Pool.Exec(t.Context(), `INSERT INTO hub.callback_deliveries (delivery_id, event_id, event_type, request_id, tenant_id,
		integration_id, integration_version, endpoint_id, subscription, payload) VALUES ($1, $2, 'request.completed', $3, 'orders-service',
		'int_evt_brandB', 1, 'reg_billing', 0, '\x7b7d')`, dlv, "evt_"+core.NewULID(time.Now()), "req_"+core.NewULID(time.Now())); err != nil {
		t.Fatal(err)
	}
	if resp, _ := a.do(RoleAdmin, http.MethodDelete, "/registrar-endpoints/reg_billing?reason=replaced", ""); resp.Code != 200 {
		t.Fatalf("delete: %d %s", resp.Code, resp.Body)
	}
	var status string
	_ = a.s.d.Pool.QueryRow(t.Context(), `SELECT status FROM hub.callback_deliveries WHERE delivery_id = $1`, dlv).Scan(&status)
	if status != "dead_lettered" {
		t.Fatalf("pending delivery is %q, want dead_lettered", status)
	}
	if all, _ := store.ReadAll(); all["reg_billing"].Active.Version != 0 {
		t.Fatal("the deleted endpoint's secret is still stored")
	}
	if resp, _ := a.do(RoleViewer, http.MethodGet, "/registrar-endpoints/reg_billing", ""); resp.Code != 404 {
		t.Fatalf("deleted endpoint: %d", resp.Code)
	}
}

// TestRegistrarSecretRotationOverlap: the new secret is shown once; during the overlap the
// signer serves both secrets (new first), afterwards only the new one.
func TestRegistrarSecretRotationOverlap(t *testing.T) {
	a := newAdminRig(t)
	store := withRotator(a)
	start := time.Now()
	resp, body := a.do(RoleAdmin, http.MethodPost, "/registrar-endpoints/reg_orders_cb/rotate-secret", `{"overlap_ms":3600000,"reason":"scheduled rotation"}`)
	if resp.Code != 200 {
		t.Fatalf("rotate: %d %s", resp.Code, resp.Body)
	}
	secret := body["signing_secret"].(string)
	ep := body["endpoint"].(map[string]any)
	if ep["secret_version"].(float64) != 2 || ep["secret_overlap_until"] == nil || strings.Contains(resp.Body.String(), accessSecrets[4]) {
		t.Fatalf("rotate result %s", resp.Body)
	}
	if resp, _ := a.do(RoleViewer, http.MethodGet, "/registrar-endpoints/reg_orders_cb", ""); strings.Contains(resp.Body.String(), secret) {
		t.Fatal("the new secret is shown again")
	}
	now := start
	cache, err := creds.NewCache(store, telemetry.NewSecretSet(), telemetry.NewMetrics("test", "dev"), creds.CacheOptions{Now: func() time.Time { return now }, TTL: 3 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Warm(t.Context(), []string{"reg_orders_cb"}); err != nil {
		t.Fatal(err)
	}
	signer := creds.RegistrarSecrets{Cache: cache}
	got, err := signer.SigningSecrets(t.Context(), "localfile://reg_orders_cb")
	if err != nil || len(got) != 2 || string(got[0]) != secret || string(got[1]) != accessSecrets[4] {
		t.Fatalf("during the overlap: %d secrets, %v", len(got), err)
	}
	now = start.Add(61 * time.Minute)
	if got, err = signer.SigningSecrets(t.Context(), "localfile://reg_orders_cb"); err != nil || len(got) != 1 || string(got[0]) != secret {
		t.Fatalf("after the overlap: %d secrets, %v", len(got), err)
	}
	// overlap 0: the old secret stops at once.
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/registrar-endpoints/reg_orders_cb/rotate-secret", `{"overlap_ms":0,"reason":"leaked"}`); resp.Code != 200 {
		t.Fatalf("rotate 0: %d", resp.Code)
	}
	if all, _ := store.ReadAll(); all["reg_orders_cb"].Next != nil || all["reg_orders_cb"].Active.Version != 3 {
		t.Fatalf("after overlap 0: %+v", all["reg_orders_cb"].Views())
	}
}
