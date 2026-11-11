package isfcsync

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

var (
	idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$`) // registry.idPattern
	t0        = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
)

const (
	uHL = "6f1c0b2e-3a4d-4c5e-8f60-1a2b3c4d5e6f"
	uPL = "7a2d1c3f-4b5e-4d6f-9a71-2b3c4d5e6f70"
	uXX = "8b3e2d4a-5c6f-4e70-8b82-3c4d5e6f7081"
)

// snapOf builds a snapshot holding only tenants (empty everything else).
func snapOf(t *testing.T, tenants ...*registry.Tenant) *registry.Snapshot {
	t.Helper()
	s := &registry.Snapshot{Tenants: map[string]*registry.Tenant{}}
	for _, tn := range tenants {
		s.Tenants[tn.Tenant] = tn
	}
	return registry.ReindexKeys(s)
}

func hubTenant(id, uuid, key, name, status string, linked bool) *registry.Tenant {
	return &registry.Tenant{Tenant: id, Setup: registry.SetupComplete, Status: "active", APIKeys: []registry.APIKey{},
		ISFC: &registry.TenantISFC{TenantID: uuid, Key: key, Name: name, Status: status, Linked: linked}}
}

func isfcState(linked []string, tenants ...isfc.Tenant) State {
	st := State{Linked: map[string]bool{}, Tenants: map[string]isfc.Tenant{}}
	for _, l := range linked {
		st.Linked[l] = true
	}
	for _, tn := range tenants {
		st.Tenants[tn.ID] = tn
	}
	return st
}

func it(id, key, name, status string) isfc.Tenant {
	return isfc.Tenant{ID: id, Key: key, Name: name, Status: status}
}

// TestBuildPlanLifecycle is one row per line of the AUTH_PLAN §2.1 lifecycle table.
func TestBuildPlanLifecycle(t *testing.T) {
	tests := []struct {
		name     string
		snap     *registry.Snapshot
		st       State
		want     string // "<kind> <hub id> <fields>" per action, "; " joined; "" = no action
		unchaged int
		check    func(t *testing.T, a Action)
	}{
		{"linked and ACTIVE, first seen: create pending and active",
			snapOf(t), isfcState([]string{uHL}, it(uHL, "HL", "Home Loans", "ACTIVE")), "create hl-t ", 0,
			func(t *testing.T, a Action) {
				tn := a.Tenant
				if tn.Setup != registry.SetupPending || tn.Status != "active" || tn.Disabled || tn.DisplayName != "" || tn.ISFC.Name != "Home Loans" ||
					!tn.ISFC.Linked || tn.ISFC.SyncedAt == nil || len(tn.APIKeys) != 0 {
					t.Errorf("created tenant %+v isfc %+v", tn, tn.ISFC)
				}
			}},
		{"ACTIVE in ISFC but not linked to the Hub client: nothing is created",
			snapOf(t), isfcState(nil, it(uHL, "HL", "Home Loans", "ACTIVE")), "", 0, nil},
		{"INACTIVE and linked, never seen: nothing is created",
			snapOf(t), isfcState([]string{uHL}, it(uHL, "HL", "Home Loans", "INACTIVE")), "", 0, nil},
		{"setup finished, nothing changed: no action",
			snapOf(t, hubTenant("hl-t", uHL, "HL", "Home Loans", "ACTIVE", true)),
			isfcState([]string{uHL}, it(uHL, "HL", "Home Loans", "ACTIVE")), "", 1, nil},
		{"renamed in ISFC: only the copy changes, the Hub display_name and setup stay",
			snapOf(t, func() *registry.Tenant {
				x := hubTenant("hl-t", uHL, "HL", "Home Loans", "ACTIVE", true)
				x.DisplayName, x.Setup = "Mortgages", registry.SetupComplete
				return x
			}()),
			isfcState([]string{uHL}, it(uHL, "HL", "Housing Loans", "ACTIVE")), "update hl-t name", 0,
			func(t *testing.T, a Action) {
				if a.Tenant.ISFC.Name != "Housing Loans" || a.Tenant.DisplayName != "Mortgages" || a.Tenant.Disabled || a.Tenant.Setup != registry.SetupComplete || a.HubID != "hl-t" {
					t.Errorf("tenant %+v", a.Tenant)
				}
			}},
		{"key changed in ISFC: the Hub id never changes",
			snapOf(t, hubTenant("hl-t", uHL, "HL", "Home Loans", "ACTIVE", true)),
			isfcState([]string{uHL}, it(uHL, "HOME", "Home Loans", "ACTIVE")), "update hl-t key", 0,
			func(t *testing.T, a Action) {
				if a.HubID != "hl-t" || a.Tenant.Tenant != "hl-t" || a.Tenant.ISFC.Key != "HOME" {
					t.Errorf("tenant %+v", a.Tenant)
				}
			}},
		{"INACTIVE in ISFC: disabled by sync, nothing deleted",
			snapOf(t, hubTenant("hl-t", uHL, "HL", "Home Loans", "ACTIVE", true)),
			isfcState(nil, it(uHL, "HL", "Home Loans", "INACTIVE")), "disable hl-t status,linked,disabled", 0,
			func(t *testing.T, a Action) {
				if !a.Tenant.Disabled || !a.Tenant.ISFC.DisabledBySync || a.Tenant.ISFC.Status != "INACTIVE" || a.Tenant.Setup != registry.SetupComplete {
					t.Errorf("tenant %+v isfc %+v", a.Tenant, a.Tenant.ISFC)
				}
			}},
		{"unlinked from the Hub client, still ACTIVE in ISFC: disabled",
			snapOf(t, hubTenant("hl-t", uHL, "HL", "Home Loans", "ACTIVE", true)),
			isfcState(nil, it(uHL, "HL", "Home Loans", "ACTIVE")), "disable hl-t linked,disabled", 0, nil},
		{"deleted in ISFC (absent from the list): same as INACTIVE",
			snapOf(t, hubTenant("hl-t", uHL, "HL", "Home Loans", "ACTIVE", true)),
			isfcState(nil), "disable hl-t status,linked,disabled", 0, nil},
		{"already disabled by sync and still INACTIVE: no action",
			snapOf(t, func() *registry.Tenant {
				x := hubTenant("hl-t", uHL, "HL", "Home Loans", "INACTIVE", false)
				x.Disabled, x.ISFC.DisabledBySync = true, true
				return x
			}()),
			isfcState(nil, it(uHL, "HL", "Home Loans", "INACTIVE")), "", 1, nil},
		{"re-activated and re-linked: back to enabled",
			snapOf(t, func() *registry.Tenant {
				x := hubTenant("hl-t", uHL, "HL", "Home Loans", "INACTIVE", false)
				x.Disabled, x.ISFC.DisabledBySync = true, true
				return x
			}()),
			isfcState([]string{uHL}, it(uHL, "HL", "Home Loans", "ACTIVE")), "enable hl-t status,linked,disabled", 0,
			func(t *testing.T, a Action) {
				if a.Tenant.Disabled || a.Tenant.ISFC.DisabledBySync || !a.Tenant.ISFC.Linked || a.Tenant.ISFC.Status != "ACTIVE" {
					t.Errorf("tenant %+v isfc %+v", a.Tenant, a.Tenant.ISFC)
				}
			}},
		{"re-enabled tenant keeps a suspension the Hub set itself",
			snapOf(t, func() *registry.Tenant {
				x := hubTenant("hl-t", uHL, "HL", "Home Loans", "INACTIVE", false)
				x.Disabled, x.ISFC.DisabledBySync, x.Status = true, true, "suspended"
				return x
			}()),
			isfcState([]string{uHL}, it(uHL, "HL", "Home Loans", "ACTIVE")), "enable hl-t status,linked,disabled", 0,
			func(t *testing.T, a Action) {
				if a.Tenant.Status != "suspended" || a.Tenant.Disabled {
					t.Errorf("tenant %+v", a.Tenant)
				}
			}},
		{"an admin's own disable is not undone when ISFC says ACTIVE",
			snapOf(t, func() *registry.Tenant {
				x := hubTenant("hl-t", uHL, "HL", "Home Loans", "ACTIVE", true)
				x.Disabled = true // set by an admin: DisabledBySync is false
				return x
			}()),
			isfcState([]string{uHL}, it(uHL, "HL", "Home Loans", "ACTIVE")), "", 1, nil},
		{"an admin-disabled tenant that ISFC deactivates stays the admin's (not claimed by sync)",
			snapOf(t, func() *registry.Tenant {
				x := hubTenant("hl-t", uHL, "HL", "Home Loans", "ACTIVE", true)
				x.Disabled = true
				return x
			}()),
			isfcState(nil, it(uHL, "HL", "Home Loans", "INACTIVE")), "update hl-t status,linked", 0,
			func(t *testing.T, a Action) {
				if !a.Tenant.Disabled || a.Tenant.ISFC.DisabledBySync {
					t.Errorf("tenant %+v isfc %+v", a.Tenant, a.Tenant.ISFC)
				}
			}},
		{"legacy tenant without an isfc link is never touched",
			snapOf(t, &registry.Tenant{Tenant: "orders-service", APIKeys: []registry.APIKey{}}),
			isfcState([]string{uHL}, it(uHL, "ORDERS-SERVICE", "Orders", "ACTIVE")), "create orders-service-2 ", 0, nil},
		{"the platform tenant is skipped",
			snapOf(t), isfcState([]string{uHL, uPL}, it(uHL, "HL", "Home Loans", "ACTIVE"), it(uPL, "INTEGRATION_HUB_PLATFORM", "Platform", "ACTIVE")),
			"create hl-t ", 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := BuildPlan(tt.snap, tt.st, t0, PlanOptions{PlatformTenantID: uPL})
			var got []string
			for _, a := range p.Actions {
				got = append(got, strings.TrimSpace(a.Kind+" "+a.HubID+" "+strings.Join(a.Fields, ",")))
			}
			if g := strings.Join(got, "; "); g != strings.TrimSpace(tt.want) {
				t.Fatalf("actions %q, want %q", g, tt.want)
			}
			if p.Unchanged != tt.unchaged {
				t.Errorf("unchanged %d, want %d", p.Unchanged, tt.unchaged)
			}
			if tt.check != nil && len(p.Actions) > 0 {
				tt.check(t, p.Actions[0])
			}
		})
	}
}

func TestBuildPlanNeverDeletesAndOnlyScopesToOneTenant(t *testing.T) {
	snap := snapOf(t, hubTenant("hl-t", uHL, "HL", "Home Loans", "ACTIVE", true), hubTenant("pl-t", uPL, "PL", "Personal", "ACTIVE", true))
	st := isfcState(nil, it(uHL, "HL", "Home Loans", "INACTIVE"), it(uPL, "PL", "Personal", "INACTIVE"))
	all := BuildPlan(snap, st, t0, PlanOptions{})
	one := BuildPlan(snap, st, t0, PlanOptions{Only: uHL})
	if len(all.Actions) != 2 || len(one.Actions) != 1 || one.Actions[0].HubID != "hl-t" {
		t.Fatalf("all %d one %+v", len(all.Actions), one.Actions)
	}
	for _, a := range all.Actions {
		if a.Kind == "delete" || a.Tenant.Tenant == "" {
			t.Errorf("action %+v", a)
		}
	}
}

func TestBuildPlanIsStableAndIdempotent(t *testing.T) {
	st := isfcState([]string{uHL, uPL, uXX}, it(uHL, "HL", "A", "ACTIVE"), it(uPL, "hl", "B", "ACTIVE"), it(uXX, "HL", "C", "ACTIVE"))
	a, b := BuildPlan(snapOf(t), st, t0, PlanOptions{}), BuildPlan(snapOf(t), st, t0, PlanOptions{})
	var ids []string
	for i := range a.Actions {
		if a.Actions[i].HubID != b.Actions[i].HubID {
			t.Fatalf("plan not deterministic: %v vs %v", a.Actions[i].HubID, b.Actions[i].HubID)
		}
		ids = append(ids, a.Actions[i].HubID)
	}
	// Order: key "hl" (uPL) < "hl" (uHL, "6f1c" < "7a2d")... all keys equal lower-cased, so by UUID.
	if strings.Join(ids, ",") != "hl-t,hl-t-2,hl-t-3" {
		t.Fatalf("ids %v: same-key tenants must get -2 and -3 in UUID order", ids)
	}
	// Applying the plan and planning again changes nothing.
	var applied []*registry.Tenant
	for _, x := range a.Actions {
		tn := x.Tenant
		applied = append(applied, &tn)
	}
	if again := BuildPlan(snapOf(t, applied...), st, t0.Add(time.Hour), PlanOptions{}); len(again.Actions) != 0 || again.Unchanged != 3 {
		t.Fatalf("second plan %+v", again)
	}
}

func TestDeriveHubID(t *testing.T) {
	taken := func(ids ...string) func(string) bool {
		return func(s string) bool {
			for _, id := range ids {
				if s == id {
					return true
				}
			}
			return false
		}
	}
	tests := []struct {
		name, key string
		taken     func(string) bool
		want      string
	}{
		{"lower-cased", "ORDERS", taken(), "orders"},
		{"too short is padded with -t", "HL", taken(), "hl-t"},
		{"one character", "A", taken(), "a-t"},
		{"three characters stay", "ABC", taken(), "abc"},
		{"collision gets -2", "HL", taken("hl-t"), "hl-t-2"},
		{"collision chain", "HL", taken("hl-t", "hl-t-2", "hl-t-3"), "hl-t-4"},
		{"case-insensitive collision with a legacy tenant", "Orders", taken("orders"), "orders-2"},
		{"leading underscore gets a letter", "_ab", taken(), "t_ab"},
		{"leading dash gets a letter", "-x", taken(), "t-x"},
		{"underscore and dash inside are kept", "a_b-c", taken(), "a_b-c"},
		{"foreign characters are dropped", "Ünï-1", taken(), "n-1"},
		{"only foreign characters", "日本", taken(), "t-t"},
		{"empty key", "", taken(), "t-t"},
		{"long key is cut inside the limit", strings.Repeat("x", 64), taken(), strings.Repeat("x", 56)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveHubID(tt.key, tt.taken)
			if got != tt.want {
				t.Fatalf("DeriveHubID(%q) = %q, want %q", tt.key, got, tt.want)
			}
			if !idPattern.MatchString(got) {
				t.Fatalf("%q does not match the Hub id pattern", got)
			}
		})
	}
	// Long key with many collisions still fits.
	long := strings.Repeat("y", 64)
	used := map[string]bool{}
	for range 12 {
		id := DeriveHubID(long, func(s string) bool { return used[s] })
		if !idPattern.MatchString(id) || used[id] {
			t.Fatalf("id %q (len %d)", id, len(id))
		}
		used[id] = true
	}
}

func TestSetKeysKeepsCommentsAndOtherKeys(t *testing.T) {
	doc := "# hand written comment\ntenant: hl-t\napi_keys: []   # no keys yet\nrate_limit: {rps: 10, burst: 20}\ndisabled: true\n"
	l := &registry.TenantISFC{TenantID: uHL, Key: "HL", Name: "Home Loans", Status: "ACTIVE", Linked: true, SyncedAt: &t0}
	out, err := setKeys([]byte(doc), map[string]any{"isfc": l, "disabled": nil})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"# hand written comment", "# no keys yet", "rate_limit:", "isfc:", "tenant_id: " + uHL} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "disabled") {
		t.Errorf("disabled not removed:\n%s", s)
	}
	// The result loads as a tenant.
	var back registry.Tenant
	if err := decodeYAML(out, &back); err != nil || back.ISFC == nil || back.ISFC.Key != "HL" || back.Disabled {
		t.Fatalf("round trip: %v %+v", err, back)
	}
}
