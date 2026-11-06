package edge

import (
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/transform"
)

type dryRunResult struct {
	RequestID       string                `json:"request_id"`
	DryRun          bool                  `json:"dry_run"`
	Operation       string                `json:"operation"`
	UpstreamRequest delivery.AuditRequest `json:"upstream_request"`
	Redacted        []string              `json:"redacted"`
	Warnings        []string              `json:"warnings"`
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
	tagIntegration(r.Context(), p)
	if ce != nil {
		s.fail(w, r, p, ce)
		return
	}
	in, cred, ce := s.attempter.Input(r.Context(), p.caller.Snap, p.res, p.body, p.hdr, p.trail)
	if ce != nil {
		s.fail(w, r, p, ce)
		return
	}
	in.Session = p.sessionData()                                              // a later step's dry-run reads its session (X-Hub-Session)
	in.UUID = func() string { return "00000000-0000-4000-8000-000000000000" } // stable output, no randomness
	prog := p.caller.Snap.Programs[p.res.Target.Transformer.Request]
	start := s.deps.Now()
	plain, err := prog.BuildRequest(in)
	if err != nil {
		s.fail(w, r, p, s.attempter.TransformError(r.Context(), p.integration.Integration, prog.Ref(), err))
		return
	}
	masked, err := prog.DryRun(in)
	p.timings.Observe(telemetry.StageTransform, start)
	if err != nil {
		s.fail(w, r, p, s.attempter.TransformError(r.Context(), p.integration.Integration, prog.Ref(), err))
		return
	}
	// The vendor's auth scheme (bearer, basic) adds headers after the transformer: show them,
	// masked, so the output is what would really be sent.
	if err := creds.ApplyVendor(p.caller.Snap.Vendors[p.res.Target.Vendor], cred, &plain); err != nil {
		s.log.ErrorContext(r.Context(), "auth scheme failed", slog.String("integration", p.integration.Integration), slog.String("error", err.Error()))
		s.fail(w, r, p, internalError())
		return
	}
	for k := range plain.Headers {
		if _, ok := masked.Headers[k]; !ok {
			if masked.Headers == nil {
				masked.Headers = map[string]string{}
			}
			masked.Headers[k] = transform.Redacted
		}
	}

	out := dryRunResult{
		RequestID: RequestID(r.Context()), DryRun: true, Operation: op,
		UpstreamRequest: delivery.AuditRequest{Method: masked.Method, URL: delivery.UpstreamURL(p.caller.Snap.Vendors[p.res.Target.Vendor].Regions[p.caller.Snap.Accounts[p.res.Target.Account].Region], masked), Headers: masked.Headers, Body: masked.Body},
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
	p.trail.AddJSON(audit.RequestTransformed, 0, out.UpstreamRequest, map[string]any{"dry_run": true, "transformer": prog.Ref()})
	writeJSON(w, http.StatusOK, out)
	s.finish(r, p, http.StatusOK)
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
