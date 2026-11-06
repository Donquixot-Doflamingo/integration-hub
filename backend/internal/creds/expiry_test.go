package creds

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

type staticLoader map[string]AccountSecrets

func (s staticLoader) Load(context.Context, []string) (map[string]AccountSecrets, error) {
	return s, nil
}

// TestCredentialExpiryDays (HUB-230): credential_expiry_days reports every slot that has an
// expiry, counts down with the clock without a reload, and drops slots without one.
func TestCredentialExpiryDays(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
	activeExp, nextExp := clk.t.Add(10*24*time.Hour), clk.t.Add(36*time.Hour)
	loader := staticLoader{
		"oauth-acct": {
			Active: Slot{Version: 2, Values: map[string]string{"client_secret": "s-active-not-real"}, ExpiresAt: &activeExp},
			Next:   &Slot{Version: 3, Values: map[string]string{"client_secret": "s-next-not-real"}, ExpiresAt: &nextExp},
		},
		"static-acct": {Active: Slot{Version: 1, Values: map[string]string{"passcode": "p-not-real"}}},
	}
	m := telemetry.NewMetrics("test", "dev")
	c, err := NewCache(loader, telemetry.NewSecretSet(), m, CacheOptions{Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Warm(t.Context(), []string{"oauth-acct", "static-acct"}); err != nil {
		t.Fatal(err)
	}
	want := `
# HELP credential_expiry_days Days until a credential slot expires (negative once expired); only slots with an expiry are reported.
# TYPE credential_expiry_days gauge
credential_expiry_days{account="oauth-acct",slot="active"} 10
credential_expiry_days{account="oauth-acct",slot="next"} 1.5
`
	if err := testutil.GatherAndCompare(m.Registry, strings.NewReader(want), "credential_expiry_days"); err != nil {
		t.Fatal(err)
	}
	clk.Advance(3 * 24 * time.Hour) // no reload: the value is computed at scrape time
	want = strings.NewReplacer(`"active"} 10`, `"active"} 7`, `"next"} 1.5`, `"next"} -1.5`).Replace(want)
	if err := testutil.GatherAndCompare(m.Registry, strings.NewReader(want), "credential_expiry_days"); err != nil {
		t.Fatal(err)
	}
}
