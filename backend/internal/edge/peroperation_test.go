package edge

import (
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/multiop"
)

// HUB-343: each operation of a multi-operation integration runs its own transformer pair, so
// send and verify of int_two_step go to two vendor endpoints (internal/testkit/multiop). These
// tests cover the sync path, the failover chain, dry-run and shadow; the wait and async paths
// are in peroperation_integration_test.go, session steps in the OTP example test.

const (
	twoStepSend   = "/v1/integrations/int_two_step/invoke/send"
	twoStepVerify = "/v1/integrations/int_two_step/invoke/verify"
	twoSendBody   = `{"mobile": "+919812345678"}`
	twoVerifyBody = `{"mobile": "+919812345678", "code": "482913"}`
)

// perOpSources is backend/config with the multiop overlay merged in.
func perOpSources(t testing.TB) registry.Sources {
	t.Helper()
	cfg := fstest.MapFS{}
	mergeInto(t, cfg, os.DirFS("../../config"))
	mergeOverlay(t, cfg, multiop.Config())
	return registry.Sources{Config: cfg}
}

// perOpSnapshot builds the overlay (goldens included) and lets a test tune int_two_step's
// active version on a copy.
func perOpSnapshot(t testing.TB, tune func(v *registry.IntegrationVersion)) *registry.Snapshot {
	t.Helper()
	built, err := registry.Build(perOpSources(t), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var st registry.Store
	snap := st.Swap(built)
	if tune != nil {
		in := *snap.Integrations[multiop.Integration]
		v := *in.Active()
		tune(&v)
		in.Versions = map[int]*registry.IntegrationVersion{in.ActiveVersion: &v}
		snap.Integrations = maps.Clone(snap.Integrations)
		snap.Integrations[multiop.Integration] = &in
	}
	return snap
}

func vendorOK() step { return step{status: 200, body: map[string]any{"id": "sv_ref_1", "valid": true}} }

// resolvedTransformers is the transformers meta of the request's first integration.resolved.
func resolvedTransformers(t *testing.T, b audit.Batch) map[string]string {
	t.Helper()
	for _, e := range b.Events {
		if e.Type == audit.IntegrationResolved {
			m := map[string]string{}
			for k, v := range e.Meta["transformers"].(map[string]any) {
				if s, ok := v.(string); ok {
					m[k] = s
				}
			}
			return m
		}
	}
	t.Fatal("no integration.resolved event")
	return nil
}

func TestPerOperationSyncSelectsThePair(t *testing.T) {
	tests := []struct {
		name, path, body       string
		wantPath, wantStatus   string
		wantRequest, wantReply string
	}{
		{"send uses the default pair", twoStepSend, twoSendBody, "/v1/codes", "sent", "TS-SEND-REQ@1", "TS-SEND-RES@1"},
		{"verify uses its own pair", twoStepVerify, twoVerifyBody, "/v1/codes/verify", "verified", "TS-VERIFY-REQ@1", "TS-VERIFY-RES@1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := &fakeUpstream{steps: []step{vendorOK()}}
			h, sink := invokeServer(t, perOpSnapshot(t, nil), up)
			rec := post(t, h, devKey, tt.path, tt.body, "Idempotency-Key", "k-"+tt.wantStatus)
			var out map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			if rec.Code != 200 || out["status"] != tt.wantStatus {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if up.calls() != 1 || up.reqs[0].Path != tt.wantPath {
				t.Fatalf("vendor calls %+v", up.reqs)
			}
			if m := resolvedTransformers(t, sink.only(t)); m["request"] != tt.wantRequest || m["response"] != tt.wantReply {
				t.Fatalf("integration.resolved transformers %v", m)
			}
		})
	}
}

// TestPerOperationFailoverSelectsThePair: the failover target runs the invoked operation's pair.
func TestPerOperationFailoverSelectsThePair(t *testing.T) {
	up := &fakeUpstream{steps: []step{{status: 503, body: map[string]any{"error": "down"}}, vendorOK()}}
	h, _ := invokeServer(t, perOpSnapshot(t, nil), up)
	rec := post(t, h, devKey, twoStepVerify, twoVerifyBody, "Idempotency-Key", "k-failover")
	if rec.Code != 200 || rec.Header().Get("X-Hub-Served-By") != "failover:1" || !strings.Contains(rec.Body.String(), `"verified"`) {
		t.Fatalf("%d %s %s", rec.Code, rec.Header().Get("X-Hub-Served-By"), rec.Body)
	}
	if up.calls() != 2 {
		t.Fatalf("%d vendor calls, want 2", up.calls())
	}
	for i, client := range []string{"SV-PRIMARY", "SV-BACKUP"} {
		if r := up.reqs[i]; r.Path != "/v1/codes/verify" || r.Headers["X-Client-Id"] != client {
			t.Fatalf("call %d: %s %v", i, r.Path, r.Headers)
		}
	}
}

func TestPerOperationDryRunSelectsThePair(t *testing.T) {
	h := New(Deps{Snapshots: staticSnapshots{perOpSnapshot(t, nil)}, Creds: credsFunc}).Handler()
	for op, c := range map[string]struct{ body, suffix, field string }{
		"send":   {twoSendBody, "/v1/codes", "channel"},
		"verify": {twoVerifyBody, "/v1/codes/verify", "code"},
	} {
		rec := post(t, h, devKey, "/v1/integrations/int_two_step/dry-run?operation="+op, c.body)
		var out dryRunResult
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		body, _ := out.UpstreamRequest.Body.(map[string]any)
		if rec.Code != 200 || out.Operation != op || !strings.HasSuffix(out.UpstreamRequest.URL, c.suffix) || body[c.field] == nil {
			t.Fatalf("%s: %d %s", op, rec.Code, rec.Body)
		}
	}
}

// TestPerOperationShadowSelectsThePair: the shadow copy of a verify call runs verify's pair on
// the shadow target.
func TestPerOperationShadowSelectsThePair(t *testing.T) {
	snap := perOpSnapshot(t, func(v *registry.IntegrationVersion) {
		v.Failover, v.Delivery.OnCircuitOpen = nil, registry.FailFast
		v.Shadow = &registry.Shadow{Mode: registry.ShadowSend, Target: v.Targets[0]}
		v.Shadow.Target.Account, v.Shadow.Target.Weight = "sv-backup", 0
	})
	up := &fakeUpstream{steps: []step{vendorOK()}}
	log := slog.New(slog.DiscardHandler)
	sink := &recordingSink{}
	shadow := delivery.NewShadower(&delivery.Attempter{Creds: credsFunc, Upstream: up, Log: log}, sink, log, nil)
	h := New(Deps{Snapshots: staticSnapshots{snap}, Creds: credsFunc, Upstream: up, Audit: sink, Log: log, Shadow: shadow}).Handler()
	rec := post(t, h, devKey, twoStepVerify, twoVerifyBody, "Idempotency-Key", "k-shadow")
	shadow.Wait()
	if rec.Code != 200 || up.calls() != 2 {
		t.Fatalf("%d %s, %d vendor calls", rec.Code, rec.Body, up.calls())
	}
	clients := map[string]string{}
	for _, r := range up.reqs {
		clients[r.Headers["X-Client-Id"]] = r.Path
	}
	if clients["SV-PRIMARY"] != "/v1/codes/verify" || clients["SV-BACKUP"] != "/v1/codes/verify" {
		t.Fatalf("primary and shadow calls %v", clients)
	}
}
