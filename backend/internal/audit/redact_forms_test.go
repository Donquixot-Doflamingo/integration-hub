package audit

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// HUB-448: a credential with JSON- or URL-special characters must not reach stored payloads in
// any encoded form.
func TestTrailRedactsEncodedSecrets(t *testing.T) {
	secrets := []string{`pa&ss<w>"rd\1`, `päss wörd`, `a+b/c=d`, `tok%en`, `plainSecret42`}
	for _, secret := range secrets {
		for _, policy := range []registry.PayloadPolicy{registry.PayloadFull, registry.PayloadMasked} {
			t.Run(string(policy)+"/"+secret, func(t *testing.T) {
				hdr, _ := json.Marshal(map[string]string{"Authorization": "Bearer " + secret, "X-Api-Key": secret})
				body, _ := json.Marshal(map[string]any{"token": secret, "note": "plain text stays", "n": 5})
				vendorReq := `{"url":"https://v.example/p/` + url.PathEscape(secret) + `?k=` + url.QueryEscape(secret) +
					`&x=1","headers":` + string(hdr) + `,"body":` + string(body) + `}`
				resp := `{"echo":` + string(mustJSON(t, secret)) + `,"status":"ok"}`

				tr := NewTrail(TrailOptions{RequestID: "req_x", Tenant: "t", Policy: policy, Secrets: []string{secret}})
				tr.Add(RequestTransformed, 0, []byte(vendorReq), nil)
				tr.Add(VendorResponded, 1, []byte(resp), nil)
				for _, typ := range []string{RequestTransformed, VendorResponded} {
					got := string(tr.Stored(typ)) // also what the DLQ keeps as last_vendor_request / last_vendor_response
					for _, f := range independentForms(secret) {
						if strings.Contains(got, f) {
							t.Errorf("%s stores secret form %q: %s", typ, f, got)
						}
					}
					if !strings.Contains(got, Redacted) {
						t.Errorf("%s has no redaction marker: %s", typ, got)
					}
				}
				if got := string(tr.Stored(RequestTransformed)); !strings.Contains(got, "plain text stays") || !strings.Contains(got, `"n":5`) {
					t.Errorf("non-secret text was changed: %s", got)
				}
			})
		}
	}
}

// independentForms builds the encoded forms with the standard library only, so the test does
// not trust the production derivation to say what must be masked.
func independentForms(secret string) []string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	_ = enc.Encode(secret)
	withHTML := strings.TrimSuffix(buf.String(), "\n")
	buf.Reset()
	enc.SetEscapeHTML(false)
	_ = enc.Encode(secret)
	noHTML := strings.TrimSuffix(buf.String(), "\n")
	return []string{secret, withHTML[1 : len(withHTML)-1], noHTML[1 : len(noHTML)-1],
		url.QueryEscape(secret), url.PathEscape(secret),
		base64.StdEncoding.EncodeToString([]byte(secret)), base64.URLEncoding.EncodeToString([]byte(secret))}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTrailRedactsLongerFormsFirst(t *testing.T) {
	// A secret that is a prefix of another must not leave the tail of the longer one behind.
	tr := NewTrail(TrailOptions{RequestID: "r", Policy: registry.PayloadFull, Secrets: []string{"abcdef", "abcdef-tail-123"}})
	tr.Add(RequestTransformed, 0, []byte(`{"v":"abcdef-tail-123"}`), nil)
	if got := string(tr.Stored(RequestTransformed)); got != `{"v":"***"}` {
		t.Errorf("got %s", got)
	}
}

func TestTrailLeavesOrdinaryDataAlone(t *testing.T) {
	tr := NewTrail(TrailOptions{RequestID: "r", Policy: registry.PayloadFull, Secrets: []string{"ab"}}) // too short to derive forms
	tr.Add(RequestTransformed, 0, []byte(`{"v":"YWI= QWI="}`), nil)
	if got := string(tr.Stored(RequestTransformed)); got != `{"v":"YWI= QWI="}` {
		t.Errorf("short secret corrupted data: %s", got)
	}
}
