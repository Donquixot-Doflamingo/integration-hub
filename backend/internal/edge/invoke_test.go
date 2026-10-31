package edge

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// step is one scripted upstream outcome: a response, or a transport error.
type step struct {
	status  int
	headers map[string]string
	body    any
	err     error
}

// fakeUpstream plays steps in order (the last one repeats) and records requests.
type fakeUpstream struct {
	mu    sync.Mutex
	steps []step
	reqs  []core.VendorRequest
}

func (f *fakeUpstream) Attempt(_ context.Context, _ upstream.Target, req core.VendorRequest) (upstream.Attempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	s := f.steps[min(len(f.reqs), len(f.steps))-1]
	if s.err != nil {
		return upstream.Attempt{Latency: time.Millisecond}, s.err
	}
	h := http.Header{}
	for k, v := range s.headers {
		h.Set(k, v)
	}
	return upstream.Attempt{Response: core.VendorResponse{Status: s.status, Headers: h, Body: s.body}, Latency: time.Millisecond}, nil
}

func (f *fakeUpstream) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func ok200() step {
	return step{status: 200, headers: map[string]string{"X-Request-Id": "vr_1"}, body: map[string]any{"status": "success", "processed": 1.0, "unprocessed": []any{}}}
}

func timeout(callerDeadline bool) step {
	return step{err: &upstream.TransportError{Kind: upstream.KindTimeout, Phase: "response", CallerDeadline: callerDeadline}}
}

func invokeServer(t *testing.T, snap *registry.Snapshot, up *fakeUpstream, opts ...func(*Deps)) (http.Handler, *recordingSink) {
	t.Helper()
	sink := &recordingSink{}
	d := Deps{Snapshots: staticSnapshots{snap}, Creds: credsFunc, Upstream: up, Audit: sink}
	for _, o := range opts {
		o(&d)
	}
	return New(d).Handler(), sink
}

func errorCode(t *testing.T, body string) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	e, _ := env["error"].(map[string]any)
	return e
}

func TestInvokeHappyPath(t *testing.T) {
	up := &fakeUpstream{steps: []step{ok200()}}
	h, sink := invokeServer(t, devSnapshot(t), up)
	rec := post(t, h, devKey, "/v1/integrations/int_evt_brandA/invoke", trackBody)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["status"] != "ok" || out["accepted"] != 1.0 || out["vendor_ref"] != "vr_1" {
		t.Fatalf("canonical response %v", out)
	}
	if rec.Header().Get("X-Hub-Served-By") != "primary" || rec.Header().Get("X-Hub-Integration-Version") != "" {
		t.Fatalf("headers %v", rec.Header())
	}
	// The vendor request carries the credentials; the audit trail never does.
	if req := up.reqs[0]; req.Headers["X-CleverTap-Passcode"] != fakeCreds["passcode"] || req.Path != "/1/upload" {
		t.Fatalf("vendor request %+v", req)
	}
	b := sink.only(t)
	want := "request.received,request.validated,integration.resolved,request.transformed,vendor.sent,vendor.responded,response.transformed,request.completed"
	if got := types(b); got != want {
		t.Fatalf("events\n got %s\nwant %s", got, want)
	}
	for _, e := range b.Events {
		for _, secret := range fakeCreds {
			if strings.Contains(string(e.Payload), secret) || strings.Contains(string(mustJSON(t, e.Meta)), secret) {
				t.Fatalf("%s stores a credential: %s", e.Type, e.Payload)
			}
		}
	}
	if r := b.Records[0]; r.Status != "succeeded" || r.Attempts != 1 || r.HTTPStatus != 200 || r.IntegrationVersion != 2 {
		t.Fatalf("record %+v", r)
	}
	if a := b.Attempts; len(a) != 1 || a[0].HTTPStatus != 200 || a[0].AccountID != "ct-brandA-in" || a[0].EndpointID != "upload@1" {
		t.Fatalf("attempts %+v", a)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInvokeDebugVersionHeader(t *testing.T) {
	h, _ := invokeServer(t, devSnapshot(t), &fakeUpstream{steps: []step{ok200()}}, func(d *Deps) { d.DebugHeaders = true })
	if rec := post(t, h, devKey, "/v1/integrations/int_evt_brandA/invoke", trackBody); rec.Header().Get("X-Hub-Integration-Version") != "2" {
		t.Fatalf("debug header %q", rec.Header().Get("X-Hub-Integration-Version"))
	}
}

// TestOutcomeClassification: DESIGN §4.4/§10.2 for both a transformer with error rules (R1,
// int_evt_brandA) and one without (R2, int_evt_brandB_min: status fallback).
func TestOutcomeClassification(t *testing.T) {
	cases := []struct {
		name        string
		integration string
		step        step
		status      int
		code        string
		retryAfter  float64
		message     string
	}{
		{"timeout", "int_evt_brandA", timeout(false), 504, "UPSTREAM_TIMEOUT", 0, ""},
		{"connect refused", "int_evt_brandA", step{err: &upstream.TransportError{Kind: upstream.KindConnect}}, 503, "UPSTREAM_UNAVAILABLE", 0, ""},
		{"egress denied", "int_evt_brandA", step{err: &upstream.TransportError{Kind: upstream.KindEgressDenied}}, 503, "UPSTREAM_UNAVAILABLE", 0, ""},
		{"5xx rule", "int_evt_brandA", step{status: 502, body: map[string]any{"status": "fail"}}, 503, "UPSTREAM_UNAVAILABLE", 0, ""},
		{"5xx fallback", "int_evt_brandB_min", step{status: 500, body: "<html>oops</html>"}, 503, "UPSTREAM_UNAVAILABLE", 0, ""},
		{"429 rule", "int_evt_brandA", step{status: 429, headers: map[string]string{"Retry-After": "7"}}, 429, "UPSTREAM_RATE_LIMITED", 7000, ""},
		{"429 fallback Retry-After", "int_evt_brandB_min", step{status: 429, headers: map[string]string{"Retry-After": "3"}}, 429, "UPSTREAM_RATE_LIMITED", 3000, ""},
		{"401 rule", "int_evt_brandA", step{status: 401, body: map[string]any{"status": "fail"}}, 502, "UPSTREAM_AUTH_FAILED", 0, ""},
		{"403 fallback", "int_evt_brandB_min", step{status: 403}, 502, "UPSTREAM_AUTH_FAILED", 0, ""},
		{"4xx rule message", "int_evt_brandA", step{status: 400, body: map[string]any{"status": "fail", "error": "Invalid identity"}}, 422, "UPSTREAM_REJECTED", 0, "Invalid identity"},
		{"4xx fallback message", "int_evt_brandB_min", step{status: 400, body: map[string]any{"error": map[string]any{"message": "evtName too long", "code": 512.0}}}, 422, "UPSTREAM_REJECTED", 0, "evtName too long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, sink := invokeServer(t, devSnapshot(t), &fakeUpstream{steps: []step{tc.step}})
			rec := post(t, h, devKey, "/v1/integrations/"+tc.integration+"/invoke", trackBody)
			e := errorCode(t, rec.Body.String())
			if rec.Code != tc.status || e["code"] != tc.code {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if tc.retryAfter > 0 && e["retry_after_ms"] != tc.retryAfter {
				t.Fatalf("retry_after_ms %v", e["retry_after_ms"])
			}
			if tc.message != "" && e["message"] != tc.message {
				t.Fatalf("message %q", e["message"])
			}
			if r := sink.only(t).Records[0]; r.Status != "failed" || r.ErrorCode != tc.code {
				t.Fatalf("record %+v", r)
			}
		})
	}
}

// nonIdempotent returns a snapshot copy whose clevertap upload endpoint is not idempotent.
func nonIdempotent(t *testing.T) *registry.Snapshot {
	snap := devSnapshot(t)
	cp := *snap
	cp.Vendors = map[string]*registry.Vendor{}
	for k, v := range snap.Vendors {
		vc := *v
		vc.Endpoints = map[string]registry.Endpoint{}
		for name, ep := range v.Endpoints {
			ep.Idempotent = false
			vc.Endpoints[name] = ep
		}
		cp.Vendors[k] = &vc
	}
	return &cp
}

func TestSyncRetryRule(t *testing.T) {
	reset := step{err: &upstream.TransportError{Kind: upstream.KindReset}}
	cases := []struct {
		name   string
		snap   func(*testing.T) *registry.Snapshot
		first  step
		key    string
		calls  int
		status int
	}{
		{"timeout, idempotent endpoint: one retry", devSnapshot2, timeout(false), "", 2, 200},
		{"reset, idempotent endpoint: one retry", devSnapshot2, reset, "", 2, 200},
		{"timeout twice: no third attempt", devSnapshot2, timeout(false), "", 2, 504},
		{"non-idempotent without key: never retried", nonIdempotent, timeout(false), "", 1, 504},
		{"non-idempotent with key: one retry", nonIdempotent, timeout(false), "order-8821", 2, 200},
		{"caller deadline: never retried", devSnapshot2, timeout(true), "", 1, 504},
		{"connect failure: not retried", devSnapshot2, step{err: &upstream.TransportError{Kind: upstream.KindConnect}}, "", 1, 503},
		{"5xx answer: not retried", devSnapshot2, step{status: 503, body: map[string]any{"status": "fail"}}, "", 1, 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps := []step{tc.first, ok200()}
			if tc.name == "timeout twice: no third attempt" {
				steps = []step{tc.first, tc.first, ok200()}
			}
			up := &fakeUpstream{steps: steps}
			h, sink := invokeServer(t, tc.snap(t), up)
			var hdr []string
			if tc.key != "" {
				hdr = []string{"Idempotency-Key", tc.key}
			}
			rec := post(t, h, devKey, "/v1/integrations/int_evt_brandA/invoke", trackBody, hdr...)
			if rec.Code != tc.status || up.calls() != tc.calls {
				t.Fatalf("status %d calls %d: %s", rec.Code, up.calls(), rec.Body)
			}
			b := sink.only(t)
			if b.Records[0].Attempts != tc.calls || len(b.Attempts) != tc.calls {
				t.Fatalf("attempts %d rows %d", b.Records[0].Attempts, len(b.Attempts))
			}
		})
	}
}

func devSnapshot2(t *testing.T) *registry.Snapshot { return devSnapshot(t) }

func TestInvokeRejections(t *testing.T) {
	snap := devSnapshot(t)
	paused := *snap
	paused.Integrations = map[string]*registry.Integration{}
	for k, v := range snap.Integrations {
		c := *v
		c.Paused = k == "int_evt_brandA"
		paused.Integrations[k] = &c
	}
	money := *snap
	money.Integrations = map[string]*registry.Integration{}
	for k, v := range snap.Integrations {
		c := *v
		c.MovesMoneyOrQuota = true
		money.Integrations[k] = &c
	}
	cases := []struct {
		name   string
		snap   *registry.Snapshot
		target string
		hdr    []string
		status int
		code   string
	}{
		{"paused", &paused, "/v1/integrations/int_evt_brandA/invoke", nil, 503, "INTEGRATION_PAUSED"},
		{"idempotency key required", &money, "/v1/integrations/int_evt_brandA/invoke", nil, 400, "IDEMPOTENCY_KEY_REQUIRED"},
		{"unknown operation", snap, "/v1/integrations/int_evt_brandA/invoke/verify", nil, 404, "OPERATION_NOT_FOUND"},
		{"default operation by name", snap, "/v1/integrations/int_evt_brandA/invoke/default", nil, 200, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := &fakeUpstream{steps: []step{ok200()}}
			h, _ := invokeServer(t, tc.snap, up)
			rec := post(t, h, devKey, tc.target, trackBody, tc.hdr...)
			if rec.Code != tc.status || (tc.code != "" && errorCode(t, rec.Body.String())["code"] != tc.code) {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if tc.status != 200 && up.calls() != 0 {
				t.Fatalf("rejected request reached the upstream %d times", up.calls())
			}
		})
	}
}

func TestInvokeBadDeadline(t *testing.T) {
	h, _ := invokeServer(t, devSnapshot(t), &fakeUpstream{steps: []step{ok200()}})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/integrations/int_evt_brandA/invoke", strings.NewReader(trackBody))
	req.Header.Set("Authorization", "Bearer "+devKey)
	req.Header.Set("X-Request-Deadline", "0")
	rec := specInvalid(t, h, req)
	if rec.Code != 400 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
