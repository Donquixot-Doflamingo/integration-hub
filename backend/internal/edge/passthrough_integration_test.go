//go:build integration

package edge

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
)

// withoutResponseTransformer strips int_evt_brandA's response transformers: every call of it is
// a pass-through (ADR-0023). int_evt_brandA is a wait-mode integration.
func withoutResponseTransformer(snap *registry.Snapshot) *registry.Snapshot {
	cp := *snap
	cp.Integrations = map[string]*registry.Integration{}
	for k, v := range snap.Integrations {
		c := *v
		if k == "int_evt_brandA" {
			c.Versions = map[int]*registry.IntegrationVersion{}
			for n, iv := range v.Versions {
				ivc := *iv
				ivc.Targets = append([]registry.Target(nil), iv.Targets...)
				for i := range ivc.Targets {
					ivc.Targets[i].Transformer.Response = registry.TransformerRef{}
				}
				c.Versions[n] = &ivc
			}
		}
		cp.Integrations[k] = &c
	}
	return &cp
}

// TestE2EPassThrough: the caller gets the vendor's status and body unchanged, only Content-Type
// and Retry-After of the vendor's headers, the Hub's headers and X-Hub-Pass-Through; retries and
// their classification are as with a response transformer. Responses are contract-validated
// (the default PassThrough response).
func TestE2EPassThrough(t *testing.T) {
	e := startE2EWith(t, withoutResponseTransformer)
	vendorHeaders := map[string]string{"X-CleverTap-Region": "in1", "Server": "clevertap-edge", "X-Request-Id": "ct-123"}
	html := "<html><body>maintenance</body></html>"
	cases := []struct {
		name        string
		behaviour   mockvendor.Behaviour
		status      int
		body        string
		contentType string
		retryAfter  string
		calls       int
	}{
		{"2xx JSON unchanged", mockvendor.Behaviour{Status: 200, Body: raw(`{"status":"success","processed":1,"unprocessed":[]}`), Headers: vendorHeaders},
			200, `{"status":"success","processed":1,"unprocessed":[]}`, "application/json", "", 1},
		{"other 2xx keeps its status", mockvendor.Behaviour{Status: 201, Body: raw(`{"id":"ev_1"}`), Headers: vendorHeaders},
			201, `{"id":"ev_1"}`, "application/json", "", 1},
		{"non-retryable 4xx returned at once", mockvendor.Behaviour{Status: 400, Body: raw(`{"status":"fail","error":"Invalid event name"}`), Headers: vendorHeaders},
			400, `{"status":"fail","error":"Invalid event name"}`, "application/json", "", 1},
		{"5xx non-JSON unchanged", mockvendor.Behaviour{Status: 503, BodyRaw: &html, Headers: map[string]string{"Content-Type": "text/html", "Retry-After": "0", "Server": "clevertap-edge"}},
			503, html, "text/html", "0", 1}, // sync: 5xx is not retried (as with a response transformer)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route(e, t, tc.behaviour)
			rec := post(t, e.h, devKey, "/v1/integrations/int_evt_brandA/invoke", trackBody, "Idempotency-Key", "pt-"+strings.ReplaceAll(tc.name, " ", "-"))
			if rec.Code != tc.status || rec.Body.String() != tc.body {
				t.Fatalf("got %d %q, want %d %q", rec.Code, rec.Body.String(), tc.status, tc.body)
			}
			h := rec.Header()
			if h.Get("X-Hub-Pass-Through") != "true" || h.Get("X-Hub-Request-Id") == "" || !strings.HasPrefix(h.Get("Content-Type"), tc.contentType) || h.Get("Retry-After") != tc.retryAfter {
				t.Fatalf("headers %v", h)
			}
			for name := range vendorHeaders {
				if h.Get(name) != "" {
					t.Errorf("vendor header %s reached the caller", name)
				}
			}
			if n := len(e.mv.Requests(t, nil)); n != tc.calls {
				t.Errorf("%d vendor calls, want %d", n, tc.calls)
			}
		})
	}
	// Validation still answers with the canonical envelope (no vendor was called).
	if rec := post(t, e.h, devKey, "/v1/integrations/int_evt_brandA/invoke", `{"user":{}}`); rec.Code != http.StatusBadRequest || rec.Header().Get("X-Hub-Pass-Through") != "" {
		t.Fatalf("validation: %d %v", rec.Code, rec.Header())
	}
}
