package edge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
)

// fakeCreds are obviously fake test values, unique enough to grep for.
var fakeCreds = map[string]string{"account_id": "fake-acct-7Q2X", "passcode": "fake-pass-9KZW"}

func credsFunc(ctx context.Context, account string) (map[string]string, error) {
	if account == "" {
		return nil, errors.New("no account")
	}
	return fakeCreds, nil
}

func post(t *testing.T, h http.Handler, key, target, body string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	spec := contract.Load(t, "hub-public.v1.yaml")
	if strings.Contains(body, "not json") {
		return spec.ServeInvalid(t, h, req)
	}
	return spec.Serve(t, h, req)
}

const trackBody = `{"user": {"id": "u_48213"}, "event": {"name": "order_placed", "timestamp": "2026-10-31T09:15:00Z", "properties": {"order_id": "ord_8821"}}}`

func TestDryRunShowsMaskedUpstreamRequest(t *testing.T) {
	h := New(Deps{Snapshots: staticSnapshots{devSnapshot(t)}, Creds: credsFunc}).Handler()
	rec := post(t, h, devKey, "/v1/integrations/int_evt_brandA/dry-run", trackBody)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, secret := range fakeCreds {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("secret %q in dry-run output", secret)
		}
	}
	var out dryRunResult
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	u := out.UpstreamRequest
	if u.Method != "POST" || !strings.HasSuffix(u.URL, "/1/upload") || u.Headers["X-CleverTap-Passcode"] != "***" {
		t.Fatalf("upstream %+v", u)
	}
	if !slices.Equal(out.Redacted, []string{"headers.X-CleverTap-Account-Id", "headers.X-CleverTap-Passcode"}) {
		t.Fatalf("redacted %v", out.Redacted)
	}
	d := u.Body.(map[string]any)["d"].([]any)[0].(map[string]any)
	if d["identity"] != "u_48213" || d["ts"] != 1793438100.0 {
		t.Fatalf("body %v", d)
	}
	if out.RequestID != rec.Header().Get("X-Hub-Request-Id") || !out.DryRun || out.Operation != "default" {
		t.Fatalf("%+v", out)
	}
}

func TestDryRunErrors(t *testing.T) {
	h := New(Deps{Snapshots: staticSnapshots{discoverySnapshot(t)}, Creds: credsFunc}).Handler()
	cases := []struct {
		name, key, target, body string
		hdr                     []string
		status                  int
		want                    string
	}{
		{"missing scope", growthKey, "/v1/integrations/int_evt_brandB/dry-run", trackBody, nil, 403, `"reason":"missing_scope"`},
		{"not granted", growthKey, "/v1/integrations/int_evt_brandA/dry-run", trackBody, nil, 403, `"reason":"not_granted"`},
		{"unknown integration", devKey, "/v1/integrations/int_nope/dry-run", trackBody, nil, 404, `"INTEGRATION_NOT_FOUND"`},
		{"unknown operation", devKey, "/v1/integrations/int_evt_brandA/dry-run?operation=verify", trackBody, nil, 404, `"OPERATION_NOT_FOUND"`},
		{"unknown session", devKey, "/v1/integrations/int_evt_brandA/dry-run", trackBody, []string{"X-Hub-Session", "ses_01J9Z3K8X2V7Q4M6N8P0R2S4T6"}, 404, `"SESSION_NOT_FOUND"`},
		{"schema", devKey, "/v1/integrations/int_evt_brandA/dry-run", `{"user": {}}`, nil, 400, `"pointer":"/user"`},
		{"not json", devKey, "/v1/integrations/int_evt_brandA/dry-run", `not json`, nil, 400, `"VALIDATION_FAILED"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(t, h, tc.key, tc.target, tc.body, tc.hdr...)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestDryRunCredentialMissIsInternal(t *testing.T) {
	failing := func(context.Context, string) (map[string]string, error) { return nil, errors.New("cache miss") }
	h := New(Deps{Snapshots: staticSnapshots{devSnapshot(t)}, Creds: failing}).Handler()
	if rec := post(t, h, devKey, "/v1/integrations/int_evt_brandA/dry-run", trackBody); rec.Code != 500 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
