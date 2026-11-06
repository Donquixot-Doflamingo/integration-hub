//go:build integration

package edge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/multiop"
)

// HUB-343: wait mode (attempt in the gateway) and async mode (the worker re-resolves at send
// time from the envelope's operation) run the invoked operation's transformer pair, so send and
// verify of int_two_step reach two mockvendor endpoints.

// perOpVendorSnapshot is the multiop overlay with every vendor region pointed at vendorURL.
func perOpVendorSnapshot(t *testing.T, vendorURL string) *registry.Snapshot {
	t.Helper()
	snap := *perOpSnapshot(t, nil)
	vendors := map[string]*registry.Vendor{}
	for k, v := range snap.Vendors {
		vc := *v
		vc.Regions = map[string]string{}
		for r := range v.Regions {
			vc.Regions[r] = vendorURL
		}
		vendors[k] = &vc
	}
	snap.Vendors = vendors
	return &snap
}

// perOpRig is a queued rig on the overlay with int_two_step in mode (polling, no failover), and
// mockvendor answering both endpoints.
func perOpRig(t *testing.T, mode registry.Mode) *queuedRig {
	t.Helper()
	q := newQueuedRigFrom(t, perOpVendorSnapshot, "", func(id string, _ *registry.Integration, v *registry.IntegrationVersion) {
		if id != multiop.Integration {
			return
		}
		v.Delivery.Mode, v.Delivery.Polling, v.Delivery.OnCircuitOpen, v.Failover = mode, true, registry.FailFast, nil
		if mode == registry.ModeWait {
			v.Delivery.WaitMS = 3000
		}
	})
	q.mv.Reset(t)
	for name, path := range map[string]string{"send": "/v1/codes", "verify": "/v1/codes/verify"} {
		q.mv.SetRoute(t, name, mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: path},
			Behaviour: mockvendor.Behaviour{Status: 200, Body: json.RawMessage(`{"id": "sv_ref_1", "valid": true}`)}})
	}
	return q
}

// invokeOp calls one operation of int_two_step, validated against the caller contract.
func (q *queuedRig) invokeOp(t *testing.T, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+devKey)
	req.Header.Set("Content-Type", "application/json")
	rec := q.spec.Serve(t, q.h, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// vendorPaths lists the paths mockvendor received, in order.
func (q *queuedRig) vendorPaths(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, r := range q.mv.Requests(t, nil) {
		out = append(out, r.Path)
	}
	return out
}

var perOpCases = []struct {
	op, path, body, vendorPath, status string
}{
	{"send", twoStepSend, twoSendBody, "/v1/codes", "sent"},
	{"verify", twoStepVerify, twoVerifyBody, "/v1/codes/verify", "verified"},
}

func TestPerOperationWaitMode(t *testing.T) {
	q := perOpRig(t, registry.ModeWait)
	for i, c := range perOpCases {
		rec, body := q.invokeOp(t, c.path, c.body)
		if rec.Code != 200 || body["status"] != c.status {
			t.Fatalf("%s: %d %s", c.op, rec.Code, rec.Body)
		}
		if paths := q.vendorPaths(t); len(paths) != i+1 || paths[i] != c.vendorPath {
			t.Fatalf("%s: vendor paths %v", c.op, paths)
		}
	}
}

func TestPerOperationAsyncWorker(t *testing.T) {
	q := perOpRig(t, registry.ModeAsync)
	for i, c := range perOpCases {
		rec, body := q.invokeOp(t, c.path, c.body)
		if rec.Code != 202 || body["status"] != "accepted" {
			t.Fatalf("%s: %d %s", c.op, rec.Code, rec.Body)
		}
		if n, err := q.worker.ProcessOnce(t.Context()); err != nil || n != 1 {
			t.Fatalf("%s: worker %d %v", c.op, n, err)
		}
		code, st := q.status(t, devKey, body["request_id"].(string))
		res, _ := st["result"].(map[string]any)
		if code != 200 || st["status"] != "succeeded" || res["status"] != c.status {
			t.Fatalf("%s: status %d %v", c.op, code, st)
		}
		if paths := q.vendorPaths(t); len(paths) != i+1 || paths[i] != c.vendorPath {
			t.Fatalf("%s: vendor paths %v", c.op, paths)
		}
	}
}
