package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

func testSealer(t *testing.T, master string) *Sealer {
	t.Helper()
	keys, err := NewLocalKeys([]byte(master))
	if err != nil {
		t.Fatal(err)
	}
	return NewSealer(keys)
}

const masterA = "seal-master-key-0123456789abcdef"

func TestSealRoundTripAndBinding(t *testing.T) {
	s := testSealer(t, masterA)
	plain := []byte(`{"pan":"ABCDE1234F"}`)
	blob, err := s.Seal("orders-service", "req_1", 3, plain, false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("ABCDE1234F")) || SealedTruncated(blob) {
		t.Fatalf("blob %q", blob)
	}
	got, truncated, err := s.Open("orders-service", "req_1", 3, blob)
	if err != nil || !bytes.Equal(got, plain) || truncated {
		t.Fatalf("open %q %v %v", got, truncated, err)
	}
	other := testSealer(t, "another-master-key-0123456789abc")
	flipped := bytes.Clone(blob)
	flipped[len(flipped)-1] ^= 1
	header := bytes.Clone(blob)
	header[3] = flagTruncated // flags are authenticated
	cases := map[string]func() ([]byte, bool, error){
		"other tenant":  func() ([]byte, bool, error) { return s.Open("billing-service", "req_1", 3, blob) },
		"other request": func() ([]byte, bool, error) { return s.Open("orders-service", "req_2", 3, blob) },
		"other seq":     func() ([]byte, bool, error) { return s.Open("orders-service", "req_1", 4, blob) },
		"other master":  func() ([]byte, bool, error) { return other.Open("orders-service", "req_1", 3, blob) },
		"tampered":      func() ([]byte, bool, error) { return s.Open("orders-service", "req_1", 3, flipped) },
		"flags":         func() ([]byte, bool, error) { return s.Open("orders-service", "req_1", 3, header) },
		"not a blob":    func() ([]byte, bool, error) { return s.Open("orders-service", "req_1", 3, plain) },
	}
	for name, open := range cases {
		if pt, _, err := open(); !errors.Is(err, ErrUnseal) || pt != nil {
			t.Errorf("%s: %q %v", name, pt, err)
		}
	}
	// Per-tenant keys differ: the same plaintext under two tenants never shares a key.
	ka, _ := (&LocalKeys{master: []byte(masterA)}).Key("orders-service", LocalKeyID)
	kb, _ := (&LocalKeys{master: []byte(masterA)}).Key("billing-service", LocalKeyID)
	if bytes.Equal(ka, kb) {
		t.Fatal("tenant keys must differ")
	}
	if _, err := NewLocalKeys([]byte("short")); err == nil {
		t.Fatal("a short master key must be refused")
	}
}

// TestTrailSeals is HUB-208: under `sealed` every payload is ciphertext, the hash and size are
// of the original bytes, the plaintext opens back byte for byte, secrets are removed before
// sealing and Stored never hands out the ciphertext.
func TestTrailSeals(t *testing.T) {
	s := testSealer(t, masterA)
	tr := NewTrail(TrailOptions{RequestID: "req_x", Tenant: "orders-service", Policy: registry.PayloadSealed, Sealer: s,
		Secrets: []string{"fake-pass-SEAL"}})
	tr.Add(RequestReceived, 0, []byte(received), nil)
	withSecret := `{"h":"fake-pass-SEAL","pan":"ABCDE1234F"}`
	tr.Add(RequestTransformed, 0, []byte(withSecret), nil)
	tr.Add(VendorResponded, 1, []byte(response), nil)
	big := []byte(strings.Repeat("x", MaxInlinePayload+10))
	tr.Add(ResponseTransformed, 1, big, nil)
	ev := tr.Events()
	for _, e := range ev {
		if e.PayloadState != PayloadSealed {
			t.Fatalf("%s: state %s", e.Type, e.PayloadState)
		}
		for _, p := range append(personal, "ABCDE1234F", "fake-pass-SEAL") {
			if bytes.Contains(e.Payload, []byte(p)) {
				t.Fatalf("%s: %q in the stored bytes", e.Type, p)
			}
		}
	}
	got, _, err := s.Open("orders-service", "req_x", ev[0].Seq, ev[0].Payload)
	sum := sha256.Sum256(got)
	if err != nil || string(got) != received || ev[0].SHA256 != "sha256:"+hex.EncodeToString(sum[:]) || ev[0].Bytes != len(got) {
		t.Fatalf("round trip %q %v %s/%d", got, err, ev[0].SHA256, ev[0].Bytes)
	}
	if got, _, _ := s.Open("orders-service", "req_x", ev[1].Seq, ev[1].Payload); string(got) != `{"h":"***","pan":"ABCDE1234F"}` || ev[1].SHA256 != sha(withSecret) {
		t.Fatalf("secrets: %q", got)
	}
	got, truncated, err := s.Open("orders-service", "req_x", ev[3].Seq, ev[3].Payload)
	if err != nil || !truncated || len(got) != MaxInlinePayload || ev[3].Bytes != len(big) || !SealedTruncated(ev[3].Payload) {
		t.Fatalf("truncated: %d %v %v", len(got), truncated, err)
	}
	if tr.Stored(VendorResponded) != nil {
		t.Fatal("Stored must not return a sealed payload")
	}
}
