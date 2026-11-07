package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Vendors (DESIGN §8.3, §16.1). A vendor is vendors/<id>.yaml of the config tree (the object the
// registry loads, strictly decoded). The admin-only fields the registry does not model
// (display_name, webhook_verification, revision, endpoint creation times) live next to it in
// admin/vendors/<id>.json, which the registry loader ignores but the snapshot archive keeps.

func init() { registrars = append(registrars, (*Server).registerVendors) }

func (s *Server) registerVendors() {
	s.handle("listVendors", RoleViewer, false, "GET /vendors", s.listVendors)
	s.handle("createVendor", RoleAdmin, true, "POST /vendors", s.createVendor)
	s.handle("getVendor", RoleViewer, false, "GET /vendors/{vendor_id}", s.getVendor)
	s.handle("updateVendor", RoleAdmin, true, "PUT /vendors/{vendor_id}", s.updateVendor)
	s.handle("listVendorEndpoints", RoleViewer, false, "GET /vendors/{vendor_id}/endpoints", s.listVendorEndpoints)
	s.handle("createVendorEndpoint", RoleAdmin, true, "POST /vendors/{vendor_id}/endpoints", s.createVendorEndpoint)
	s.handle("getVendorEndpoint", RoleViewer, false, "GET /vendors/{vendor_id}/endpoints/{endpoint_ref}", s.getVendorEndpoint)
}

// ---- shared registry helpers (vendors, transformers) ----

// currentCache keeps the sources and snapshot of the latest archive version, so reads that need
// files (fixtures, admin metadata) do not rebuild the snapshot on every request.
type currentCache struct {
	mu      sync.Mutex
	version int64
	src     registry.Sources
	snap    *registry.Snapshot
}

var currentCaches sync.Map // *Server → *currentCache

// current returns the config tree nodes run (the latest archived snapshot, else the bootstrap
// tree) with its snapshot.
func (s *Server) current(ctx context.Context) (registry.Sources, *registry.Snapshot, error) {
	if s.d.Publisher == nil {
		return registry.Sources{}, nil, errors.New("no publisher")
	}
	version := int64(-1)
	if s.d.Publisher.Archive != nil {
		m, err := s.d.Publisher.Archive.LatestMeta(ctx)
		switch {
		case err == nil:
			version = m.Version
		case errors.Is(err, control.ErrNoSnapshot):
			version = 0
		default:
			return registry.Sources{}, nil, err
		}
	}
	v, _ := currentCaches.LoadOrStore(s, &currentCache{})
	c := v.(*currentCache)
	c.mu.Lock()
	defer c.mu.Unlock()
	if version >= 0 && c.snap != nil && c.version == version {
		return c.src, c.snap, nil
	}
	src, snap, err := s.d.Publisher.Current(ctx)
	if err != nil {
		return registry.Sources{}, nil, err
	}
	c.version, c.src, c.snap = version, src, snap
	return src, snap, nil
}

// pageOf returns the page of keys (already in list order) after the request's cursor.
func pageOf(r *http.Request, keys []string) (items []string, next *string, err error) {
	limit, cursor, err := page(r)
	if err != nil {
		return nil, nil, err
	}
	start := 0
	if len(cursor) > 0 {
		i := slices.Index(keys, cursor[0])
		if len(cursor) != 1 || i < 0 {
			return nil, nil, badRequest("", "cursor is not a cursor of this list")
		}
		start = i + 1
	}
	end := min(start+limit, len(keys))
	items = keys[start:end]
	if end < len(keys) {
		next = nextCursor(items[len(items)-1])
	}
	return items, next, nil
}

// matchQ is the contract's q filter: case-insensitive substring of any of the given fields.
func matchQ(r *http.Request, fields ...string) bool {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if q == "" {
		return true
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// inputError maps an edit error for operations whose contract has no 422: publish-check
// failures are reported as 400 VALIDATION_FAILED with the same details.
func inputError(err error) error {
	var problems registry.ConfigErrors
	if errors.As(err, &problems) {
		e := validationFailed(problems)
		e.Status, e.Code, e.Meta = http.StatusBadRequest, CodeValidationFailed, nil
		return e
	}
	return controlError(err)
}

// readMeta decodes the admin metadata file at path into v; a missing file leaves v unchanged.
func readMeta(get func(string) ([]byte, bool), path string, v any) error {
	b, ok := get(path)
	if !ok {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func srcGet(src registry.Sources) func(string) ([]byte, bool) {
	return func(p string) ([]byte, bool) {
		if src.Config == nil {
			return nil, false
		}
		b, err := fs.ReadFile(src.Config, p)
		return b, err == nil
	}
}

func putJSON(t control.Tree, path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	t.Put(path, append(b, '\n'))
	return nil
}

// preconditionError is If-Match not matching the current ETag (412).
type preconditionError struct{ current string }

func (e *preconditionError) Error() string { return "object changed since it was read" }

func preconditionFailed(current string) *Error {
	return &Error{Status: http.StatusPreconditionFailed, Code: CodePreconditionFailed, Message: "Object changed since it was read",
		Details: []Detail{}, Meta: map[string]any{"current_etag": current}}
}

func etag(revision int) string { return `"` + strconv.Itoa(revision) + `"` }

// ifMatch reports whether the request's If-Match (if any) matches current.
func ifMatch(r *http.Request, current string) bool {
	h := strings.TrimSpace(r.Header.Get("If-Match"))
	if h == "" || h == "*" {
		return true
	}
	for c := range strings.SplitSeq(h, ",") {
		c = strings.TrimPrefix(strings.TrimSpace(c), "W/")
		if c == current {
			return true
		}
	}
	return false
}

var (
	slugRE         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$`)
	endpointNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	endpointRefRE  = regexp.MustCompile(`^([a-z][a-z0-9_]{0,63})@([1-9][0-9]*)$`)
)

// ---- vendor model ----

type vendorMeta struct {
	DisplayName         string               `json:"display_name,omitempty"`
	WebhookVerification json.RawMessage      `json:"webhook_verification,omitempty"`
	Revision            int                  `json:"revision,omitempty"`
	EndpointsCreatedAt  map[string]time.Time `json:"endpoints_created_at,omitempty"`
}

func vendorFile(id string) string     { return "vendors/" + id + ".yaml" }
func vendorMetaFile(id string) string { return "admin/vendors/" + id + ".json" }

// authAliases maps older registry spellings to the contract's AuthScheme names.
var authAliases = map[string]string{"query": "query_param", "hmac": "hmac_signing", "oauth2_cc": "oauth2_client_credentials"}

var authSchemes = []string{"none", "static_headers", "bearer", "basic", "query_param", "hmac_signing", "oauth2_client_credentials", "mtls"}

type breakerMS struct {
	WindowMS            *int     `json:"window_ms,omitempty"`
	MinCalls            *int     `json:"min_calls,omitempty"`
	FailureRatio        *float64 `json:"failure_ratio,omitempty"`
	ConsecutiveTimeouts *int     `json:"consecutive_timeouts,omitempty"`
	OpenMS              *int     `json:"open_ms,omitempty"`
	OpenMaxMS           *int     `json:"open_max_ms,omitempty"`
	HalfOpenProbes      *int     `json:"half_open_probes,omitempty"`
}

type healthProbeMS struct {
	Endpoint string `json:"endpoint,omitempty"`
	Dry      *bool  `json:"dry,omitempty"`
	EveryMS  *int   `json:"every_ms,omitempty"`
}

type vendorDefaultsIn struct {
	RateLimit *struct {
		PerAccountRPS *float64 `json:"per_account_rps,omitempty"`
	} `json:"rate_limit,omitempty"`
	Breaker     *breakerMS     `json:"breaker,omitempty"`
	HealthProbe *healthProbeMS `json:"health_probe,omitempty"`
}

type vendorFieldsIn struct {
	DisplayName         string            `json:"display_name"`
	AuthScheme          string            `json:"auth_scheme"`
	Regions             map[string]string `json:"regions"`
	Defaults            *vendorDefaultsIn `json:"defaults"`
	WebhookVerification json.RawMessage   `json:"webhook_verification"`
	DocsURL             *string           `json:"docs_url"`
	Reason              string            `json:"reason"`
}

// seconds converts a contract millisecond duration to the registry's seconds.
func seconds(ptr string, ms *int) (int, error) {
	if ms == nil {
		return 0, nil
	}
	if *ms < 1000 || *ms%1000 != 0 {
		return 0, badRequest(ptr, "must be a whole number of seconds in ms, at least 1000")
	}
	return *ms / 1000, nil
}

func positive(ptr string, v *int) (int, error) {
	if v == nil {
		return 0, nil
	}
	if *v < 1 {
		return 0, badRequest(ptr, "must be at least 1")
	}
	return *v, nil
}

func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// apply validates the request fields and writes them onto v (the registry vendor) and m.
func (in *vendorFieldsIn) apply(v *registry.Vendor, m *vendorMeta) error {
	if n := len([]rune(in.DisplayName)); n == 0 || n > 128 {
		return badRequest("/display_name", "display_name is required (at most 128 characters)")
	}
	if !slices.Contains(authSchemes, in.AuthScheme) {
		return badRequest("/auth_scheme", "auth_scheme must be one of "+strings.Join(authSchemes, ", "))
	}
	if err := creds.ValidateScheme(in.AuthScheme); err != nil {
		return badRequest("/auth_scheme", "auth_scheme "+in.AuthScheme+" is not implemented by this build (implemented: "+strings.Join(creds.Schemes(), ", ")+")")
	}
	if len(in.Regions) == 0 {
		return badRequest("/regions", "regions needs at least one region")
	}
	for code, base := range in.Regions {
		if !httpURL(base) {
			return badRequest("/regions/"+code, "region base URL must be an absolute http(s) URL")
		}
	}
	docs := ""
	if in.DocsURL != nil {
		if !httpURL(*in.DocsURL) {
			return badRequest("/docs_url", "docs_url must be an absolute http(s) URL")
		}
		docs = *in.DocsURL
	}
	var d registry.VendorDefaults
	if in.Defaults != nil {
		if rl := in.Defaults.RateLimit; rl != nil && rl.PerAccountRPS != nil {
			rps := *rl.PerAccountRPS
			if rps < 0 || rps != float64(int(rps)) {
				return badRequest("/defaults/rate_limit/per_account_rps", "per_account_rps must be a whole number ≥ 0")
			}
			d.RateLimit.PerAccountRPS = int(rps)
		}
		if b := in.Defaults.Breaker; b != nil {
			var err error
			const at = "/defaults/breaker/"
			if d.Breaker.WindowS, err = seconds(at+"window_ms", b.WindowMS); err != nil {
				return err
			}
			if d.Breaker.OpenS, err = seconds(at+"open_ms", b.OpenMS); err != nil {
				return err
			}
			if d.Breaker.OpenMaxS, err = seconds(at+"open_max_ms", b.OpenMaxMS); err != nil {
				return err
			}
			if d.Breaker.MinCalls, err = positive(at+"min_calls", b.MinCalls); err != nil {
				return err
			}
			if d.Breaker.ConsecutiveTimeouts, err = positive(at+"consecutive_timeouts", b.ConsecutiveTimeouts); err != nil {
				return err
			}
			if d.Breaker.HalfOpenProbes, err = positive(at+"half_open_probes", b.HalfOpenProbes); err != nil {
				return err
			}
			if b.FailureRatio != nil {
				if *b.FailureRatio < 0 || *b.FailureRatio > 1 {
					return badRequest(at+"failure_ratio", "failure_ratio must be within 0..1")
				}
				d.Breaker.FailureRatio = *b.FailureRatio
			}
		}
		if hp := in.Defaults.HealthProbe; hp != nil {
			if !endpointRefRE.MatchString(hp.Endpoint) {
				return badRequest("/defaults/health_probe/endpoint", "health_probe.endpoint must be <name>@<version>")
			}
			every, err := seconds("/defaults/health_probe/every_ms", hp.EveryMS)
			if err != nil {
				return err
			}
			d.HealthProbe = &registry.HealthProbe{Endpoint: hp.Endpoint, Dry: hp.Dry != nil && *hp.Dry, EveryS: every}
		}
	}
	var wv json.RawMessage
	if len(in.WebhookVerification) > 0 && string(in.WebhookVerification) != "null" {
		var w struct {
			Scheme      string   `json:"scheme"`
			Header      string   `json:"header,omitempty"`
			MaxSkewMS   *int     `json:"max_skew_ms,omitempty"`
			IPAllowlist []string `json:"ip_allowlist,omitempty"`
		}
		dec := json.NewDecoder(strings.NewReader(string(in.WebhookVerification)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&w); err != nil {
			return badRequest("/webhook_verification", "webhook_verification: "+err.Error())
		}
		if !slices.Contains([]string{"none", "hmac_sha256", "basic", "ip_allowlist"}, w.Scheme) {
			return badRequest("/webhook_verification/scheme", "scheme must be none, hmac_sha256, basic or ip_allowlist")
		}
		if w.MaxSkewMS != nil && *w.MaxSkewMS < 0 {
			return badRequest("/webhook_verification/max_skew_ms", "max_skew_ms must be ≥ 0")
		}
		wv, _ = json.Marshal(w)
	}
	v.Auth, v.Regions, v.Defaults, v.DocsURL = in.AuthScheme, in.Regions, d, docs
	m.DisplayName, m.WebhookVerification = in.DisplayName, wv
	return nil
}

// marshalVendor renders the registry vendor file.
func marshalVendor(v *registry.Vendor) ([]byte, error) {
	if v.Endpoints == nil {
		v.Endpoints = map[string]registry.Endpoint{}
	}
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append([]byte("# Managed by the Hub admin API (DESIGN §8.3, §16.1). Admin fields: admin/vendors/"+v.Vendor+".json.\n"), b...), nil
}

func (m *vendorMeta) revision() int { return max(m.Revision, 1) }

func (m *vendorMeta) displayName(id string) string {
	if m.DisplayName == "" {
		return id
	}
	return m.DisplayName
}

func contractAuth(a string) string {
	if c, ok := authAliases[a]; ok {
		return c
	}
	return a
}

func defaultsView(d registry.VendorDefaults) map[string]any {
	out := map[string]any{"rate_limit": map[string]any{"per_account_rps": d.RateLimit.PerAccountRPS}}
	b := map[string]any{}
	set := func(k string, v int, scale int) {
		if v > 0 {
			b[k] = v * scale
		}
	}
	set("window_ms", d.Breaker.WindowS, 1000)
	set("min_calls", d.Breaker.MinCalls, 1)
	set("consecutive_timeouts", d.Breaker.ConsecutiveTimeouts, 1)
	set("open_ms", d.Breaker.OpenS, 1000)
	set("open_max_ms", d.Breaker.OpenMaxS, 1000)
	set("half_open_probes", d.Breaker.HalfOpenProbes, 1)
	if d.Breaker.FailureRatio > 0 {
		b["failure_ratio"] = d.Breaker.FailureRatio
	}
	out["breaker"] = b
	if hp := d.HealthProbe; hp != nil {
		p := map[string]any{"endpoint": hp.Endpoint, "dry": hp.Dry}
		if hp.EveryS > 0 {
			p["every_ms"] = hp.EveryS * 1000
		}
		out["health_probe"] = p
	}
	return out
}

func endpointView(name string, ep registry.Endpoint, created time.Time) map[string]any {
	m := endpointRefRE.FindStringSubmatch(name)
	version := 0
	base := name
	if m != nil {
		base = m[1]
		version, _ = strconv.Atoi(m[2])
	}
	var batch any
	if ep.BatchMax > 0 {
		batch = ep.BatchMax
	}
	var response any
	if ep.ResponseSchema != nil {
		response = ep.ResponseSchema
	}
	return map[string]any{
		"endpoint_ref": name, "name": base, "version": version, "method": ep.Method, "path": ep.Path,
		"timeout_ms": ep.TimeoutMS, "connect_timeout_ms": ep.ConnectTimeoutMS, "idempotent": ep.Idempotent,
		"batch_max": batch, "request_schema": requestSchemaView(ep.RequestSchema), "response_schema": response,
		"created_at": created.UTC(),
	}
}

// requestSchemaView is the contract's EndpointRequestSchema: the parts the endpoint declares.
func requestSchemaView(rs *registry.EndpointRequestSchema) map[string]any {
	out := map[string]any{}
	if rs == nil {
		return out
	}
	for name, s := range map[string]*registry.JSONSchema{"path": rs.Path, "query": rs.Query, "headers": rs.Headers, "body": rs.Body} {
		if s != nil {
			out[name] = s.Doc
		}
	}
	return out
}

// endpointRequestSchema reads the contract's EndpointRequestSchema (required, ADR-0023): each
// part must be a JSON Schema object that compiles (400 at its pointer otherwise).
func endpointRequestSchema(raw json.RawMessage) (*registry.EndpointRequestSchema, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, badRequest("/request_schema", "request_schema is required ({} when the endpoint takes nothing besides method and path)")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var in struct {
		Path    map[string]any `json:"path"`
		Query   map[string]any `json:"query"`
		Headers map[string]any `json:"headers"`
		Body    map[string]any `json:"body"`
	}
	if err := dec.Decode(&in); err != nil {
		return nil, badRequest("/request_schema", "request_schema must be an object with optional path, query, headers and body schemas: "+err.Error())
	}
	rs := &registry.EndpointRequestSchema{}
	for _, part := range []struct {
		name string
		doc  map[string]any
		dst  **registry.JSONSchema
	}{{"path", in.Path, &rs.Path}, {"query", in.Query, &rs.Query}, {"headers", in.Headers, &rs.Headers}, {"body", in.Body, &rs.Body}} {
		if part.doc == nil {
			continue
		}
		s := registry.NewJSONSchema(part.doc)
		if err := s.Compile("request_schema/" + part.name); err != nil {
			return nil, &Error{Status: http.StatusBadRequest, Code: CodeValidationFailed, Message: "request_schema." + part.name + " does not compile",
				Details: []Detail{{Pointer: "/request_schema/" + part.name, Keyword: registry.CheckSchemasCompile, Message: err.Error()}}}
		}
		*part.dst = s
	}
	return rs, nil
}

// endpointOrder sorts endpoint refs by name, then version.
func endpointOrder(refs []string) []string {
	slices.SortFunc(refs, func(a, b string) int {
		ma, mb := endpointRefRE.FindStringSubmatch(a), endpointRefRE.FindStringSubmatch(b)
		if ma == nil || mb == nil || ma[1] != mb[1] {
			return strings.Compare(a, b)
		}
		va, _ := strconv.Atoi(ma[2])
		vb, _ := strconv.Atoi(mb[2])
		return va - vb
	})
	return refs
}

// vendorView is the contract's Vendor.
func (s *Server) vendorView(ctx context.Context, snap *registry.Snapshot, v *registry.Vendor, m vendorMeta) map[string]any {
	created, updated := s.objectTimes(ctx, "vendor", v.Vendor, snap.BuiltAt)
	endpoints := []map[string]any{}
	for _, name := range endpointOrder(slices.Collect(maps.Keys(v.Endpoints))) {
		at, ok := m.EndpointsCreatedAt[name]
		if !ok {
			at = created
		}
		endpoints = append(endpoints, endpointView(name, v.Endpoints[name], at))
	}
	var wv any
	if len(m.WebhookVerification) > 0 {
		wv = m.WebhookVerification
	}
	regions := v.Regions
	if regions == nil {
		regions = map[string]string{}
	}
	return map[string]any{
		"vendor_id": v.Vendor, "display_name": m.displayName(v.Vendor), "auth_scheme": contractAuth(v.Auth), "regions": regions,
		"defaults": defaultsView(v.Defaults), "webhook_verification": wv, "docs_url": nullable(v.DocsURL),
		"endpoints": endpoints, "revision": m.revision(), "created_at": created, "updated_at": updated,
	}
}

// vendorAudit is what the change log records of a vendor (no timestamps, no secrets: vendors hold none).
func vendorAudit(v *registry.Vendor, m vendorMeta) map[string]any {
	return map[string]any{"display_name": m.displayName(v.Vendor), "auth_scheme": contractAuth(v.Auth), "regions": v.Regions,
		"defaults": defaultsView(v.Defaults), "webhook_verification": m.WebhookVerification, "docs_url": v.DocsURL, "revision": m.revision()}
}

func (s *Server) loadVendor(ctx context.Context, id string) (*registry.Snapshot, *registry.Vendor, vendorMeta, error) {
	src, snap, err := s.current(ctx)
	if err != nil {
		return nil, nil, vendorMeta{}, err
	}
	v, ok := snap.Vendors[id]
	if !ok {
		return nil, nil, vendorMeta{}, notFound("Vendor " + id)
	}
	var m vendorMeta
	if err := readMeta(srcGet(src), vendorMetaFile(id), &m); err != nil {
		return nil, nil, vendorMeta{}, err
	}
	return snap, v, m, nil
}

// ---- handlers ----

func (s *Server) listVendors(w http.ResponseWriter, r *http.Request) {
	src, snap, err := s.current(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	metas := map[string]vendorMeta{}
	var ids []string
	for _, id := range slices.Sorted(maps.Keys(snap.Vendors)) {
		var m vendorMeta
		if err := readMeta(srcGet(src), vendorMetaFile(id), &m); err != nil {
			s.fail(w, r, err)
			return
		}
		if matchQ(r, id, m.displayName(id)) {
			metas[id] = m
			ids = append(ids, id)
		}
	}
	pg, next, err := pageOf(r, ids)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	accounts := map[string]int{}
	for _, a := range snap.Accounts {
		accounts[a.Vendor]++
	}
	items := make([]map[string]any, 0, len(pg))
	for _, id := range pg {
		v, m := snap.Vendors[id], metas[id]
		_, updated := s.objectTimes(r.Context(), "vendor", id, snap.BuiltAt)
		items = append(items, map[string]any{
			"vendor_id": id, "display_name": m.displayName(id), "auth_scheme": contractAuth(v.Auth),
			"regions": slices.Sorted(maps.Keys(v.Regions)), "endpoint_count": len(v.Endpoints), "account_count": accounts[id],
			"revision": m.revision(), "updated_at": updated,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) getVendor(w http.ResponseWriter, r *http.Request) {
	snap, v, m, err := s.loadVendor(r.Context(), r.PathValue("vendor_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(m.revision()))
	writeJSON(w, http.StatusOK, s.vendorView(r.Context(), snap, v, m))
}

func (s *Server) createVendor(w http.ResponseWriter, r *http.Request) {
	var body struct {
		VendorID string `json:"vendor_id"`
		vendorFieldsIn
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
	if !slugRE.MatchString(body.VendorID) {
		s.fail(w, r, badRequest("/vendor_id", "vendor_id must match "+slugRE.String()))
		return
	}
	v := &registry.Vendor{Vendor: body.VendorID, Endpoints: map[string]registry.Endpoint{}}
	m := vendorMeta{Revision: 1}
	if err := body.apply(v, &m); err != nil {
		s.fail(w, r, err)
		return
	}
	if s.d.Publisher == nil {
		s.fail(w, r, errors.New("no publisher"))
		return
	}
	id := body.VendorID
	res, err := s.d.Publisher.Edit(r.Context(), func(t control.Tree, snap *registry.Snapshot) (control.Change, error) {
		if _, exists := snap.Vendors[id]; exists {
			return control.Change{}, fmt.Errorf("%w: vendor %s exists", control.ErrConflict, id)
		}
		if _, exists := t.Get(vendorFile(id)); exists {
			return control.Change{}, fmt.Errorf("%w: %s exists", control.ErrConflict, vendorFile(id))
		}
		doc, err := marshalVendor(v)
		if err != nil {
			return control.Change{}, err
		}
		t.Put(vendorFile(id), doc)
		if err := putJSON(t, vendorMetaFile(id), m); err != nil {
			return control.Change{}, err
		}
		return control.Change{Actor: principal(r).Actor(), Action: "createVendor", ObjectType: "vendor", ObjectID: id, Reason: reason,
			After: vendorAudit(v, m)}, nil
	})
	if err != nil {
		s.fail(w, r, inputError(err))
		return
	}
	w.Header().Set("ETag", etag(m.revision()))
	writeJSON(w, http.StatusCreated, map[string]any{"change_id": res.ChangeID, "vendor": s.vendorView(r.Context(), res.Snapshot, res.Snapshot.Vendors[id], m)})
}

func (s *Server) updateVendor(w http.ResponseWriter, r *http.Request) {
	var body vendorFieldsIn
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
	id := r.PathValue("vendor_id")
	var m vendorMeta
	res, err := s.d.Publisher.Edit(r.Context(), func(t control.Tree, snap *registry.Snapshot) (control.Change, error) {
		old, ok := snap.Vendors[id]
		if !ok {
			return control.Change{}, fmt.Errorf("%w: vendor %q", control.ErrNotFound, id)
		}
		if err := readMeta(t.Get, vendorMetaFile(id), &m); err != nil {
			return control.Change{}, err
		}
		if cur := etag(m.revision()); !ifMatch(r, cur) {
			return control.Change{}, &preconditionError{current: cur}
		}
		before := vendorAudit(old, m)
		v := &registry.Vendor{Vendor: id, Endpoints: old.Endpoints}
		if err := body.apply(v, &m); err != nil {
			return control.Change{}, err
		}
		m.Revision = m.revision() + 1
		doc, err := marshalVendor(v)
		if err != nil {
			return control.Change{}, err
		}
		t.Put(vendorFile(id), doc)
		if err := putJSON(t, vendorMetaFile(id), m); err != nil {
			return control.Change{}, err
		}
		return control.Change{Actor: principal(r).Actor(), Action: "updateVendor", ObjectType: "vendor", ObjectID: id, Reason: reason,
			Before: before, After: vendorAudit(v, m)}, nil
	})
	var pre *preconditionError
	if errors.As(err, &pre) {
		s.fail(w, r, preconditionFailed(pre.current))
		return
	}
	if err != nil {
		s.fail(w, r, inputError(err))
		return
	}
	w.Header().Set("ETag", etag(m.revision()))
	writeJSON(w, http.StatusOK, map[string]any{"change_id": res.ChangeID, "vendor": s.vendorView(r.Context(), res.Snapshot, res.Snapshot.Vendors[id], m)})
}

func (s *Server) listVendorEndpoints(w http.ResponseWriter, r *http.Request) {
	snap, v, m, err := s.loadVendor(r.Context(), r.PathValue("vendor_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pg, next, err := pageOf(r, endpointOrder(slices.Collect(maps.Keys(v.Endpoints))))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	created, _ := s.objectTimes(r.Context(), "vendor", v.Vendor, snap.BuiltAt)
	items := make([]map[string]any, 0, len(pg))
	for _, name := range pg {
		at, ok := m.EndpointsCreatedAt[name]
		if !ok {
			at = created
		}
		items = append(items, endpointView(name, v.Endpoints[name], at))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) getVendorEndpoint(w http.ResponseWriter, r *http.Request) {
	snap, v, m, err := s.loadVendor(r.Context(), r.PathValue("vendor_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	name := r.PathValue("endpoint_ref")
	ep, ok := v.Endpoints[name]
	if !ok {
		s.fail(w, r, notFound("Endpoint "+v.Vendor+"."+name))
		return
	}
	at, ok := m.EndpointsCreatedAt[name]
	if !ok {
		at, _ = s.objectTimes(r.Context(), "vendor", v.Vendor, snap.BuiltAt)
	}
	writeJSON(w, http.StatusOK, endpointView(name, ep, at))
}

func (s *Server) createVendorEndpoint(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name             string          `json:"name"`
		Method           string          `json:"method"`
		Path             string          `json:"path"`
		TimeoutMS        *int            `json:"timeout_ms"`
		ConnectTimeoutMS *int            `json:"connect_timeout_ms"`
		Idempotent       *bool           `json:"idempotent"`
		BatchMax         *int            `json:"batch_max"`
		Stream           bool            `json:"stream"`
		RequestSchema    json.RawMessage `json:"request_schema"`
		ResponseSchema   json.RawMessage `json:"response_schema"`
		Reason           string          `json:"reason"`
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
	ep := registry.Endpoint{Method: body.Method, Path: body.Path, TimeoutMS: 3000, ConnectTimeoutMS: 500}
	switch {
	case !endpointNameRE.MatchString(body.Name):
		err = badRequest("/name", "name must match "+endpointNameRE.String())
	case !slices.Contains([]string{"GET", "POST", "PUT", "PATCH", "DELETE"}, body.Method):
		err = badRequest("/method", "method must be GET, POST, PUT, PATCH or DELETE")
	case !strings.HasPrefix(body.Path, "/"):
		err = badRequest("/path", "path must start with /")
	case body.Idempotent == nil:
		err = badRequest("/idempotent", "idempotent is required")
	case body.TimeoutMS != nil && *body.TimeoutMS < 1:
		err = badRequest("/timeout_ms", "timeout_ms must be at least 1")
	case body.ConnectTimeoutMS != nil && *body.ConnectTimeoutMS < 1:
		err = badRequest("/connect_timeout_ms", "connect_timeout_ms must be at least 1")
	case body.BatchMax != nil && *body.BatchMax < 1:
		err = badRequest("/batch_max", "batch_max must be at least 1")
	case body.Stream:
		err = badRequest("/stream", "streaming endpoints are not supported by this build")
	}
	if err == nil {
		ep.RequestSchema, err = endpointRequestSchema(body.RequestSchema)
	}
	if err == nil && len(body.ResponseSchema) > 0 && string(body.ResponseSchema) != "null" {
		if err = json.Unmarshal(body.ResponseSchema, &ep.ResponseSchema); err != nil || ep.ResponseSchema == nil {
			err = badRequest("/response_schema", "response_schema must be a JSON Schema object or null")
		}
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ep.Idempotent = *body.Idempotent
	if body.TimeoutMS != nil {
		ep.TimeoutMS = *body.TimeoutMS
	}
	if body.ConnectTimeoutMS != nil {
		ep.ConnectTimeoutMS = *body.ConnectTimeoutMS
	}
	if body.BatchMax != nil {
		ep.BatchMax = *body.BatchMax
	}
	if s.d.Publisher == nil {
		s.fail(w, r, errors.New("no publisher"))
		return
	}
	id := r.PathValue("vendor_id")
	var ref string
	var created time.Time
	res, err := s.d.Publisher.Edit(r.Context(), func(t control.Tree, snap *registry.Snapshot) (control.Change, error) {
		old, ok := snap.Vendors[id]
		if !ok {
			return control.Change{}, fmt.Errorf("%w: vendor %q", control.ErrNotFound, id)
		}
		var m vendorMeta
		if err := readMeta(t.Get, vendorMetaFile(id), &m); err != nil {
			return control.Change{}, err
		}
		n := 1
		for k := range old.Endpoints {
			if mm := endpointRefRE.FindStringSubmatch(k); mm != nil && mm[1] == body.Name {
				v, _ := strconv.Atoi(mm[2])
				n = max(n, v+1)
			}
		}
		ref = body.Name + "@" + strconv.Itoa(n)
		v := *old
		v.Endpoints = maps.Clone(old.Endpoints)
		if v.Endpoints == nil {
			v.Endpoints = map[string]registry.Endpoint{}
		}
		v.Endpoints[ref] = ep
		doc, err := marshalVendor(&v)
		if err != nil {
			return control.Change{}, err
		}
		t.Put(vendorFile(id), doc)
		if m.EndpointsCreatedAt == nil {
			m.EndpointsCreatedAt = map[string]time.Time{}
		}
		created = s.d.Now().UTC().Truncate(time.Millisecond)
		m.EndpointsCreatedAt[ref] = created
		if err := putJSON(t, vendorMetaFile(id), m); err != nil {
			return control.Change{}, err
		}
		return control.Change{Actor: principal(r).Actor(), Action: "createVendorEndpoint", ObjectType: "vendor_endpoint", ObjectID: id + "." + ref,
			Reason: reason, After: endpointView(ref, ep, created)}, nil
	})
	if err != nil {
		s.fail(w, r, inputError(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"change_id": res.ChangeID, "endpoint": endpointView(ref, res.Snapshot.Vendors[id].Endpoints[ref], created)})
}
