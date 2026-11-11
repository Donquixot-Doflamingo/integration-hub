package isfcsync

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// State is what ISFC says now: the tenants linked to the Hub console client and every ISFC
// tenant by UUID (both keyed by lower-case UUID).
type State struct {
	Linked  map[string]bool
	Tenants map[string]isfc.Tenant
}

// Change kinds of one tenant.
const (
	KindCreate  = "create"
	KindUpdate  = "update" // the ISFC copy changed
	KindDisable = "disable"
	KindEnable  = "enable"
)

// Action is one planned tenant change. Tenant is the tenant as it must be after the change.
type Action struct {
	Kind    string // the strongest change: create, disable, enable, update
	HubID   string
	ISFCID  string
	Fields  []string // what changed: key, name, status, linked, disabled
	Tenant  registry.Tenant
	Created bool
}

// Plan is the set of changes one reconcile makes.
type Plan struct {
	Actions   []Action
	Unchanged int
}

// Counts returns created, updated, disabled and enabled actions.
func (p Plan) Counts() (created, updated, disabled, enabled int) {
	for _, a := range p.Actions {
		switch a.Kind {
		case KindCreate:
			created++
		case KindUpdate:
			updated++
		case KindDisable:
			disabled++
		case KindEnable:
			enabled++
		}
	}
	return
}

// PlanOptions narrow or shape a plan.
type PlanOptions struct {
	// PlatformTenantID is the ISFC UUID of the platform tenant: sync skips it (AUTH_PLAN §2.2).
	PlatformTenantID string
	// Only restricts the plan to one ISFC tenant UUID (syncTenant); "" plans every tenant.
	Only string
}

// BuildPlan compares ISFC's state with the snapshot and returns what to change, following the
// lifecycle table of AUTH_PLAN §2.1:
//
//   - linked and ACTIVE, not in the Hub: create (setup pending, status active)
//   - rename or key change in ISFC: only the isfc copy changes; display_name, setup, status stay
//   - INACTIVE, unlinked, or gone from ISFC: disable (and remember that the sync did it)
//   - ACTIVE and linked again: re-enable what the sync disabled; a Hub suspension and an admin's
//     own disable are kept
//   - never delete; Hub tenants without an isfc link are not touched
//
// New Hub ids are derived deterministically (processing order: ISFC key, then UUID) and never
// collide with an existing tenant or one created in the same plan.
func BuildPlan(snap *registry.Snapshot, st State, now time.Time, opt PlanOptions) Plan {
	platform := strings.ToLower(opt.PlatformTenantID)
	only := strings.ToLower(opt.Only)
	known := map[string]bool{}
	for id := range st.Linked {
		known[id] = true
	}
	for _, t := range snap.Tenants {
		if t.ISFC != nil {
			known[strings.ToLower(t.ISFC.TenantID)] = true
		}
	}
	ids := make([]string, 0, len(known))
	for id := range known {
		if id == platform && platform != "" || (only != "" && id != only) {
			continue
		}
		ids = append(ids, id)
	}
	// Stable order: ISFC key, then UUID. A tenant gone from ISFC sorts by its stored key.
	key := func(id string) string {
		if t, ok := st.Tenants[id]; ok {
			return strings.ToLower(t.Key)
		}
		if t, ok := snap.TenantByISFC(id); ok {
			return strings.ToLower(t.ISFC.Key)
		}
		return ""
	}
	slices.SortFunc(ids, func(a, b string) int {
		if c := strings.Compare(key(a), key(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})

	taken := func(id string) bool { _, ok := snap.Tenants[id]; return ok }
	var p Plan
	for _, id := range ids {
		it, present := st.Tenants[id]
		active := st.Linked[id] && present && it.Status == isfc.StatusActive
		cur, exists := snap.TenantByISFC(id)
		if !exists {
			if !active {
				continue // never create a tenant that is not active and linked
			}
			hub := DeriveHubID(it.Key, taken)
			t := registry.Tenant{
				Tenant: hub, Setup: registry.SetupPending, Status: "active", APIKeys: []registry.APIKey{},
				ISFC: &registry.TenantISFC{TenantID: id, Key: it.Key, Name: it.Name, Status: it.Status, Linked: true, SyncedAt: ptr(now)},
			}
			prev := taken
			taken = func(x string) bool { return x == hub || prev(x) }
			p.Actions = append(p.Actions, Action{Kind: KindCreate, HubID: hub, ISFCID: id, Tenant: t, Created: true})
			continue
		}
		next := *cur
		l := *cur.ISFC
		next.ISFC = &l
		var fields []string
		set := func(field string, differs bool, apply func()) {
			if differs {
				fields = append(fields, field)
				apply()
			}
		}
		if present {
			set("key", l.Key != it.Key, func() { l.Key = it.Key })
			set("name", l.Name != it.Name, func() { l.Name = it.Name })
			set("status", l.Status != it.Status, func() { l.Status = it.Status })
		} else {
			set("status", l.Status != isfc.StatusInactive, func() { l.Status = isfc.StatusInactive }) // gone: same as INACTIVE
		}
		set("linked", l.Linked != st.Linked[id], func() { l.Linked = st.Linked[id] })
		kind := KindUpdate
		switch {
		case active && next.Disabled && l.DisabledBySync:
			fields, kind = append(fields, "disabled"), KindEnable
			next.Disabled, l.DisabledBySync = false, false
		case !active && !next.Disabled:
			fields, kind = append(fields, "disabled"), KindDisable
			next.Disabled, l.DisabledBySync = true, true
		}
		if len(fields) == 0 {
			p.Unchanged++
			continue
		}
		l.SyncedAt = ptr(now)
		p.Actions = append(p.Actions, Action{Kind: kind, HubID: cur.Tenant, ISFCID: id, Fields: fields, Tenant: next})
	}
	return p
}

func ptr[T any](v T) *T { return &v }

var nonID = regexp.MustCompile(`[^a-z0-9_-]`)

// DeriveHubID derives the immutable Hub tenant id of an ISFC tenant key (AUTH_PLAN §2.1): the
// lower-cased key, padded to three characters with "-t" when shorter, then "-2", "-3", ... until
// taken reports it free. The result always matches the Hub's id pattern
// ^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$. It is computed once, at creation; the id then never changes,
// even if the ISFC key does.
func DeriveHubID(isfcKey string, taken func(string) bool) string {
	base := nonID.ReplaceAllString(strings.ToLower(isfcKey), "")
	if base == "" || !isAlnum(base[0]) {
		base = "t" + base
	}
	if len(base) < 3 {
		base += "-t"
	}
	if len(base) > 56 { // room for a "-NNNN" suffix inside 64
		base = strings.TrimRight(base[:56], "-_")
	}
	if len(base) < 3 { // trimming cannot make it shorter than 3 in practice; stay safe
		base += "-t"
	}
	id := base
	for n := 2; taken(id); n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

func isAlnum(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }
