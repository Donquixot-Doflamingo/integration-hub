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
