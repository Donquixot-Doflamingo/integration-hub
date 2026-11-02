//go:build integration

package roles

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestAdminServesDevOIDCAndMe is HUB-200 end to end: `hub admin` with the dev issuer stub mints
// a token over HTTP, /admin/v1/me answers with it, and a request without one is 401.
func TestAdminServesDevOIDCAndMe(t *testing.T) {
	freshArchive(t)
	cfg := composeConfig(t, Admin)
	cfg.AdminDevOIDC = true
	addrs := startRoleWith(t, cfg, Options{})
	base := "http://" + addrs["http"]
	if code, body := get(t, base+"/admin/v1/me", nil); code != 401 || !strings.Contains(body, "UNAUTHORIZED") {
		t.Fatalf("no token: %d %s", code, body)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/oidc/dev-token", strings.NewReader(`{"sub":"ops-1","email":"ops@example.com","roles":["viewer"]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	_ = resp.Body.Close()
	code, body := get(t, base+"/admin/v1/me", map[string]string{"Authorization": "Bearer " + tok.AccessToken})
	if code != 200 || !strings.Contains(body, `"subject":"ops-1"`) || !strings.Contains(body, `"roles":["viewer"]`) {
		t.Fatalf("me: %d %s", code, body)
	}
	if code, _ := get(t, base+"/.well-known/openid-configuration", nil); code != 200 {
		t.Fatalf("discovery %d", code)
	}
}

// TestAdminWithoutIssuerFailsClosed: no issuer and no stub → every admin request is 401 and the
// stub's paths do not exist.
func TestAdminWithoutIssuerFailsClosed(t *testing.T) {
	freshArchive(t)
	addrs := startRoleWith(t, composeConfig(t, Admin), Options{})
	base := "http://" + addrs["http"]
	if code, _ := get(t, base+"/admin/v1/me", map[string]string{"Authorization": "Bearer x.y.z"}); code != 401 {
		t.Fatalf("me without an issuer: %d", code)
	}
	if code, _ := get(t, base+"/.well-known/openid-configuration", nil); code != 404 {
		t.Fatalf("stub served without HUB_ADMIN_DEV_OIDC: %d", code)
	}
}
