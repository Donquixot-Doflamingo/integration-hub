package edge

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

const isfcSecretKey = "isfc_orders_super_secret_key_value"

func isfcAuditRig(t *testing.T, sink audit.Sink, logs *bytes.Buffer) http.Handler {
	t.Helper()
	snap := devSnapshot(t)
	orders := *snap.Tenants["orders-service"]
	orders.ISFC = &registry.TenantISFC{TenantID: uOrders, Key: "ORDERS", Name: "Orders", Status: registry.ISFCStatusActive, Linked: true}
	snap = withTenants(t, snap, &orders)
	var km isfc.KeyMap
	km.Replace([]isfc.CallerEntry{{KeyID: "ik_orders", Hash: HashKey(isfcSecretKey), ClientID: "orders-backend", TenantIDs: []string{uOrders},
		Scopes: isfc.ScopesFromPermissions([]string{"HUB:INVOKE", "HUB:DRY_RUN"})}})
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return New(Deps{Snapshots: staticSnapshots{snap}, Creds: credsFunc, Audit: sink, Masker: audit.NewMasker([]byte("test-mask-key")), ISFCKeys: &km, Log: log}).Handler()
}

func isfcPost(t *testing.T, h http.Handler, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestISFCCallerIsAuditedWithoutItsKey is HUB-488: request.received carries caller_client_id and
// isfc_key_id (and the key hash), and the x-isfc-api-key value is in no audit event, no record and
// no log line, on success and on failure.
func TestISFCCallerIsAuditedWithoutItsKey(t *testing.T) {
	sink, logs := &recordingSink{}, &bytes.Buffer{}
	h := isfcAuditRig(t, sink, logs)
	creds := map[string]string{"x-isfc-client-id": "orders-backend", "x-isfc-api-key": isfcSecretKey}

	rec := isfcPost(t, h, "/v1/integrations/int_evt_brandA/dry-run", trackBody, creds)
	if rec.Code != 200 {
		t.Fatalf("dry-run: %d %s", rec.Code, rec.Body)
	}
	b := sink.only(t)
	m := b.Events[0].Meta
	if b.Events[0].Type != audit.RequestReceived || m["caller_client_id"] != "orders-backend" || m["isfc_key_id"] != "ik_orders" ||
		m["key_hash"] != HashKey(isfcSecretKey) || m["auth_method"] != "isfc" {
		t.Fatalf("request.received meta %v", m)
	}
	// A failing request after authorization is audited too, still without the key.
	isfcPost(t, h, "/v1/integrations/int_evt_brandA/dry-run", `{"user": {}}`, creds)
	// And a refused one is only logged.
	isfcPost(t, h, "/v1/integrations/int_evt_brandA/dry-run", trackBody, map[string]string{"x-isfc-client-id": "other-backend", "x-isfc-api-key": isfcSecretKey})

	sink.mu.Lock()
	all, _ := json.Marshal(sink.batches)
	sink.mu.Unlock()
	for name, haystack := range map[string]string{"audit": string(all), "response": rec.Body.String(), "logs": logs.String()} {
		if strings.Contains(haystack, isfcSecretKey) {
			t.Errorf("the x-isfc-api-key value is in the %s: %.300s", name, haystack)
		}
	}
	if strings.Contains(string(all), "x-isfc-api-key") || strings.Contains(strings.ToLower(string(all)), "x-isfc-api-key") {
		t.Errorf("the audit trail names the credential header: %.300s", all)
	}
}

// TestHubKeyRequestsCarryNoISFCFields: the Hub key path records exactly what it did before.
func TestHubKeyRequestsCarryNoISFCFields(t *testing.T) {
	sink, logs := &recordingSink{}, &bytes.Buffer{}
	h := isfcAuditRig(t, sink, logs)
	rec := isfcPost(t, h, "/v1/integrations/int_evt_brandA/dry-run", trackBody, map[string]string{"Authorization": "Bearer " + devKey})
	if rec.Code != 200 {
		t.Fatalf("dry-run: %d %s", rec.Code, rec.Body)
	}
	m := sink.only(t).Events[0].Meta
	for _, k := range []string{"caller_client_id", "isfc_key_id", "auth_method"} {
		if _, ok := m[k]; ok {
			t.Errorf("Hub key request has %s: %v", k, m)
		}
	}
	if m["key_hash"] != HashKey(devKey) {
		t.Fatalf("meta %v", m)
	}
}
