package edge

import (
	"strings"
	"sync"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
)

// recordingSink keeps every emitted batch.
type recordingSink struct {
	mu      sync.Mutex
	batches []audit.Batch
}

func (s *recordingSink) Emit(b audit.Batch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, b)
}

func (s *recordingSink) only(t *testing.T) audit.Batch {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.batches) != 1 {
		t.Fatalf("%d audit batches, want 1", len(s.batches))
	}
	return s.batches[0]
}

func types(b audit.Batch) string {
	var out []string
	for _, e := range b.Events {
		out = append(out, e.Type)
	}
	return strings.Join(out, ",")
}

func TestDryRunIsAudited(t *testing.T) {
	sink := &recordingSink{}
	h := New(Deps{Snapshots: staticSnapshots{devSnapshot(t)}, Creds: credsFunc, Audit: sink, Masker: audit.NewMasker([]byte("test-mask-key"))}).Handler()
	rec := post(t, h, devKey, "/v1/integrations/int_evt_brandA/dry-run", trackBody)
	b := sink.only(t)
	if got := types(b); got != "request.received,request.validated,integration.resolved,request.transformed,request.completed" {
		t.Fatalf("events %s", got)
	}
	r := b.Records[0]
	if !r.DryRun || r.Status != "succeeded" || r.HTTPStatus != 200 || r.PayloadPolicy != "masked" ||
		r.RequestID != rec.Header().Get("X-Hub-Request-Id") || r.IntegrationVersion != 2 || r.SnapshotVersion == 0 {
		t.Fatalf("record %+v", r)
	}
	for i, e := range b.Events {
		if e.Seq != i+1 || e.RequestID != r.RequestID {
			t.Fatalf("event %d: %+v", i, e)
		}
		for _, secret := range append([]string{devKey, "u_48213"}, fakeCreds["account_id"], fakeCreds["passcode"]) {
			if strings.Contains(string(e.Payload), secret) {
				t.Errorf("%s stores %q: %s", e.Type, secret, e.Payload)
			}
		}
	}
	if b.Events[0].Meta["key_hash"] != HashKey(devKey) {
		t.Fatalf("received meta %v", b.Events[0].Meta)
	}
	if b.Events[2].Meta["version"] != 2 || b.Events[2].Meta["account"] != "ct-brandA-in" {
		t.Fatalf("resolved meta %v", b.Events[2].Meta)
	}
}

func TestFailuresAreAuditedAfterAuthorization(t *testing.T) {
	sink := &recordingSink{}
	h := New(Deps{Snapshots: staticSnapshots{devSnapshot(t)}, Creds: credsFunc, Audit: sink}).Handler()
	post(t, h, devKey, "/v1/integrations/int_evt_brandA/dry-run", `{"user": {}}`)
	b := sink.only(t)
	if got := types(b); got != "request.received,request.failed" {
		t.Fatalf("events %s", got)
	}
	if r := b.Records[0]; r.Status != "failed" || r.ErrorCode != "VALIDATION_FAILED" || r.HTTPStatus != 400 {
		t.Fatalf("record %+v", r)
	}
	if b.Events[0].SHA256 == "" || b.Events[0].Bytes != len(`{"user": {}}`) {
		t.Fatalf("received hash %+v", b.Events[0])
	}

	sink.batches = nil
	post(t, h, "hk_wrong", "/v1/integrations/int_evt_brandA/dry-run", trackBody)
	post(t, h, devKey, "/v1/integrations/int_nope/dry-run", trackBody)
	if len(sink.batches) != 0 {
		t.Fatalf("unauthenticated or unauthorized calls audited: %d", len(sink.batches))
	}
}
