package edge

import (
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

type upstreamRequest struct {
	Method       string            `json:"method"`
	URL          string            `json:"url"`
	Headers      map[string]string `json:"headers"`
	Body         any               `json:"body,omitempty"`
	BodyEncoding string            `json:"body_encoding,omitempty"`
}

type dryRunResult struct {
	RequestID       string          `json:"request_id"`
	DryRun          bool            `json:"dry_run"`
	Operation       string          `json:"operation"`
	UpstreamRequest upstreamRequest `json:"upstream_request"`
	Redacted        []string        `json:"redacted"`
	Warnings        []string        `json:"warnings"`
}

// dryRun shows the upstream request the Hub would send, with credentials masked, and sends
// nothing: no upstream call, no queue entry, no idempotency record (contracts: dryRunIntegration,
// the documented, scope-gated vendor-shape exception).
func (s *Server) dryRun(w http.ResponseWriter, r *http.Request) {
	op := r.URL.Query().Get("operation")
	if op == "" {
		op = "default"
	}
	p, ce := s.prepare(r, ScopeDryRun, op)
	if ce != nil {
		s.fail(w, r, p, ce)
		return
	}
	in := p.input
	in.UUID = func() string { return "00000000-0000-4000-8000-000000000000" } // stable output, no randomness
	start := s.deps.Now()
	plain, err := p.request.BuildRequest(in)
	if err != nil {
		s.fail(w, r, p, s.transformError(r, p, err))
		return
	}
	masked, err := p.request.DryRun(in)
	p.timings.Observe(telemetry.StageTransform, start)
	if err != nil {
		s.fail(w, r, p, s.transformError(r, p, err))
		return
	}

	out := dryRunResult{
		RequestID: RequestID(r.Context()), DryRun: true, Operation: op,
		UpstreamRequest: upstreamRequest{Method: masked.Method, URL: upstreamURL(p.baseURL, masked), Headers: masked.Headers, Body: masked.Body},
		Redacted:        redactedPaths(plain, masked), Warnings: []string{},
	}
	if out.UpstreamRequest.Headers == nil {
		out.UpstreamRequest.Headers = map[string]string{}
	}
	if _, isText := masked.Body.(string); isText {
		out.UpstreamRequest.BodyEncoding = "text"
	}
	if p.res.Integration.Paused {
		out.Warnings = append(out.Warnings, "integration is paused: invocations currently return 503 INTEGRATION_PAUSED")
	}
	p.trail.AddJSON(audit.RequestTransformed, 0, out.UpstreamRequest, map[string]any{"dry_run": true, "transformer": p.request.Ref()})
	writeJSON(w, http.StatusOK, out)
	s.finish(r, p, http.StatusOK)
}

// upstreamURL joins the account's regional base URL, the path and the sorted query.
func upstreamURL(base string, req core.VendorRequest) string {
	u := strings.TrimSuffix(base, "/") + req.Path
	if len(req.Query) > 0 {
		q := url.Values{}
		for k, v := range req.Query {
			q.Set(k, v)
		}
		u += "?" + q.Encode()
	}
	return u
}

// redactedPaths lists what DryRun masked: "headers.<name>", "query.<name>", "body<json pointer>".
func redactedPaths(plain, masked core.VendorRequest) []string {
	out := []string{}
	for _, k := range slices.Sorted(maps.Keys(masked.Headers)) {
		if masked.Headers[k] != plain.Headers[k] {
			out = append(out, "headers."+k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(masked.Query)) {
		if masked.Query[k] != plain.Query[k] {
			out = append(out, "query."+k)
		}
	}
	diffBody("", plain.Body, masked.Body, &out)
	return out
}

func diffBody(ptr string, plain, masked any, out *[]string) {
	switch m := masked.(type) {
	case map[string]any:
		pm, _ := plain.(map[string]any)
		for _, k := range slices.Sorted(maps.Keys(m)) {
			diffBody(ptr+"/"+strings.NewReplacer("~", "~0", "/", "~1").Replace(k), pm[k], m[k], out)
		}
	case []any:
		pl, _ := plain.([]any)
		for i := range m {
			var pv any
			if i < len(pl) {
				pv = pl[i]
			}
			diffBody(ptr+"/"+strconv.Itoa(i), pv, m[i], out)
		}
	case string:
		if ps, _ := plain.(string); ps != m {
			*out = append(*out, "body"+ptr)
		}
	}
}
