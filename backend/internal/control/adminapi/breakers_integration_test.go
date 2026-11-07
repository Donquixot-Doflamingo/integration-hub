//go:build integration

package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

var brandAUpload = upstream.BreakerKey{Account: "ct-brandA-in", Endpoint: "clevertap.upload@1"}

// resetBreaker removes the shared record of key now and after the test.
func resetBreaker(a *adminRig, key upstream.BreakerKey) {
	del := func() { _ = a.s.d.Redis.Del(context.Background(), "hub:breaker:"+key.String()).Err() }
	del()
	a.t.Cleanup(del)
}

func init() {
	reset := func(a *adminRig) { resetBreaker(a, brandAUpload) }
	mutationCases["forceOpenBreaker"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/breakers/ct-brandA-in/upload@1/force-open",
		body: `{"duration_ms":60000,"reason":"vendor maintenance"}`, setup: reset}
	mutationCases["forceCloseBreaker"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/breakers/ct-brandA-in/upload@1/force-close",
		body: `{"duration_ms":60000,"reason":"probe endpoint broken"}`, setup: reset}
	mutationCases["clearBreakerForce"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/breakers/ct-brandA-in/upload@1/clear-force",
		body: `{"reason":"maintenance over"}`, setup: func(a *adminRig) {
			reset(a)
			if _, _, err := upstream.ForceShared(a.t.Context(), a.s.d.Redis, brandAUpload, upstream.ForceOpen, time.Time{}, time.Now()); err != nil {
				a.t.Fatal(err)
			}
		}}
}

// TestBreakerBoardAndHolds is HUB-209: force-open from the admin is in force on a data-plane
// node within 1 s; the board lists and filters breakers with forced state and parked backlog;
// the detail shows config, the forced transitions and the integrations; clear returns to
// automatic control (half-open) and a second clear is 409; force-close holds closed.
func TestBreakerBoardAndHolds(t *testing.T) {
	a := newAdminRig(t)
	resetBreaker(a, brandAUpload)
	gw := upstream.NewBreakers(a.s.d.Redis, telemetry.NewMetrics("gateway", "test"), nil)
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan struct{})
	go func() { gw.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	local := gw.Get(brandAUpload, upstream.BreakerPolicy{Window: 30 * time.Second, MinCalls: 20, FailureRatio: 0.5, ConsecutiveTimeouts: 5,
		Open: 30 * time.Second, OpenMax: 10 * time.Minute, HalfOpenProbes: 3})
	time.Sleep(200 * time.Millisecond) // subscription up

	// A parked envelope on this breaker.
	ensurePartitions(t, a.s.d.Pool)
	now := time.Now().UTC()
	env := core.Envelope{RequestID: "req_" + core.NewULID(now), Tenant: "orders-service", IntegrationID: "int_evt_brandA",
		Operation: "default", Mode: "async", Canonical: json.RawMessage(`{}`), NextAttemptAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now.Add(-time.Minute)}
	if err := pgx.BeginFunc(t.Context(), a.s.d.Pool, func(tx pgx.Tx) error {
		return (&queue.Outbox{}).EnqueueParked(t.Context(), tx, env, brandAUpload.Account, brandAUpload.Endpoint)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = a.s.d.Pool.Exec(context.Background(), `DELETE FROM hub.outbox WHERE request_id = $1`, env.RequestID)
	})

	start := time.Now()
	resp, body := a.do(RoleAdmin, http.MethodPost, "/breakers/ct-brandA-in/upload@1/force-open", `{"duration_ms":1800000,"reason":"Vendor maintenance window"}`)
	br, _ := body["breaker"].(map[string]any)
	if resp.Code != 200 || br["state"] != "open" || br["forced"] != "open" || br["forced_until"] == nil || br["parked_count"].(float64) < 1 {
		t.Fatalf("force-open %d %v", resp.Code, body)
	}
	for local.Snapshot().State != upstream.Open {
		if time.Since(start) > time.Second {
			t.Fatal("force-open not on the gateway node within 1 s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ok, _, _ := local.Allow(time.Now().Add(time.Minute)); ok {
		t.Fatal("the gateway admitted a call through a forced-open breaker")
	}
	t.Logf("force-open in force on the gateway after %s", time.Since(start).Round(time.Millisecond))

	_, list := a.do(RoleViewer, http.MethodGet, "/breakers?state=open&vendor_id=clevertap", "")
	found := false
	for _, it := range list["items"].([]any) {
		m := it.(map[string]any)
		if m["account_id"] == "ct-brandA-in" && m["endpoint_ref"] == "upload@1" && m["forced"] == "open" {
			found = true
		}
	}
	if !found {
		t.Fatalf("board %v", list)
	}
	if _, all := a.do(RoleViewer, http.MethodGet, "/breakers?limit=1", ""); all["next_cursor"] == nil {
		t.Fatalf("pages: %v", all) // brand A and brand B targets at least
	}
	_, detail := a.do(RoleViewer, http.MethodGet, "/breakers/ct-brandA-in/upload@1", "")
	trans, _ := detail["transitions"].([]any)
	if len(trans) == 0 || trans[len(trans)-1].(map[string]any)["cause"] != "forced" || trans[len(trans)-1].(map[string]any)["actor"] != "admin@example.com" ||
		detail["config"].(map[string]any)["window_ms"] != 30000.0 {
		t.Fatalf("detail %v", detail)
	}
	if ins := detail["integrations"].([]any); len(ins) == 0 || ins[0] != "int_evt_brandA" {
		t.Fatalf("integrations %v", ins)
	}

	resp, body = a.do(RoleAdmin, http.MethodPost, "/breakers/ct-brandA-in/upload@1/clear-force", `{"reason":"Maintenance finished early"}`)
	if br := body["breaker"].(map[string]any); resp.Code != 200 || br["state"] != "half_open" || br["forced"] != "none" {
		t.Fatalf("clear %d %v", resp.Code, body)
	}
	for local.Snapshot().State != upstream.HalfOpen {
		if time.Since(start) > 3*time.Second {
			t.Fatal("clear not on the gateway")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/breakers/ct-brandA-in/upload@1/clear-force", `{"reason":"again"}`); resp.Code != http.StatusConflict {
		t.Fatalf("second clear: %d", resp.Code)
	}
	resp, body = a.do(RoleAdmin, http.MethodPost, "/breakers/ct-brandA-in/upload@1/force-close", `{"reason":"Probe endpoint broken"}`)
	if br := body["breaker"].(map[string]any); resp.Code != 200 || br["state"] != "closed" || br["forced"] != "closed" || br["forced_until"] != nil {
		t.Fatalf("force-close %d %v", resp.Code, body)
	}

	// Refusals.
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/breakers/nope-account/upload@1/force-open", `{"reason":"unknown account"}`); resp.Code != http.StatusNotFound {
		t.Fatalf("unknown account: %d", resp.Code)
	}
	if resp, _ := a.do(RoleViewer, http.MethodGet, "/breakers/ct-brandA-in/nope@1", ""); resp.Code != http.StatusNotFound {
		t.Fatalf("unknown endpoint: %d", resp.Code)
	}
	bad := a.spec.ServeInvalid(t, a.h, a.request(RoleAdmin, http.MethodPost, "/breakers/ct-brandA-in/upload@1/force-open", `{"duration_ms":10,"reason":"too short"}`))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("short duration: %d", bad.Code)
	}
}
