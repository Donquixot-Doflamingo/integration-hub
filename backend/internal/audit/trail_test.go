package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

var trackPII = []registry.PIIPath{{Pointer: "/user/id", Kind: "identifier"}, {Pointer: "/event/properties", Kind: "free_text"}}

const (
	received = `{"user":{"id":"u_48213"},"event":{"name":"order_placed","properties":{"order_id":"ord_8821","email":"asha@example.com"}}}`
	vendor   = `{"d":[{"identity":"u_48213","type":"event","evtName":"order_placed","evtData":{"order_id":"ord_8821","email":"asha@example.com"}}]}`
	response = `{"status":"success","processed":1,"unprocessed":[],"note":"stored for u_48213"}`
)

// personal values that must never be stored in clear under `masked`.
var personal = []string{"u_48213", "ord_8821", "asha@example.com"}

func canonical(t *testing.T) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(received), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func trail(t *testing.T, policy registry.PayloadPolicy, secrets ...string) *Trail {
	tr := NewTrail(TrailOptions{RequestID: "req_x", Tenant: "orders-service", Policy: policy, PII: trackPII,
		Canonical: canonical(t), Masker: NewMasker([]byte("test-master-key-0123456789abcdef")), Secrets: secrets})
	tr.Add(RequestReceived, 0, []byte(received), map[string]any{"key_hash": "sha256:abc"})
	tr.Add(RequestValidated, 0, []byte(received), nil)
	tr.Add(IntegrationResolved, 0, nil, map[string]any{"version": 2})
	tr.Add(RequestTransformed, 0, []byte(vendor), nil)
	tr.Add(VendorResponded, 1, []byte(response), nil)
	tr.Add(ResponseTransformed, 0, []byte(`{"status":"ok"}`), nil)
	return tr
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestPayloadPolicies(t *testing.T) {
	cases := []struct {
		policy registry.PayloadPolicy
		state  PayloadState
		clear  bool // personal values stored in clear
		noBody bool
	}{
		{registry.PayloadFull, PayloadFull, true, false},
		{registry.PayloadMasked, PayloadMasked, false, false},
		{"", PayloadMasked, false, false}, // default
		{registry.PayloadMetadataOnly, PayloadOmitted, false, true},
		{registry.PayloadSealed, PayloadOmitted, false, true}, // until HUB-208: never in clear
	}
	for _, tc := range cases {
		t.Run(string(tc.policy), func(t *testing.T) {
			tr := trail(t, tc.policy)
			if want := tc.policy; want != "" && tr.Policy() != want {
				t.Fatalf("policy %s", tr.Policy())
			}
			for _, e := range tr.Events() {
				if e.Type == IntegrationResolved {
					if e.SHA256 != "" || e.Payload != nil || e.PayloadState != PayloadNone {
						t.Fatalf("payload-less event %+v", e)
					}
					continue
				}
				if !strings.HasPrefix(e.SHA256, "sha256:") || e.Bytes == 0 {
					t.Fatalf("%s: hash and size always kept: %+v", e.Type, e)
				}
				if e.PayloadState != tc.state || (e.Payload == nil) != tc.noBody {
					t.Fatalf("%s: state %s payload %q", e.Type, e.PayloadState, e.Payload)
				}
				for _, p := range personal {
					if strings.Contains(string(e.Payload), p) != (tc.clear && strings.Contains(map[string]string{
						RequestReceived: received, RequestValidated: received, RequestTransformed: vendor, VendorResponded: response,
					}[e.Type], p)) {
						t.Errorf("%s: %q clear=%v in %s", e.Type, p, tc.clear, e.Payload)
					}
				}
			}
			if rec := tr.Batch(Record{RequestID: "req_x"}).Records[0]; rec.PayloadPolicy != string(tr.Policy()) {
				t.Fatalf("record policy %q", rec.PayloadPolicy)
			}
		})
	}
}

func TestHashIsOfTheBytesSeen(t *testing.T) {
	for _, policy := range []registry.PayloadPolicy{registry.PayloadFull, registry.PayloadMasked, registry.PayloadMetadataOnly} {
		e := trail(t, policy).Events()[0]
		if e.SHA256 != sha(received) || e.Bytes != len(received) {
			t.Fatalf("%s: %s/%d", policy, e.SHA256, e.Bytes)
		}
	}
}

func TestMaskedShapes(t *testing.T) {
	tr := trail(t, registry.PayloadMasked)
	var got map[string]any
	_ = json.Unmarshal(tr.Events()[0].Payload, &got)
	id := got["user"].(map[string]any)["id"].(string)
	props := got["event"].(map[string]any)["properties"].(map[string]any)
	if !strings.HasPrefix(id, "hmac:") || props["masked"] != "free_text" || !strings.HasPrefix(props["sha256"].(string), "sha256:") {
		t.Fatalf("masked canonical %v", got)
	}
	if got["event"].(map[string]any)["name"] != "order_placed" {
		t.Fatal("untagged fields stay as they are")
	}
	// Same subject, same tenant → same mask in every stage (the deletion job keys on it).
	if !strings.Contains(string(tr.Events()[3].Payload), id) {
		t.Fatalf("vendor stage masks the identifier differently: %s", tr.Events()[3].Payload)
	}
	other := NewTrail(TrailOptions{Tenant: "growth-team", PII: trackPII, Canonical: canonical(t), Masker: NewMasker([]byte("test-master-key-0123456789abcdef"))})
	other.Add(RequestReceived, 0, []byte(received), nil)
	if strings.Contains(string(other.Events()[0].Payload), id) {
		t.Fatal("identifier masks must differ across tenants")
	}
}

func TestMaskKinds(t *testing.T) {
	m := NewMasker([]byte("k"))
	for _, tc := range []struct{ kind, in, want string }{
		{"phone", "+919876543210", "***3210"},
		{"government_id", "ABCDE1234F", "***234F"},
		{"financial", "12", "***"},
		{"name", "Asha Rao", "***"},
		{"address", "12 MG Road", "***"},
		{"email", "asha@example.com", "@example.com"},
		{"identifier", "u_1", "hmac:"},
	} {
		got := m.maskString("t", tc.kind, tc.in)
		if !strings.Contains(got, tc.want) || strings.Contains(got, tc.in) {
			t.Errorf("%s(%q) = %q", tc.kind, tc.in, got)
		}
	}
}

func TestSecretsNeverStored(t *testing.T) {
	const secret = "fake-pass-9KZW"
	for _, policy := range []registry.PayloadPolicy{registry.PayloadFull, registry.PayloadMasked} {
		tr := NewTrail(TrailOptions{Policy: policy, Secrets: []string{secret}})
		tr.Add(VendorResponded, 1, []byte(`{"echo":"`+secret+`"}`), nil)
		tr.Add(VendorResponded, 1, []byte(`plain text `+secret), nil)
		for _, e := range tr.Events() {
			if strings.Contains(string(e.Payload), secret) {
				t.Fatalf("%s stored the secret: %s", policy, e.Payload)
			}
		}
	}
}

func TestLargePayloadTruncated(t *testing.T) {
	tr := NewTrail(TrailOptions{Policy: registry.PayloadFull})
	big := []byte(`"` + strings.Repeat("x", MaxInlinePayload+10) + `"`)
	tr.Add(VendorResponded, 1, big, nil)
	e := tr.Events()[0]
	if e.PayloadState != PayloadTruncated || len(e.Payload) != MaxInlinePayload || e.Bytes != len(big) {
		t.Fatalf("state %s stored %d bytes %d", e.PayloadState, len(e.Payload), e.Bytes)
	}
}

func TestMaskingDoesNotModifyTheCanonicalBody(t *testing.T) {
	body := canonical(t)
	tr := NewTrail(TrailOptions{PII: trackPII, Canonical: body, Masker: NewMasker([]byte("k"))})
	b, _ := json.Marshal(body)
	tr.Add(RequestValidated, 0, b, nil)
	if body["user"].(map[string]any)["id"] != "u_48213" {
		t.Fatal("canonical body was modified")
	}
}

func TestAnnotateSetsMetaOnTheLatestEventOfAType(t *testing.T) {
	tr := NewTrail(TrailOptions{RequestID: "req_x"})
	tr.Add(ResponseTransformed, 1, nil, nil)
	tr.Add(ResponseTransformed, 2, nil, map[string]any{"transformer": "R@1"})
	tr.Annotate(ResponseTransformed, "session", map[string]any{"id": "ses_1"})
	tr.Annotate(VendorSent, "ignored", true) // no such event: no-op
	ev := tr.Events()
	if ev[0].Meta != nil || ev[1].Meta["transformer"] != "R@1" || ev[1].Meta["session"].(map[string]any)["id"] != "ses_1" || len(ev) != 2 {
		t.Fatalf("events %+v", ev)
	}
}
