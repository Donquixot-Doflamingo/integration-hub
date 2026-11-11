package roles

import (
	"fmt"
	"strings"
	"time"
)

// Tenant sources (HUB_TENANT_SOURCE).
const (
	TenantSourceLocal = "local"
	TenantSourceISFC  = "isfc"
)

// ISFCConfig is the ISFC side of a role's configuration (AUTH_PLAN §5, TECH_SPEC §8.1). It is read
// from the environment only: the client secret must never appear in a process listing.
type ISFCConfig struct {
	// TenantSource is HUB_TENANT_SOURCE: "local" (default) or "isfc" (the worker runs the
	// reconciler, the admin refuses createTenant, the edge refuses setup:pending tenants).
	TenantSource string
	// EdgeAuth is HUB_EDGE_ISFC_AUTH=on: the gateway also accepts ISFC S2S credentials.
	EdgeAuth bool
	// AuthURL, ClientID and ClientSecret are the Hub backend client's access to the ISFC
	// auth-service (HUB_ISFC_AUTH_URL, HUB_ISFC_CLIENT_ID, HUB_ISFC_CLIENT_SECRET).
	// AuthClientID is the auth-service's own client id (HUB_ISFC_AUTH_CLIENT_ID, default
	// "auth-service"). BackendClientID is the client that issues caller keys
	// (HUB_ISFC_BACKEND_CLIENT_ID, default ClientID).
	AuthURL         string
	ClientID        string
	ClientSecret    Secret
	AuthClientID    string
	BackendClientID string
	// ReconcileInterval is HUB_ISFC_RECONCILE_INTERVAL (default 5m); SyncDryRun is
	// HUB_ISFC_SYNC_DRY_RUN (log what would change, change nothing).
	ReconcileInterval time.Duration
	SyncDryRun        bool
	// EventsSecret signs the ISFC events webhook (HUB_ISFC_EVENTS_SECRET); empty disables the route.
	EventsSecret Secret
}

// consoleClientID is the console client whose tenants list is the set of linked tenants.
func (c Config) consoleClientID() string {
	if c.ISFCConsoleClientID != "" {
		return c.ISFCConsoleClientID
	}
	return "integration-hub-console"
}

// FromISFC reports whether tenants come from ISFC.
func (c ISFCConfig) FromISFC() bool { return c.TenantSource == TenantSourceISFC }

// loadISFC reads and validates the ISFC settings of role. Problems are returned, not fatal here,
// so Load reports them together with the others.
func (c *Config) loadISFC(role Role, getenv func(string) string) []error {
	env := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	var errs []error
	i := &c.ISFC
	i.TenantSource = env("HUB_TENANT_SOURCE", TenantSourceLocal)
	if i.TenantSource != TenantSourceLocal && i.TenantSource != TenantSourceISFC {
		errs = append(errs, fmt.Errorf("HUB_TENANT_SOURCE: want local or isfc, got %q", i.TenantSource))
	}
	switch v := env("HUB_EDGE_ISFC_AUTH", "off"); v {
	case "on":
		i.EdgeAuth = true
	case "off":
	default:
		errs = append(errs, fmt.Errorf("HUB_EDGE_ISFC_AUTH: want on or off, got %q", v))
	}
	i.AuthURL, i.ClientID = env("HUB_ISFC_AUTH_URL", ""), env("HUB_ISFC_CLIENT_ID", "")
	i.ClientSecret = Secret(getenv("HUB_ISFC_CLIENT_SECRET"))
	i.AuthClientID = env("HUB_ISFC_AUTH_CLIENT_ID", "auth-service")
	i.BackendClientID = env("HUB_ISFC_BACKEND_CLIENT_ID", i.ClientID)
	i.EventsSecret = Secret(getenv("HUB_ISFC_EVENTS_SECRET"))
	i.SyncDryRun = env("HUB_ISFC_SYNC_DRY_RUN", "false") == "true"
	interval := env("HUB_ISFC_RECONCILE_INTERVAL", "5m")
	var err error
	if i.ReconcileInterval, err = time.ParseDuration(interval); err != nil || i.ReconcileInterval < time.Second {
		errs = append(errs, fmt.Errorf("HUB_ISFC_RECONCILE_INTERVAL: want a duration of at least 1s, got %q", interval))
	}
	if i.FromISFC() && (role == Worker || role == Admin) {
		var missing []string
		for _, f := range []struct{ name, v string }{{"HUB_ISFC_AUTH_URL", i.AuthURL}, {"HUB_ISFC_CLIENT_ID", i.ClientID}, {"HUB_ISFC_CLIENT_SECRET", string(i.ClientSecret)}} {
			if f.v == "" {
				missing = append(missing, f.name)
			}
		}
		if len(missing) > 0 {
			errs = append(errs, fmt.Errorf("HUB_TENANT_SOURCE=isfc needs %s (the Hub backend client's access to the ISFC auth-service)", strings.Join(missing, ", ")))
		}
	}
	return errs
}
