//go:build integration

package roles

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

func composeConfig(t *testing.T, role Role) Config {
	t.Helper()
	cfg := testConfig(role)
	cfg.PGDSN = Secret(os.Getenv("HUB_PG_DSN"))
	cfg.RedisAddr = os.Getenv("HUB_REDIS_ADDR")
	if cfg.PGDSN == "" || cfg.RedisAddr == "" {
		t.Fatal("HUB_PG_DSN and HUB_REDIS_ADDR must be set; run via `make itest`")
	}
	return cfg
}

func TestReadyzAgainstComposeServices(t *testing.T) {
	t.Run("postgres and redis up", func(t *testing.T) {
		addrs := startRoleWith(t, composeConfig(t, Gateway), Options{})
		code, body := get(t, "http://"+addrs["ops"]+"/readyz", nil)
		if code != http.StatusOK {
			t.Fatalf("status %d: %s", code, body)
		}
		var h health
		if err := json.Unmarshal([]byte(body), &h); err != nil {
			t.Fatal(err)
		}
		if h.Checks["postgres"] != "ok" || h.Checks["redis"] != "ok" || h.Checks["snapshot"] != "ok" {
			t.Fatalf("checks %v", h.Checks)
		}
	})
	t.Run("redis down", func(t *testing.T) {
		cfg := composeConfig(t, Worker)
		cfg.RedisAddr = "127.0.0.1:1" // nothing listens there
		addrs := startRoleWith(t, cfg, Options{})
		code, body := get(t, "http://"+addrs["ops"]+"/readyz", nil)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("status %d, want 503: %s", code, body)
		}
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Details []struct {
					Message string `json:"message"`
				} `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(body), &env); err != nil {
			t.Fatal(err)
		}
		if env.Error.Code != "NOT_READY" || len(env.Error.Details) != 1 || env.Error.Details[0].Message != "redis unreachable" {
			t.Fatalf("body %s", body)
		}
	})
}
