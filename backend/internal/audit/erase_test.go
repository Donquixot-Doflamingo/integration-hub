package audit

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// TestSubjectFormsFindMaskedPayloads proves the erasure's search forms match what the masked
// policy actually stores for each x-pii kind, at canonical and vendor stages: the kinds whose
// mask derives from the whole value are found by their masked form, and no masked payload ever
// keeps the raw identifier.
func TestSubjectFormsFindMaskedPayloads(t *testing.T) {
	t.Parallel()
	m := NewMasker([]byte("test-master-key-0123456789abcdef"))
	cases := []struct {
		name     string
		kind     string
		subject  string
		findable bool // the masked payloads keep a form derived from the whole value
	}{
		{name: "identifier is found by its HMAC", kind: "identifier", subject: "ABCDE1234F", findable: true},
		{name: "device id is found by its HMAC", kind: "device_id", subject: "dev-7781-xa", findable: true},
		{name: "email is found by local-part HMAC and domain", kind: "email", subject: "asha@example.com", findable: true},
		{name: "phone keeps only the last four", kind: "phone", subject: "9876543210"},
		{name: "government id keeps only the last four", kind: "government_id", subject: "ABCDE1234F"},
		{name: "name is fully masked", kind: "name", subject: "Asha Rao"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			canonicalBody := map[string]any{"customer": map[string]any{"id": tc.subject}, "amount": 5.0}
			raw, _ := json.Marshal(canonicalBody)
			vendorBody, _ := json.Marshal(map[string]any{"req": map[string]any{"ref": tc.subject, "note": "for " + tc.subject}})
			tr := NewTrail(TrailOptions{RequestID: "req_x", Tenant: "lending", Policy: registry.PayloadMasked, Masker: m,
				PII: []registry.PIIPath{{Pointer: "/customer/id", Kind: tc.kind}}, Canonical: canonicalBody})
			tr.Add(RequestReceived, 0, raw, nil)
			tr.Add(RequestTransformed, 0, vendorBody, nil)
			forms := m.SubjectForms("lending", tc.subject)
			for _, e := range tr.Events() {
				if bytes.Contains(e.Payload, []byte(tc.subject)) {
					t.Fatalf("%s stores the raw identifier: %s", e.Type, e.Payload)
				}
				if got := containsAny(e.Payload, forms); got != tc.findable {
					t.Fatalf("%s: found=%v, want %v (payload %s)", e.Type, got, tc.findable, e.Payload)
				}
			}
			if !containsAny(raw, forms) { // full payloads, envelopes, sealed plaintext: the raw form
				t.Fatal("the raw form is missing")
			}
			if other := m.SubjectForms("other-tenant", tc.subject); tc.findable && bytes.Equal(other[1], forms[1]) {
				t.Fatal("masked forms must differ across tenants")
			}
		})
	}
}

func TestSubjectFormsEscapedAndRef(t *testing.T) {
	t.Parallel()
	m := NewMasker([]byte("test-master-key-0123456789abcdef"))
	forms := m.SubjectForms("lending", `a"b<c>`)
	for _, want := range []string{`a"b<c>`, `a\"b<c>`, `a\"b<c>`} {
		if !containsAny([]byte(want), forms) {
			t.Errorf("missing form %s", want)
		}
	}
	ref := m.SubjectRef("lending", "ABCDE1234F")
	if ref != m.SubjectRef("lending", "ABCDE1234F") || ref == m.SubjectRef("other", "ABCDE1234F") {
		t.Fatal("the subject ref must be stable per tenant and differ across tenants")
	}
	if containsAny([]byte(ref), m.SubjectForms("lending", "ABCDE1234F")) {
		t.Fatal("the change-log ref must not equal any stored form of the identifier")
	}
}
