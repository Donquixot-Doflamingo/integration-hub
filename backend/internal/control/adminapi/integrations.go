package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
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

// integrationView is the contract's Integration (without the active version in full). fallback
// is the time reported for an integration the change log does not know.
func (s *Server) integrationView(ctx context.Context, in *registry.Integration, hasDraft bool, fallback time.Time) map[string]any {
	latest := latestVersion(in)
	if hasDraft {
		latest++
	}
	created, updated := s.objectTimes(ctx, "integration", in.Integration, fallback)
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
		"integration_id": in.Integration, "owner_tenant": in.OwnerTenant, "interactive": in.Interactive,
		"description": nullable(in.Description), "grants": grants, "moves_money_or_quota": in.MovesMoneyOrQuota,
		"active_version": active, "latest_version": latestV, "has_draft": hasDraft,
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

// catalog is every integration the admin knows: the snapshot's (what nodes run) plus the
// created ones without a published version (Postgres), with their drafts.
type catalog struct {
	snap    *registry.Snapshot
	all     map[string]*registry.Integration
	pending map[string]control.PendingIntegration
	drafts  map[string]control.Draft
}

func (c *catalog) draft(id string) (control.Draft, bool) {
	d, ok := c.drafts[id]
	return d, ok
}

// fallback is the time reported for an integration the change log does not know.
func (c *catalog) fallback(id string) time.Time {
	if p, ok := c.pending[id]; ok {
		return p.CreatedAt
	}
	return c.snap.BuiltAt
}

// pendingIntegration turns a pending row into an integration without versions.
func pendingIntegration(p control.PendingIntegration) (*registry.Integration, error) {
	var f registry.IntegrationFile
	if err := yaml.Unmarshal(p.Header, &f); err != nil {
		return nil, fmt.Errorf("pending integration %s: %w", p.Integration, err)
	}
	return &registry.Integration{IntegrationFile: f, Versions: map[int]*registry.IntegrationVersion{},
		File: "integrations/" + p.Integration + "/integration.yaml", VersionFiles: map[int]string{}}, nil
}

func (s *Server) catalog(ctx context.Context) (*catalog, error) {
	snap, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	c := &catalog{snap: snap, all: maps.Clone(snap.Integrations), pending: map[string]control.PendingIntegration{}, drafts: map[string]control.Draft{}}
	if s.d.Pool == nil {
		return c, nil
	}
	if c.drafts, err = control.ListDrafts(ctx, s.d.Pool); err != nil {
		return nil, err
	}
	pending, err := control.ListPending(ctx, s.d.Pool)
	if err != nil {
		return nil, err
	}
	for id, p := range pending {
		if _, published := c.all[id]; published {
			continue // its first publish succeeded; the row is a leftover
		}
		in, err := pendingIntegration(p)
		if err != nil {
			return nil, err
		}
		c.all[id], c.pending[id] = in, p
	}
	return c, nil
}

// visibleIntegration loads the integration of the path, 404 outside the caller's scope.
func (s *Server) visibleIntegration(r *http.Request) (*catalog, *registry.Integration, error) {
	c, err := s.catalog(r.Context())
	if err != nil {
		return nil, nil, err
	}
	in, ok := c.all[r.PathValue("integration_id")]
	if !ok || !sees(principal(r), in) {
		return c, nil, notFound("Integration")
	}
	return c, in, nil
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
	c, err := s.catalog(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	needle := strings.ToLower(q.Get("q"))
	var rows []integrationRow
	for _, id := range slices.Sorted(maps.Keys(c.all)) {
		rows = append(rows, integrationRow{id: id, in: c.all[id]})
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
		_, hasDraft := c.draft(row.id)
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
	s.fillUpdatedAt(r.Context(), c, items)
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
	ops := []string{}
	if v := in.Active(); v != nil {
		ops = v.OperationNames()
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
	return map[string]any{"integration_id": in.Integration, "owner_tenant": in.OwnerTenant, "interactive": in.Interactive, "operations": ops,
		"description": nullable(in.Description), "active_version": active, "latest_version": latest, "has_draft": hasDraft,
		"paused": in.Paused, "delivery_mode": mode, "targets": tg}
}

// fillUpdatedAt sets updated_at of each summary from the change log in one query.
func (s *Server) fillUpdatedAt(ctx context.Context, c *catalog, items []map[string]any) {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it["integration_id"].(string)
		it["updated_at"] = c.fallback(ids[i]).UTC()
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
	c, in, err := s.visibleIntegration(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_, hasDraft := c.draft(in.Integration)
	view := s.integrationView(r.Context(), in, hasDraft, c.fallback(in.Integration))
	if v := in.Active(); v != nil {
		meta := s.versionMeta(r.Context(), in.Integration)
		view["active"] = publishedVersionView(c.snap, in, in.ActiveVersion, v, meta[in.ActiveVersion])
	}
	w.Header().Set("ETag", integrationETag(in, hasDraft))
	writeJSON(w, http.StatusOK, view)
}

// versionMeta is what the change log knows about a published version.
type versionMeta struct {
	Author          string     `json:"author"` // the draft's author when a draft was published
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
		m.Author, m.BasedOn, m.CopiedFrom, m.CreatedAt, m.known = a.Author, a.BasedOn, a.CopiedFrom, a.CreatedAt, true
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
	author := v.Publish.Author
	if m.Author != "" {
		author = m.Author
	}
	return map[string]any{"integration_id": in.Integration, "version": n, "state": "published", "spec": renderSpec(v),
		"based_on": basedOn, "copied_from_version": m.CopiedFrom, "author": author, "reason": v.Publish.Reason,
		"created_at": created, "published_at": published, "published_by": by, "snapshot_version": snapV}
}

// versionSummary drops the spec of a version view (IntegrationVersionSummary).
func versionSummary(view map[string]any, active int) map[string]any {
	return map[string]any{"integration_id": view["integration_id"], "version": view["version"], "state": view["state"],
		"based_on": view["based_on"], "author": view["author"], "reason": view["reason"], "created_at": view["created_at"],
		"published_at": view["published_at"], "is_active": view["version"] == active && view["state"] == "published"}
}

// draftView is the contract's IntegrationVersion of a draft: always the next version.
func draftView(in *registry.Integration, d control.Draft) (map[string]any, error) {
	v, err := parseVersionYAML(d.Spec)
	if err != nil {
		return nil, err
	}
	return map[string]any{"integration_id": in.Integration, "version": latestVersion(in) + 1, "state": "draft", "spec": renderSpec(v),
		"based_on": d.BasedOn, "copied_from_version": d.CopiedFrom, "author": d.Author, "reason": d.Reason,
		"created_at": d.CreatedAt, "published_at": nil, "published_by": nil, "snapshot_version": nil}, nil
}

// versionViews lists every version of in (newest first: the draft, then the published ones)
// as full IntegrationVersion views.
func (s *Server) versionViews(ctx context.Context, c *catalog, in *registry.Integration) ([]map[string]any, error) {
	var out []map[string]any
	if d, ok := c.draft(in.Integration); ok {
		v, err := draftView(in, d)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	meta := s.versionMeta(ctx, in.Integration)
	for _, n := range slices.Backward(slices.Sorted(maps.Keys(in.Versions))) {
		out = append(out, publishedVersionView(c.snap, in, n, in.Versions[n], meta[n]))
	}
	return out, nil
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
	c, in, err := s.visibleIntegration(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	views, err := s.versionViews(r.Context(), c, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items := []map[string]any{}
	var next *string
	for _, v := range views {
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
func (s *Server) findVersion(ctx context.Context, c *catalog, in *registry.Integration, n int) (map[string]any, error) {
	views, err := s.versionViews(ctx, c, in)
	if err != nil {
		return nil, err
	}
	for _, v := range views {
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
	c, in, err := s.visibleIntegration(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v, err := s.findVersion(r.Context(), c, in, n)
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
	c, in, err := s.visibleIntegration(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var specs [2]map[string]any
	for i, n := range ns {
		v, err := s.findVersion(r.Context(), c, in, n)
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
		writeJSON(w, http.StatusOK, map[string]any{"change_id": res.ChangeID, "integration": s.changedIntegration(r.Context(), res.Snapshot, r.PathValue("integration_id"))})
	}
}

// changedIntegration is the Integration of a mutation response, read from the snapshot the
// mutation produced.
func (s *Server) changedIntegration(ctx context.Context, snap *registry.Snapshot, id string) map[string]any {
	hasDraft := false
	if s.d.Pool != nil {
		_, err := control.GetDraft(ctx, s.d.Pool, id)
		hasDraft = err == nil
	}
	return s.integrationView(ctx, snap.Integrations[id], hasDraft, snap.BuiltAt)
}
