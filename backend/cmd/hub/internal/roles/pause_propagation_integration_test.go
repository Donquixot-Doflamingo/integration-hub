//go:build integration

package roles

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const trackBody = `{"user": {"id": "u_48213"}, "event": {"name": "order_placed", "timestamp": "2026-10-31T09:15:00Z", "properties": {"order_id": "ord_8821"}}}`

func send(t *testing.T, method, url, body string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// pausedOn reads the caller discovery view of int_evt_brandA on a gateway.
func pausedOn(t *testing.T, gateway string) bool {
	t.Helper()
	code, body := get(t, "http://"+gateway+"/v1/integrations/int_evt_brandA", map[string]string{"Authorization": "Bearer " + devAPIKey})
	if code != 200 {
		t.Fatalf("discovery on %s: %d %s", gateway, code, body)
	}
	var d struct {
		Paused bool `json:"paused"`
	}
	_ = json.Unmarshal([]byte(body), &d)
	return d.Paused
}

// TestPauseReachesThreeNodes is HUB-205's acceptance: POST /admin/v1/integrations/{id}/pause on
// `hub admin` takes effect on three gateway nodes following the archive within 10 s (invokes
// answer 503 INTEGRATION_PAUSED), and resume undoes it the same way.
func TestPauseReachesThreeNodes(t *testing.T) {
	freshArchive(t)
	blobURL := "file://" + t.TempDir()

	admin := composeConfig(t, Admin)
	admin.BlobURL = blobURL
	admin.AdminDevOIDC = true
	adminAddrs := startRoleWith(t, admin, Options{})
	base := "http://" + adminAddrs["http"]

	gateways := make([]string, 3)
	for i := range gateways {
		cfg := composeConfig(t, Gateway)
		cfg.BlobURL = blobURL
		gateways[i] = startRoleWith(t, cfg, Options{})["http"]
	}
	for _, g := range gateways {
		if pausedOn(t, g) {
			t.Fatalf("gateway %s starts paused", g)
		}
	}

	code, body := send(t, http.MethodPost, base+"/oidc/dev-token", `{"sub":"oncall","email":"oncall@example.com","roles":["editor"]}`, nil)
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(body), &tok); err != nil || code != 200 {
		t.Fatalf("dev token: %d %s", code, body)
	}
	auth := map[string]string{"Authorization": "Bearer " + tok.AccessToken, "Content-Type": "application/json"}

	for _, step := range []struct {
		action string
		paused bool
	}{{"pause", true}, {"resume", false}} {
		start := time.Now()
		code, body := send(t, http.MethodPost, base+"/admin/v1/integrations/int_evt_brandA/"+step.action, `{"reason":"vendor incident drill"}`, auth)
		if code != 200 || !strings.Contains(body, `"change_id":"chg_`) {
			t.Fatalf("%s: %d %s", step.action, code, body)
		}
		for i, g := range gateways {
			deadline := start.Add(10 * time.Second)
			for pausedOn(t, g) != step.paused {
				if time.Now().After(deadline) {
					t.Fatalf("%s: gateway %d not updated 10 s after the admin call", step.action, i)
				}
				time.Sleep(50 * time.Millisecond)
			}
			if step.paused {
				code, body := send(t, http.MethodPost, "http://"+g+"/v1/integrations/int_evt_brandA/invoke", trackBody,
					map[string]string{"Authorization": "Bearer " + devAPIKey, "Content-Type": "application/json"})
				if code != http.StatusServiceUnavailable || !strings.Contains(body, "INTEGRATION_PAUSED") {
					t.Fatalf("invoke on paused gateway %d: %d %s", i, code, body)
				}
			}
		}
		t.Logf("%s reached 3 gateways in %s", step.action, time.Since(start).Round(time.Millisecond))
	}
}
