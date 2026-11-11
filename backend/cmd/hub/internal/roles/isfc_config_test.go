package roles

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestLoadISFCConfig(t *testing.T) {
	base := map[string]string{"HUB_PG_DSN": "postgres://x"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	creds := []string{"HUB_TENANT_SOURCE", "isfc", "HUB_ISFC_AUTH_URL", "http://auth", "HUB_ISFC_CLIENT_ID", "integration-hub", "HUB_ISFC_CLIENT_SECRET", "s3cret"}
	tests := []struct {
		name    string
		role    Role
		env     map[string]string
		wantErr string
		check   func(t *testing.T, c ISFCConfig)
	}{
		{"defaults change nothing", Worker, base, "", func(t *testing.T, c ISFCConfig) {
			if c.FromISFC() || c.EdgeAuth || c.SyncDryRun || c.ReconcileInterval != 5*time.Minute || c.AuthClientID != "auth-service" {
				t.Errorf("%+v", c)
			}
		}},
		{"isfc source with credentials", Worker, with(creds...), "", func(t *testing.T, c ISFCConfig) {
			if !c.FromISFC() || c.BackendClientID != "integration-hub" || c.ClientSecret != "s3cret" {
				t.Errorf("%+v", c)
			}
		}},
		{"isfc source without credentials is refused on the worker", Worker, with("HUB_TENANT_SOURCE", "isfc"), "HUB_ISFC_AUTH_URL, HUB_ISFC_CLIENT_ID, HUB_ISFC_CLIENT_SECRET", nil},
		{"isfc source without credentials is refused on the admin", Admin, with("HUB_TENANT_SOURCE", "isfc"), "HUB_ISFC_AUTH_URL", nil},
		{"the gateway needs no ISFC credentials", Gateway, with("HUB_TENANT_SOURCE", "isfc", "HUB_EDGE_ISFC_AUTH", "on"), "", func(t *testing.T, c ISFCConfig) {
			if !c.EdgeAuth || !c.FromISFC() {
				t.Errorf("%+v", c)
			}
		}},
		{"bad source", Worker, with("HUB_TENANT_SOURCE", "ldap"), "HUB_TENANT_SOURCE: want local or isfc", nil},
		{"bad edge switch", Gateway, with("HUB_EDGE_ISFC_AUTH", "maybe"), "HUB_EDGE_ISFC_AUTH: want on or off", nil},
		{"bad interval", Worker, with("HUB_ISFC_RECONCILE_INTERVAL", "soon"), "HUB_ISFC_RECONCILE_INTERVAL", nil},
		{"dry run and interval", Worker, with("HUB_ISFC_SYNC_DRY_RUN", "true", "HUB_ISFC_RECONCILE_INTERVAL", "30s"), "", func(t *testing.T, c ISFCConfig) {
			if !c.SyncDryRun || c.ReconcileInterval != 30*time.Second {
				t.Errorf("%+v", c)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(tt.role, nil, envMap(tt.env), io.Discard)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, c.ISFC)
		})
	}
	// The secret never prints.
	c, _ := Load(Worker, nil, envMap(with(creds...)), io.Discard)
	if s := fmt.Sprintf("%v %+v", c.ISFC, c.ISFC); strings.Contains(s, "s3cret") {
		t.Fatalf("secret printed: %s", s)
	}
}

func TestNewISFCSyncIsOffByDefault(t *testing.T) {
	s, err := newISFCSync(Config{}, nil, nil, nil, nil, nil, nil)
	if s != nil || err != nil {
		t.Fatalf("local source built a reconciler: %v %v", s, err)
	}
}

func TestNewISFCAdminIsOffByDefault(t *testing.T) {
	syncer, events, err := newISFCAdmin(Config{}, nil, nil, nil, nil, nil)
	if syncer != nil || events != nil || err != nil {
		t.Fatalf("local source built sync routes: %v %v %v", syncer, events, err)
	}
}
