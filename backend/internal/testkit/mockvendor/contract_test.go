package mockvendor

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
)

// TestControlAPIMatchesContract drives every control operation against a running mock and
// validates each request and response against mockvendor.v1.yaml (HUB-028 acceptance).
func TestControlAPIMatchesContract(t *testing.T) {
	spec := contract.Load(t, "mockvendor.v1.yaml")
	mv := Start(t)
	receiverSrv, _ := receiver(t, http.StatusOK)

	send := func(invalid bool, method, path string, body any, want int) []byte {
		t.Helper()
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req, _ := http.NewRequestWithContext(t.Context(), method, mv.ControlURL+path, rd)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		do := spec.Do
		if invalid {
			do = spec.DoInvalid
		}
		code, b := do(t, req)
		if code != want {
			t.Errorf("%s %s: status %d, want %d: %s", method, path, code, want, b)
		}
		return b
	}
	do := func(method, path string, body any, want int) []byte {
		t.Helper()
		return send(false, method, path, body, want)
	}
	bad := func(method, path string, body any, want int) []byte {
		t.Helper()
		return send(true, method, path, body, want)
	}

	seed := int64(1)
	times := 3
	do("PUT", "/__control/routes/ct-upload", RouteSpec{
		Match: RouteMatch{Method: "POST", Path: "/1/upload"}, Times: &times,
		Behaviour: Behaviour{Status: 200, Headers: map[string]string{"X-Request-Id": "mock-123"}, Body: okBody,
			Latency: &Latency{Mode: "fixed", FixedMS: 1}, ErrorRatio: &ErrorRatio{Ratio: 0, Status: 503, Seed: &seed}},
	}, 200)
	bad("PUT", "/__control/routes/bad", map[string]any{"match": map[string]any{"method": "FETCH", "path": "/"}, "behaviour": map[string]any{}}, 400)
	do("GET", "/__control/routes", nil, 200)
	do("GET", "/__control/routes/ct-upload", nil, 200)
	do("GET", "/__control/routes/missing", nil, 404)
	do("PUT", "/__control/default", Behaviour{Status: 404, Body: json.RawMessage(`{"error":"no such route"}`)}, 200)

	call(t, mv, "POST", "/1/upload", `{"d":[{"identity":"u_1"}]}`, nil)
	call(t, mv, "POST", "/raw", "not json", nil)
	do("GET", "/__control/requests?limit=1", nil, 200)
	do("GET", "/__control/requests?route_id=ct-upload&method=POST&path=/1/*", nil, 200)
	bad("GET", "/__control/requests?limit=0", nil, 400)

	do("POST", "/__control/webhooks", WebhookTrigger{TargetURL: receiverSrv.URL, Body: json.RawMessage(`{"event":"delivered"}`),
		Signature: &Signature{Scheme: "hmac_sha256", Secret: "test-webhook-secret", Header: "X-Sig"}}, 200)
	do("POST", "/__control/webhooks", WebhookTrigger{TargetURL: "http://127.0.0.1:1/x", Body: json.RawMessage(`{}`)}, 200)
	bad("POST", "/__control/webhooks", WebhookTrigger{TargetURL: "nope"}, 400)

	do("GET", "/__control/healthz", nil, 200)
	do("DELETE", "/__control/requests", nil, 204)
	do("DELETE", "/__control/routes/ct-upload", nil, 204)
	do("DELETE", "/__control/routes/ct-upload", nil, 404)
	do("POST", "/__control/reset", ResetRequest{}, 200)
	do("POST", "/__control/reset", nil, 200)
}
