//go:build integration

package adminapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

const (
	sealedBody = `{"pan":"ABCDE1234F","name":"Asha Rao"}`
	sealedResp = `{"score":781,"pan":"ABCDE1234F"}`
)

func testSealer(t *testing.T) *audit.Sealer {
	t.Helper()
	keys, err := audit.NewLocalKeys([]byte("adminapi-seal-test-key-012345678"))
	if err != nil {
		t.Fatal(err)
	}
	return audit.NewSealer(keys)
}

// sealedRequest records a request under the sealed policy through a real audit.Trail (the
// edge's path) and returns its id.
func sealedRequest(t *testing.T, a *adminRig, id string, sealer *audit.Sealer) string {
	t.Helper()
	ensurePartitions(t, a.s.d.Pool)
	if id == "" {
		id = "req_" + core.NewULID(time.Now())
	}
	created, _ := core.RequestIDTime(id)
	tr := audit.NewTrail(audit.TrailOptions{RequestID: id, Tenant: "orders-service", Policy: registry.PayloadSealed, Sealer: sealer,
		Now: func() time.Time { return created.Add(time.Millisecond) }})
	tr.Add(audit.RequestReceived, 0, []byte(sealedBody), map[string]any{"ip": "hmac:1"})
	tr.Add(audit.IntegrationResolved, 0, nil, map[string]any{"version": 1})
	tr.Add(audit.VendorResponded, 1, []byte(sealedResp), map[string]any{"status": 200})
	b := tr.Batch(audit.Record{RequestID: id, CreatedAt: created, TenantID: "orders-service", IntegrationID: "int_bureau_pull",
		Capability: "events.track@v1", Operation: "default", Mode: "sync", Status: "succeeded", Attempts: 1, CompletedAt: created.Add(time.Second)})
	if err := (audit.PGStore{Pool: a.s.d.Pool}).Write(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	return id
}

var unsealCaseID = "req_" + core.NewULID(time.Now())

func init() {
	mutationCases["unsealRequest"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/requests/" + unsealCaseID + "/unseal",
		body: `{"reason":"Customer dispute SUP-4411: verify bureau response"}`,
		setup: func(a *adminRig) {
			sl := testSealer(a.t)
			a.s.SetSealer(sl)
			sealedRequest(a.t, a, unsealCaseID, sl)
		}}
}

func hexSHA(b string) string { s := sha256.Sum256([]byte(b)); return hex.EncodeToString(s[:]) }

// TestSealedPayloads is HUB-208: sealed bodies are ciphertext in the database, the explorer
// shows hashes and sizes only, unseal needs admin and a ≥ 10 character reason, returns the
// original bytes (hash and size match) with no-store, and writes one change-log row.
func TestSealedPayloads(t *testing.T) {
	a := newAdminRig(t)
	sl := testSealer(t)
	a.s.SetSealer(sl)
	id := sealedRequest(t, a, "", sl)

	// In the database: ciphertext only.
	rows, err := a.s.d.Pool.Query(t.Context(), `SELECT payload_state, payload FROM hub.request_events WHERE request_id = $1 AND payload IS NOT NULL`, id)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var state string
		var payload []byte
		if err := rows.Scan(&state, &payload); err != nil {
			t.Fatal(err)
		}
		n++
		for _, clear := range []string{"ABCDE1234F", "Asha Rao", "781"} {
			if bytes.Contains(payload, []byte(clear)) || state != "sealed" {
				t.Fatalf("%s payload holds %q", state, clear)
			}
		}
	}
	if rows.Err() != nil || n != 2 {
		t.Fatalf("%d sealed rows, %v", n, rows.Err())
	}

	// Explorer: sealed, no payload, hashes and sizes of the original bytes.
	_, detail := a.do(RoleViewer, http.MethodGet, "/requests/"+id, "")
	if detail["sealed"] != true || detail["payload_policy"] != "sealed" || detail["result"] != nil {
		t.Fatalf("detail %v", detail)
	}
	_, tl := a.do(RoleViewer, http.MethodGet, "/requests/"+id+"/timeline", "")
	first := tl["events"].([]any)[0].(map[string]any)
	if first["sealed"] != true || first["payload"] != nil || first["sha256"] != hexSHA(sealedBody) || first["bytes"] != float64(len(sealedBody)) {
		t.Fatalf("timeline %v", first)
	}

	// Refusals: role, reason, not sealed.
	if resp, _ := a.do(RoleEditor, http.MethodPost, "/requests/"+id+"/unseal", `{"reason":"Customer dispute SUP-4411"}`); resp.Code != http.StatusForbidden {
		t.Fatalf("editor: %d", resp.Code)
	}
	short := a.spec.ServeInvalid(t, a.h, a.request(RoleAdmin, http.MethodPost, "/requests/"+id+"/unseal", `{"reason":"SUP-1"}`))
	if short.Code != http.StatusBadRequest {
		t.Fatalf("short reason: %d", short.Code)
	}
	masked := seed(t, a.s.d.Pool, seedRequest{tenant: "orders-service", integration: "int_evt_brandA", status: "succeeded", at: time.Now().UTC()})
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/requests/"+masked+"/unseal", `{"reason":"Customer dispute SUP-4411"}`); resp.Code != http.StatusConflict {
		t.Fatalf("masked request: %d", resp.Code)
	}
	if n := changesOf(t, a, short.Header().Get("X-Hub-Request-Id")); n != 0 {
		t.Fatalf("a refused unseal wrote %d change(s)", n)
	}

	// Unseal one event, then all.
	resp, body := a.do(RoleAdmin, http.MethodPost, "/requests/"+id+"/unseal", `{"event_seqs":[1],"reason":"Customer dispute SUP-4411: verify"}`)
	if resp.Code != 200 || resp.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unseal %d %v", resp.Code, resp.Header())
	}
	evs := body["events"].([]any)
	ev := evs[0].(map[string]any)
	clear, _ := json.Marshal(ev["payload"].(map[string]any)["body"])
	var want any
	_ = json.Unmarshal([]byte(sealedBody), &want)
	wantJSON, _ := json.Marshal(want)
	if len(evs) != 1 || ev["sealed"] != false || string(clear) != string(wantJSON) || ev["sha256"] != hexSHA(sealedBody) || ev["bytes"] != float64(len(sealedBody)) {
		t.Fatalf("unsealed %v", ev)
	}
	// The exact original bytes, not only an equivalent JSON value.
	var raw []byte
	_ = a.s.d.Pool.QueryRow(t.Context(), `SELECT payload FROM hub.request_events WHERE request_id = $1 AND seq = 1`, id).Scan(&raw)
	if pt, _, err := sl.Open("orders-service", id, 1, raw); err != nil || string(pt) != sealedBody || hexSHA(string(pt)) != ev["sha256"] {
		t.Fatalf("bytes %q %v", pt, err)
	}
	var action, objectType, reason string
	var after map[string]any
	if err := a.s.d.Pool.QueryRow(t.Context(), `SELECT action, object_type, reason, after FROM hub.change_log WHERE change_id = $1`, body["change_id"]).
		Scan(&action, &objectType, &reason, &after); err != nil || action != "unsealRequest" || objectType != "request" || reason != "Customer dispute SUP-4411: verify" {
		t.Fatalf("change %s %s %s %v", action, objectType, reason, err)
	}
	if b, _ := json.Marshal(after); bytes.Contains(b, []byte("ABCDE1234F")) {
		t.Fatalf("the change log must not hold payloads: %s", b)
	}
	_, all := a.do(RoleAdmin, http.MethodPost, "/requests/"+id+"/unseal", `{"reason":"Customer dispute SUP-4411: verify"}`)
	if evs := all["events"].([]any); len(evs) != 3 || evs[2].(map[string]any)["payload"].(map[string]any)["body"].(map[string]any)["score"] != 781.0 {
		t.Fatalf("all events %v", all)
	}
}

// request builds an authorized admin request (for ServeInvalid: refusals the contract also refuses).
func (a *adminRig) request(role Role, method, path, body string) *http.Request {
	req := httptest.NewRequestWithContext(a.t.Context(), method, Base+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token(a.t, a.iss, role, "orders-service"))
	req.Header.Set("Content-Type", "application/json")
	return req
}
