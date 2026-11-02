package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// snapCache holds the last snapshot rebuilt from each archive.
var snapCache sync.Map // *control.Archive → *registry.Snapshot

func init() { registrars = append(registrars, (*Server).registerIntegrations) }

func (s *Server) registerIntegrations() {
	s.handle("listIntegrations", RoleTenantViewer, false, "GET /integrations", s.listIntegrations)
	s.handle("getIntegration", RoleTenantViewer, false, "GET /integrations/{integration_id}", s.getIntegration)
	s.handle("listIntegrationVersions", RoleTenantViewer, false, "GET /integrations/{integration_id}/versions", s.listIntegrationVersions)
	s.handle("getIntegrationVersion", RoleTenantViewer, false, "GET /integrations/{integration_id}/versions/{version}", s.getIntegrationVersion)
	s.handle("diffIntegrationVersions", RoleTenantViewer, false, "GET /integrations/{integration_id}/diff", s.diffIntegrationVersions)
	s.handle("pauseIntegration", RoleEditor, true, "POST /integrations/{integration_id}/pause", s.setPaused(true))
	s.handle("resumeIntegration", RoleEditor, true, "POST /integrations/{integration_id}/resume", s.setPaused(false))
}

// objectTimes reads an object's created_at and updated_at from the change log; objects that
// predate it (the bootstrap config) report the snapshot build time for both.
func (s *Server) objectTimes(ctx context.Context, objectType, id string, fallback time.Time) (created, updated time.Time) {
	created, updated = fallback.UTC(), fallback.UTC()
	if s.d.Pool == nil {
		return created, updated
	}
	var lo, hi *time.Time
	if err := s.d.Pool.QueryRow(ctx, `SELECT min(at), max(at) FROM hub.change_log WHERE object_type = $1 AND object_id = $2`, objectType, id).
		Scan(&lo, &hi); err == nil && lo != nil {
		created, updated = lo.UTC(), hi.UTC()
	}
	return created, updated
}

// latestVersion is the highest published version (0 when none).
func latestVersion(in *registry.Integration) int {
	latest := 0
	for v := range in.Versions {
		latest = max(latest, v)
	}
	return latest
}

// integrationView is the contract's Integration (without the active version in full).
func (s *Server) integrationView(ctx context.Context, snap *registry.Snapshot, in *registry.Integration) map[string]any {
	latest := latestVersion(in)
	created, updated := s.objectTimes(ctx, "integration", in.Integration, snap.BuiltAt)
	var active, latestV *int
	if in.ActiveVersion > 0 {
		active = &in.ActiveVersion
	}
	if latest > 0 {
		latestV = &latest
	}
	grants := in.Grants
	if grants == nil {
		grants = []string{}
	}
	return map[string]any{
		"integration_id": in.Integration, "owner_tenant": in.OwnerTenant, "capability": in.Capability,
		"description": nullable(in.Description), "grants": grants, "moves_money_or_quota": in.MovesMoneyOrQuota,
		"active_version": active, "latest_version": latestV, "has_draft": false,
		"paused": in.Paused, "paused_reason": nullable(in.PausedReason), "active": nil,
		"created_at": created, "updated_at": updated,
	}
}

// snapshot is the registry the admin reads: the archive's latest (ADR-0010).
//
// Rebuilding the archived blob costs a full build, so the snapshot is cached per archive and
// reused while the archive's latest checksum is unchanged (one cheap query per read).
func (s *Server) snapshot(ctx context.Context) (*registry.Snapshot, error) {
	if pub := s.d.Publisher; pub != nil {
		if pub.Archive != nil {
			if meta, err := pub.Archive.LatestMeta(ctx); err == nil {
				if c, ok := snapCache.Load(pub.Archive); ok && c.(*registry.Snapshot).Checksum == meta.Checksum {
					return c.(*registry.Snapshot), nil
				}
			}
		}
		_, snap, err := pub.Current(ctx)
		if err == nil && pub.Archive != nil {
			snapCache.Store(pub.Archive, snap)
		}
		return snap, err
	}
	if s.d.Snapshot != nil {
		if snap := s.d.Snapshot(); snap != nil {
			return snap, nil
		}
	}
	return nil, errors.New("no registry snapshot")
}

// sees reports whether the caller may read integration in (owner or grantee in scope).
func sees(p Principal, in *registry.Integration) bool {
	if p.SeesTenant(in.OwnerTenant) {
		return true
	}
	return slices.ContainsFunc(in.Grants, p.SeesTenant)
}

// visibleIntegration loads the integration of the path, 404 outside the caller's scope.
func (s *Server) visibleIntegration(r *http.Request) (*registry.Snapshot, *registry.Integration, error) {
	snap, err := s.snapshot(r.Context())
	if err != nil {
		return nil, nil, err
	}
	in, ok := snap.Integrations[r.PathValue("integration_id")]
	if !ok || !sees(principal(r), in) {
		return snap, nil, notFound("Integration")
	}
	return snap, in, nil
}

func (s *Server) listIntegrations(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := page(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	p := principal(r)
	tenant := q.Get("tenant_id")
	if tenant != "" && !p.SeesTenant(tenant) {
		s.fail(w, r, &Error{Status: http.StatusForbidden, Code: CodeForbidden, Message: "Tenant " + tenant + " is outside your scope"})
		return
	}
	var boolFilters [3]*bool
	for i, name := range []string{"paused", "has_active_version", "active_version_behind"} {
		if v := q.Get(name); v != "" {
			b, perr := strconv.ParseBool(v)
			if perr != nil {
				s.fail(w, r, badRequest("", name+" must be true or false"))
				return
			}
			boolFilters[i] = &b
		}
	}
	paused, hasActive, behind := boolFilters[0], boolFilters[1], boolFilters[2]
	snap, err := s.snapshot(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	needle := strings.ToLower(q.Get("q"))
	var rows []integrationRow
	for _, id := range slices.Sorted(maps.Keys(snap.Integrations)) {
		rows = append(rows, integrationRow{id: id, in: snap.Integrations[id]})
	}
	var items []map[string]any
	var last string
	for _, row := range rows {
		in := row.in
		if len(cursor) == 1 && row.id <= cursor[0] {
			continue
		}
		if !sees(p, in) || (tenant != "" && in.OwnerTenant != tenant && !slices.Contains(in.Grants, tenant)) {
			continue
		}
		if c := q.Get("capability"); c != "" && in.Capability != c {
			continue
		}
		act := in.Active()
		if !targets(act, q.Get("vendor_id"), q.Get("account_id")) {
			continue
		}
		if paused != nil && in.Paused != *paused {
			continue
		}
		if hasActive != nil && (act != nil) != *hasActive {
			continue
		}
		hasDraft := false
		if behind != nil && (act == nil || (latestVersion(in) > in.ActiveVersion || hasDraft) != *behind) {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(in.Integration), needle) && !strings.Contains(strings.ToLower(in.Description), needle) {
			continue
		}
		if len(items) == limit {
			break
		}
		items = append(items, s.integrationSummary(in, hasDraft))
		last = row.id
	}
	var next *string
	if len(items) == limit && last != "" && last != rows[len(rows)-1].id {
		next = nextCursor(last)
	}
	s.fillUpdatedAt(r.Context(), snap, items)
	if items == nil {
		items = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

type integrationRow struct {
	id string
	in *registry.Integration
}

// targets reports whether the active version targets vendor / account (primary or failover);
// empty filters match everything.
func targets(v *registry.IntegrationVersion, vendor, account string) bool {
	if vendor == "" && account == "" {
		return true
	}
	if v == nil {
		return false
	}
	for _, t := range append(slices.Clone(v.Targets), v.Failover...) {
		if (vendor == "" || t.Vendor == vendor) && (account == "" || t.Account == account) {
			return true
		}
	}
	return false
}

// integrationSummary is the contract's IntegrationSummary (updated_at filled by fillUpdatedAt).
func (s *Server) integrationSummary(in *registry.Integration, hasDraft bool) map[string]any {
	var active, latest *int
	if in.ActiveVersion > 0 {
		active = &in.ActiveVersion
	}
	l := latestVersion(in)
	if hasDraft {
		l++
	}
	if l > 0 {
		latest = &l
	}
	var mode *string
	tg := []map[string]any{}
	if v := in.Active(); v != nil {
		m := string(v.Delivery.Mode)
		mode = &m
		for _, t := range v.Targets {
			w := t.Weight
			if len(v.Targets) == 1 && w == 0 {
				w = 100
			}
			tg = append(tg, map[string]any{"vendor_id": t.Vendor, "account_id": t.Account, "weight": w})
		}
	}
	return map[string]any{"integration_id": in.Integration, "owner_tenant": in.OwnerTenant, "capability": in.Capability,
		"description": nullable(in.Description), "active_version": active, "latest_version": latest, "has_draft": hasDraft,
		"paused": in.Paused, "delivery_mode": mode, "targets": tg}
}

// fillUpdatedAt sets updated_at of each summary from the change log in one query.
func (s *Server) fillUpdatedAt(ctx context.Context, snap *registry.Snapshot, items []map[string]any) {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it["integration_id"].(string)
		it["updated_at"] = snap.BuiltAt.UTC()
	}
	if s.d.Pool == nil || len(ids) == 0 {
		return
	}
	rows, err := s.d.Pool.Query(ctx, `SELECT object_id, max(at) FROM hub.change_log WHERE object_type = 'integration' AND object_id = ANY($1) GROUP BY object_id`, ids)
	if err != nil {
		return
	}
	defer rows.Close()
	at := map[string]time.Time{}
	for rows.Next() {
		var id string
		var t time.Time
		if rows.Scan(&id, &t) == nil {
			at[id] = t.UTC()
		}
	}
	for _, it := range items {
		if t, ok := at[it["integration_id"].(string)]; ok {
			it["updated_at"] = t
		}
	}
}

func (s *Server) getIntegration(w http.ResponseWriter, r *http.Request) {
	snap, in, err := s.visibleIntegration(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	view := s.integrationView(r.Context(), snap, in)
	if v := in.Active(); v != nil {
		meta := s.versionMeta(r.Context(), in.Integration)
		view["active"] = publishedVersionView(snap, in, in.ActiveVersion, v, meta[in.ActiveVersion])
	}
	w.Header().Set("ETag", integrationETag(in, false))
	writeJSON(w, http.StatusOK, view)
}

// versionMeta is what the change log knows about a published version.
type versionMeta struct {
	BasedOn         *int       `json:"based_on"`
	CopiedFrom      *int       `json:"copied_from_version"`
	CreatedAt       *time.Time `json:"created_at"`
	PublishedAt     time.Time  `json:"-"`
	PublishedBy     string     `json:"-"`
	SnapshotVersion *int64     `json:"-"`
	known           bool
}

// versionMeta reads the publish rows of integration's versions (the newest row per version
// wins: an archive rebuilt from scratch republishes the same numbers).
func (s *Server) versionMeta(ctx context.Context, integration string) map[int]versionMeta {
	out := map[int]versionMeta{}
	if s.d.Pool == nil {
		return out
	}
	rows, err := s.d.Pool.Query(ctx, `SELECT at, actor, snapshot_version, after FROM hub.change_log
WHERE object_type = 'integration' AND object_id = $1 AND snapshot_version IS NOT NULL AND after ? 'version'
  AND action IN ('publishIntegrationVersion', 'rollbackIntegration', 'updateIntegrationWeights') ORDER BY at`, integration)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var m versionMeta
		var after []byte
		if rows.Scan(&m.PublishedAt, &m.PublishedBy, &m.SnapshotVersion, &after) != nil {
			continue
		}
		var a struct {
			Version int  `json:"version"`
			Active  bool `json:"active"`
			versionMeta
		}
		if json.Unmarshal(after, &a) != nil || a.Version < 1 {
			continue
		}
		m.BasedOn, m.CopiedFrom, m.CreatedAt, m.known = a.BasedOn, a.CopiedFrom, a.CreatedAt, true
		m.PublishedAt = m.PublishedAt.UTC()
		out[a.Version] = m
	}
	return out
}

// publishedVersionView is the contract's IntegrationVersion of a published version.
func publishedVersionView(snap *registry.Snapshot, in *registry.Integration, n int, v *registry.IntegrationVersion, m versionMeta) map[string]any {
	published := snap.BuiltAt.UTC()
	created := published
	var by *string
	var snapV *int64
	if m.known {
		published, by, snapV = m.PublishedAt, &m.PublishedBy, m.SnapshotVersion
		created = published
		if m.CreatedAt != nil {
			created = m.CreatedAt.UTC()
		}
	} else {
		by = nullable(v.Publish.Author)
	}
	basedOn := m.BasedOn
	if basedOn == nil && n > 1 && !m.known {
		prev := n - 1
		basedOn = &prev
	}
	return map[string]any{"integration_id": in.Integration, "version": n, "state": "published", "spec": renderSpec(v),
		"based_on": basedOn, "copied_from_version": m.CopiedFrom, "author": v.Publish.Author, "reason": v.Publish.Reason,
		"created_at": created, "published_at": published, "published_by": by, "snapshot_version": snapV}
}

// versionSummary drops the spec of a version view (IntegrationVersionSummary).
func versionSummary(view map[string]any, active int) map[string]any {
	return map[string]any{"integration_id": view["integration_id"], "version": view["version"], "state": view["state"],
		"based_on": view["based_on"], "author": view["author"], "reason": view["reason"], "created_at": view["created_at"],
		"published_at": view["published_at"], "is_active": view["version"] == active && view["state"] == "published"}
}

// versionViews lists every version of in (newest first) as full IntegrationVersion views.
func (s *Server) versionViews(ctx context.Context, snap *registry.Snapshot, in *registry.Integration) []map[string]any {
	meta := s.versionMeta(ctx, in.Integration)
	var out []map[string]any
	for _, n := range slices.Backward(slices.Sorted(maps.Keys(in.Versions))) {
		out = append(out, publishedVersionView(snap, in, n, in.Versions[n], meta[n]))
	}
	return out
}

func (s *Server) listIntegrationVersions(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := page(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	after := 0
	if len(cursor) == 1 {
		if after, err = strconv.Atoi(cursor[0]); err != nil {
			s.fail(w, r, badRequest("", "cursor is not a cursor of this list"))
			return
		}
	}
	state := r.URL.Query().Get("state")
	snap, in, err := s.visibleIntegration(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items := []map[string]any{}
	var next *string
	for _, v := range s.versionViews(r.Context(), snap, in) {
		n := v["version"].(int)
		if (after > 0 && n >= after) || (state != "" && v["state"] != state) {
			continue
		}
		if len(items) == limit {
			next = nextCursor(strconv.Itoa(items[limit-1]["version"].(int)))
			break
		}
		items = append(items, versionSummary(v, in.ActiveVersion))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

// pathVersion reads the {version} path parameter.
func pathVersion(r *http.Request) (int, error) {
	n, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || n < 1 {
		return 0, badRequest("", "version must be a positive integer")
	}
	return n, nil
}

// findVersion returns the full view of version n of in, or 404.
func (s *Server) findVersion(ctx context.Context, snap *registry.Snapshot, in *registry.Integration, n int) (map[string]any, error) {
	for _, v := range s.versionViews(ctx, snap, in) {
		if v["version"] == n {
			return v, nil
		}
	}
	return nil, notFound("Integration version")
}

func (s *Server) getIntegrationVersion(w http.ResponseWriter, r *http.Request) {
	n, err := pathVersion(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	snap, in, err := s.visibleIntegration(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v, err := s.findVersion(r.Context(), snap, in, n)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) diffIntegrationVersions(w http.ResponseWriter, r *http.Request) {
	var ns [2]int
	for i, name := range []string{"from", "to"} {
		n, err := strconv.Atoi(r.URL.Query().Get(name))
		if err != nil || n < 1 {
			s.fail(w, r, badRequest("", name+" must be a version number (≥ 1)"))
			return
		}
		ns[i] = n
	}
	snap, in, err := s.visibleIntegration(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var specs [2]map[string]any
	for i, n := range ns {
		v, err := s.findVersion(r.Context(), snap, in, n)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		specs[i], _ = v["spec"].(map[string]any)
	}
	changes := append([]diffOp{}, diffJSON("", specs[0], specs[1])...)
	writeJSON(w, http.StatusOK, map[string]any{"integration_id": in.Integration, "from": ns[0], "to": ns[1], "changes": changes,
		"unified": unifiedDiff("v"+strconv.Itoa(ns[0]), "v"+strconv.Itoa(ns[1]), yamlLines(specs[0]), yamlLines(specs[1]))})
}

func (s *Server) setPaused(paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reason string `json:"reason"`
		}
		if err := decode(w, r, &body); err != nil {
			s.fail(w, r, err)
			return
		}
		reason, err := reasonOf(r, body.Reason)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if s.d.Publisher == nil {
			s.fail(w, r, errors.New("no publisher"))
			return
		}
		res, err := s.d.Publisher.SetPaused(r.Context(), r.PathValue("integration_id"), paused, principal(r).Actor(), reason)
		if err != nil {
			s.fail(w, r, controlError(err))
			return
		}
		in := res.Snapshot.Integrations[r.PathValue("integration_id")]
		writeJSON(w, http.StatusOK, map[string]any{"change_id": res.ChangeID, "integration": s.integrationView(r.Context(), res.Snapshot, in)})
	}
}
