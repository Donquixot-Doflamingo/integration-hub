package contract

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func exchange(t *testing.T, method, path string, status int, body string) Exchange {
	t.Helper()
	h := http.Header{"Content-Type": {"application/json"}}
	return Exchange{Request: httptest.NewRequestWithContext(t.Context(), method, path, nil), Status: status, Header: h, Body: []byte(body)}
}

func TestCheck(t *testing.T) {
	pub := Load(t, "hub-public.v1.yaml")
	tests := []struct {
		name    string
		ex      Exchange
		wantErr []string // substrings; empty means valid
	}{
		{"valid healthz", exchange(t, "GET", "/healthz", 200, `{"status":"ok","role":"gateway","checks":{}}`), nil},
		{"wrong enum value", exchange(t, "GET", "/healthz", 200, `{"status":"fine"}`), []string{"/status", "getHealthz"}},
		{"bad check value", exchange(t, "GET", "/healthz", 200, `{"status":"ok","checks":{"redis":"meh"}}`), []string{"/checks/redis"}},
		{"undeclared status", exchange(t, "GET", "/healthz", 418, `{}`), []string{"response 418"}},
		{"unknown operation", exchange(t, "GET", "/v1/nope", 200, `{}`), []string{"not an operation"}},
		{"readyz 503 missing request_id", withHeader(exchange(t, "GET", "/readyz", 503, `{"error":{"code":"NOT_READY","message":"x","retryable":true}}`), "X-Hub-Request-Id", "req_01J9Z3M1A5B7C9D1E3F5G7H9JN"), []string{"request_id"}},
		{"pass-through vendor 404 JSON", passThrough(t, 404, "application/json", `{"error":"no such user"}`), nil},
		{"pass-through vendor 503 HTML", passThrough(t, 503, "text/html", "<html>down</html>"), nil},
		{"pass-through 200 non-JSON", passThrough(t, 200, "text/plain; charset=utf-8", "age=29"), nil},
		{"vendor body without the pass-through header", withoutHeader(passThrough(t, 404, "application/json", `{"error":"no such user"}`), "X-Hub-Pass-Through"), []string{"response 404"}},
		{"pass-through where none is contracted", withHeader(withHeader(exchange(t, "GET", "/healthz", 503, "down"), "Content-Type", "text/plain"), "X-Hub-Pass-Through", "true"), []string{"not contracted"}},
		{"readyz 503 missing header", exchange(t, "GET", "/readyz", 503, `{"error":{"code":"NOT_READY","message":"x","retryable":true,"request_id":"req_01J9Z3M1A5B7C9D1E3F5G7H9JN"}}`), []string{"X-Hub-Request-Id"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := pub.Check(tt.ex)
			if len(tt.wantErr) == 0 {
				if err != nil {
					t.Fatalf("unexpected violation: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("violation not detected")
			}
			for _, w := range tt.wantErr {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error does not mention %q:\n%v", w, err)
				}
			}
		})
	}
}

func TestAllContractsLoad(t *testing.T) {
	for _, f := range []string{"hub-public.v1.yaml", "hub-admin.v1.yaml", "mockvendor.v1.yaml"} {
		if s := Load(t, f); s.doc.Paths.Len() == 0 {
			t.Errorf("%s has no paths", f)
		}
	}
}

func withHeader(ex Exchange, k, v string) Exchange {
	ex.Header.Set(k, v)
	return ex
}

func TestOperationIDAndExample(t *testing.T) {
	pub := Load(t, "hub-public.v1.yaml")
	adm := Load(t, "hub-admin.v1.yaml")
	tests := []struct {
		spec         *Spec
		method, path string
		want         string
	}{
		{pub, "GET", "/v1/integrations", "listIntegrations"},
		{pub, "POST", "/v1/integrations/int_evt_brandA/invoke/send", "invokeIntegrationOperation"},
		{adm, "GET", "/admin/v1/breakers", "listBreakers"},
	}
	for _, tt := range tests {
		got, err := tt.spec.OperationID(httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil))
		if err != nil || got != tt.want {
			t.Errorf("%s %s: %q, %v; want %q", tt.method, tt.path, got, err, tt.want)
		}
	}
	if _, err := pub.OperationID(httptest.NewRequestWithContext(t.Context(), "GET", "/v1/nope", nil)); err == nil {
		t.Error("unknown path matched an operation")
	}
	for _, ex := range [][]byte{
		pub.Example(t, "listIntegrations", 200, ""),                    // inline example
		pub.Example(t, "getRequestStatus", 200, "parked"),              // inline examples
		pub.Example(t, "invokeIntegration", 503, "INTEGRATION_PAUSED"), // via components.responses
	} {
		if !strings.HasPrefix(string(ex), "{") {
			t.Errorf("example is not a JSON object: %s", ex)
		}
	}
}

// passThrough is an invoke answer passed through from the vendor (ADR-0023).
func passThrough(t *testing.T, status int, contentType, body string) Exchange {
	t.Helper()
	ex := exchange(t, "POST", "/v1/integrations/int_age_estimate/invoke", status, body)
	ex.InvalidRequest = true // only the response matters here
	ex.Header.Set("Content-Type", contentType)
	ex.Header.Set("X-Hub-Request-Id", "req_01J9Z3M1A5B7C9D1E3F5G7H9JN")
	ex.Header.Set("X-Hub-Pass-Through", "true")
	return ex
}

func withoutHeader(ex Exchange, k string) Exchange {
	ex.Header.Del(k)
	return ex
}
