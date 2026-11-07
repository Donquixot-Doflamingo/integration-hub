//go:build integration

package edge

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
)

// HUB-413: nothing optional runs on the caller's request. TestSyncRequestDoesNotWaitForAudit
// (nonblocking_test.go) covers the audit ledger; these cover queued delivery and callbacks.

// slowVendorMS is a vendor far slower than any accept: a caller that waited for it would see it.
const slowVendorMS = 2000

// TestAsyncModesAcceptWithoutWaitingForTheVendor: async and async_callback answer 202 as soon as
// the request is durably queued. The vendor takes 2 s and has not been called when the caller
// has its answer; the worker calls it afterwards, off the caller's request.
func TestAsyncModesAcceptWithoutWaitingForTheVendor(t *testing.T) {
	for _, mode := range []registry.Mode{registry.ModeAsync, registry.ModeAsyncCallback} {
		t.Run(string(mode), func(t *testing.T) {
			q := newQueuedRig(t, "", func(id string, _ *registry.Integration, v *registry.IntegrationVersion) {
				if id == "int_evt_brandB" { // async_callback in the dev config, with webhooks
					v.Delivery.Mode, v.Delivery.WaitMS = mode, 0
				}
			})
			q.route(t, mockvendor.Behaviour{Status: 200, Body: okVendor, Latency: &mockvendor.Latency{Mode: "fixed", FixedMS: slowVendorMS}})
			start := time.Now()
			rec, body := q.invoke(t, "int_evt_brandB", trackBody)
			took := time.Since(start)
			if rec.Code != http.StatusAccepted || body["status"] != "accepted" || body["reason"] != "async_mode" {
				t.Fatalf("%d %v", rec.Code, body)
			}
			if took > slowVendorMS*time.Millisecond/2 {
				t.Fatalf("the 202 took %s with a %d ms vendor", took, slowVendorMS)
			}
			if n := len(q.mv.Requests(t, nil)); n != 0 {
				t.Fatalf("the vendor was called %d times before the caller had its 202", n)
			}
			// The worker, not the caller, pays the vendor's 2 s.
			id := body["request_id"].(string)
			if n, err := q.worker.ProcessOnce(t.Context()); err != nil || n != 1 {
				t.Fatalf("worker %d %v", n, err)
			}
			if n := len(q.mv.Requests(t, nil)); n != 1 {
				t.Fatalf("vendor calls %d after the worker ran", n)
			}
			if _, st := q.status(t, devKey, id); st["status"] != "succeeded" {
				t.Fatalf("status %v", st)
			}
		})
	}
}

// TestCallbackDeliveryIsNotOnTheCallersRequest: in wait mode a request that finishes inside the
// window answers 200 and enqueues its request.completed callback in the same transaction. The
// tenant's receiver hangs every delivery; the caller still has its answer, and the callback
// reaches the receiver through the dispatcher once the receiver lets go. A caller that waited
// for the callback POST would hang with the receiver.
func TestCallbackDeliveryIsNotOnTheCallersRequest(t *testing.T) {
	q, rc, _ := callbackRig(t, func(id string, _ *registry.Integration, v *registry.IntegrationVersion) {
		if id == "int_evt_brandB" {
			v.Delivery.Mode, v.Delivery.WaitMS = registry.ModeWait, 3000
		}
	})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) }) // before the receiver's server closes
	entered := rc.holdDeliveries(release)
	q.route(t, mockvendor.Behaviour{Status: 200, Body: okVendor})

	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec, _ := q.invoke(t, "int_evt_brandB", trackBody)
		answered <- rec
	}()
	var rec *httptest.ResponseRecorder
	select {
	case rec = <-answered:
	case <-time.After(10 * time.Second):
		t.Fatal("the caller is waiting for its callback to be delivered")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("wait mode inside the window: %d %s", rec.Code, rec.Body)
	}
	id := rec.Header().Get("X-Hub-Request-Id")
	select {
	case <-entered: // the dispatcher is delivering; the receiver is holding it
	case <-time.After(10 * time.Second):
		t.Fatal("the callback was never delivered")
	}
	if ev, _, _ := rc.get(id); ev != nil {
		t.Fatalf("callback recorded while the receiver holds it: %v", ev)
	}
	once.Do(func() { close(release) })
	eventually(t, "the held callback", func() bool { ev, _, _ := rc.get(id); return ev != nil })
	if ev, _, _ := rc.get(id); ev["type"] != "request.completed" {
		t.Fatalf("callback %v", ev)
	}
}
