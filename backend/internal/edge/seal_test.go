package edge

import (
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// TestSealedPolicyIsEncryptedAtTheEdge is HUB-208's wiring: an integration with the sealed
// policy has every payload stored as ciphertext under the tenant's key, opening to the exact
// bytes the edge saw (hash and size of the original).
func TestSealedPolicyIsEncryptedAtTheEdge(t *testing.T) {
	snap := devSnapshot(t)
	cp := *snap
	cp.Integrations = map[string]*registry.Integration{}
	for k, v := range snap.Integrations {
		c := *v
		if k == "int_evt_brandA" {
			c.Versions = map[int]*registry.IntegrationVersion{}
			for n, ver := range v.Versions {
				vc := *ver
				vc.Audit.Payloads = registry.PayloadSealed
				c.Versions[n] = &vc
			}
		}
		cp.Integrations[k] = &c
	}
	keys, err := audit.NewLocalKeys([]byte("edge-seal-test-master-key-012345"))
	if err != nil {
		t.Fatal(err)
	}
	sealer := audit.NewSealer(keys)
	sink := &recordingSink{}
	h := New(Deps{Snapshots: staticSnapshots{&cp}, Creds: credsFunc, Audit: sink, Masker: audit.NewMasker([]byte("test-mask-key")), Sealer: sealer}).Handler()
	post(t, h, devKey, "/v1/integrations/int_evt_brandA/dry-run", trackBody)
	b := sink.only(t)
	if b.Records[0].PayloadPolicy != "sealed" {
		t.Fatalf("record policy %s", b.Records[0].PayloadPolicy)
	}
	sealed := 0
	for _, e := range b.Events {
		if e.SHA256 == "" {
			continue
		}
		if e.PayloadState != audit.PayloadSealed || strings.Contains(string(e.Payload), "u_48213") {
			t.Fatalf("%s: %s %q", e.Type, e.PayloadState, e.Payload)
		}
		pt, _, err := sealer.Open(b.Records[0].TenantID, e.RequestID, e.Seq, e.Payload)
		if err != nil {
			t.Fatalf("%s: open %v", e.Type, err)
		}
		if e.Type == audit.RequestReceived && (string(pt) != trackBody || len(pt) != e.Bytes) {
			t.Fatalf("received %q", pt)
		}
		for _, secret := range []string{fakeCreds["account_id"], fakeCreds["passcode"], devKey} {
			if strings.Contains(string(pt), secret) {
				t.Fatalf("%s seals a secret", e.Type)
			}
		}
		sealed++
	}
	if sealed < 2 {
		t.Fatalf("%d sealed events", sealed)
	}
}
