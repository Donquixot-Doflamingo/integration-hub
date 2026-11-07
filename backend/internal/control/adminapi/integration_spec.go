package adminapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// The admin contract's IntegrationSpec and the registry's version file (DESIGN §3.4) carry the
// same content with different names and units: vendor_id/account_id/endpoint_id ↔
// vendor/account/endpoint, ttl_ms/retention_ms ↔ durations. This file converts both ways.
// Version files also declare `delivery.polling` (DESIGN §5.2), which the contract does not name
// yet: it is accepted and rendered as an extra `delivery.polling` member (contract change
// request in the HUB-204/205 report).

type specTransformer struct {
	Request  string  `json:"request"`
	Response *string `json:"response"` // null or absent: pass-through (ADR-0023)
}

// specOperation is the contract's IntegrationOperation (ADR-0023).
type specOperation struct {
	Description   *string            `json:"description"`
	Idempotent    *bool              `json:"idempotent"`
	ValidateInput *bool              `json:"validate_input"`
	RequestSchema *specRequestSchema `json:"request_schema"`
}

// specRequestSchema is the contract's OperationRequestSchema.
type specRequestSchema struct {
	Body    map[string]any `json:"body"`
	Headers map[string]any `json:"headers,omitempty"`
}

type specTarget struct {
	VendorID    string          `json:"vendor_id"`
	AccountID   string          `json:"account_id"`
	Transformer specTransformer `json:"transformer"`
	// Operations is TargetRef.operations (hub-admin 1.2.0, HUB-343): per-operation pairs.
	Operations map[string]specTransformer `json:"operations,omitempty"`
	Weight     *int                       `json:"weight,omitempty"`
	Params     json.RawMessage            `json:"params,omitempty"`
}

type specDelivery struct {
	Mode          string          `json:"mode"`
	WaitMS        *int            `json:"wait_ms,omitempty"`
	TTLMS         *int64          `json:"ttl_ms,omitempty"`
	OnCircuitOpen string          `json:"on_circuit_open,omitempty"`
	Pending       string          `json:"pending,omitempty"`
	Polling       bool            `json:"polling,omitempty"`
	Retry         json.RawMessage `json:"retry,omitempty"`
}

type specBreaker struct {
	Override *struct {
		OpenAfterConsecutiveTimeouts int             `json:"open_after_consecutive_timeouts,omitempty"`
		FailureRatio                 json.RawMessage `json:"failure_ratio,omitempty"`
		MinCalls                     json.RawMessage `json:"min_calls,omitempty"`
	} `json:"override,omitempty"`
}

type specWebhook struct {
	Event      string `json:"event"`
	EndpointID string `json:"endpoint_id"`
}

type specAudit struct {
	Payloads    string `json:"payloads"`
	RetentionMS *int64 `json:"retention_ms,omitempty"`
}

type specShadow struct {
	Target      specTarget `json:"target"`
	Mode        string     `json:"mode"`
	SampleRatio *float64   `json:"sample_ratio,omitempty"`
}

// apiSpec is the contract's IntegrationSpec.
type apiSpec struct {
	Operations map[string]*specOperation `json:"operations"`
	Targets    []specTarget              `json:"targets"`
	Failover   []specTarget              `json:"failover"`
	Delivery   specDelivery              `json:"delivery"`
	Breaker    *specBreaker              `json:"breaker,omitempty"`
	Webhooks   []specWebhook             `json:"webhooks"`
	Audit      specAudit                 `json:"audit"`
	Shadow     *specShadow               `json:"shadow,omitempty"`
}

// unsupported refuses contract members the registry cannot express yet (400 at their pointer).
func unsupported(ptr, what string) error {
	return badRequest(ptr, what+" is not supported by the registry yet")
}

func toTarget(t specTarget, ptr string, weighted bool) (registry.Target, error) {
	if len(t.Params) > 0 && string(t.Params) != "null" {
		return registry.Target{}, unsupported(ptr+"/params", "target params")
	}
	out := registry.Target{Vendor: t.VendorID, Account: t.AccountID}
	var err error
	if out.Transformer, err = toPair(t.Transformer, ptr+"/transformer"); err != nil {
		return out, err
	}
	if t.Operations != nil {
		if len(t.Operations) == 0 {
			return out, badRequest(ptr+"/operations", "operations must name at least one operation (or be omitted)")
		}
		out.Operations = make(map[string]registry.TransformerPair, len(t.Operations))
		for _, op := range slices.Sorted(maps.Keys(t.Operations)) {
			at := ptr + "/operations/" + op
			if !operationName.MatchString(op) {
				return out, badRequest(at, fmt.Sprintf("operation names must match %s", operationName))
			}
			if out.Operations[op], err = toPair(t.Operations[op], at); err != nil {
				return out, err
			}
		}
	}
	if t.Weight != nil {
		if !weighted {
			return out, badRequest(ptr+"/weight", "failover and shadow targets take no weight")
		}
		out.Weight = *t.Weight
	}
	return out, nil
}

// operationName is the contract's OperationName.
var operationName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// toPair parses a contract TransformerPair at ptr.
func toPair(p specTransformer, ptr string) (registry.TransformerPair, error) {
	var out registry.TransformerPair
	var err error
	if out.Request, err = registry.ParseTransformerRef(p.Request); err != nil {
		return out, badRequest(ptr+"/request", err.Error())
	}
	if p.Response != nil {
		if out.Response, err = registry.ParseTransformerRef(*p.Response); err != nil {
			return out, badRequest(ptr+"/response", err.Error())
		}
	}
	return out, nil
}

// toOperations converts the contract's IntegrationOperations at ptr. Schemas are stored as
// given: a schema that does not compile is reported by validate and refused by publish
// (check schemas_compile), so a draft can be saved while it is being written.
func toOperations(ops map[string]*specOperation, ptr string) (map[string]*registry.Operation, error) {
	if len(ops) == 0 {
		return nil, badRequest(ptr, "declare at least one operation (single-operation integrations use default)")
	}
	out := make(map[string]*registry.Operation, len(ops))
	for _, name := range slices.Sorted(maps.Keys(ops)) {
		at := ptr + "/" + name
		o := ops[name]
		switch {
		case !operationName.MatchString(name):
			return nil, badRequest(at, fmt.Sprintf("operation names must match %s", operationName))
		case o == nil:
			return nil, badRequest(at, "an operation needs idempotent and request_schema")
		case o.Idempotent == nil:
			return nil, badRequest(at+"/idempotent", "idempotent is required")
		case o.RequestSchema == nil:
			return nil, badRequest(at+"/request_schema", "request_schema is required")
		case o.RequestSchema.Body == nil:
			return nil, badRequest(at+"/request_schema/body", "request_schema.body is required (use {\"type\": \"object\"} for a call without fields)")
		}
		op := &registry.Operation{Idempotent: *o.Idempotent, RequestSchema: registry.RequestSchema{Body: registry.NewJSONSchema(o.RequestSchema.Body)}}
		if o.Description != nil {
			op.Description = *o.Description
		}
		if o.ValidateInput != nil && !*o.ValidateInput {
			op.ValidateInput = o.ValidateInput // true is the default: not written
		}
		if o.RequestSchema.Headers != nil {
			op.RequestSchema.Headers = registry.NewJSONSchema(o.RequestSchema.Headers)
		}
		out[name] = op
	}
	return out, nil
}

// fromAPISpec converts a contract spec (the request member "spec": its pointers start with
// /spec) to a registry version with the DESIGN §2 defaults filled, so drafts and published
// versions render alike.
func fromAPISpec(s apiSpec) (*registry.IntegrationVersion, error) {
	const ptr = "/spec"
	v := &registry.IntegrationVersion{}
	var err error
	if v.Operations, err = toOperations(s.Operations, ptr+"/operations"); err != nil {
		return nil, err
	}
	for i, t := range s.Targets {
		rt, err := toTarget(t, fmt.Sprintf("%s/targets/%d", ptr, i), true)
		if err != nil {
			return nil, err
		}
		v.Targets = append(v.Targets, rt)
	}
	if len(v.Targets) == 0 {
		return nil, badRequest(ptr+"/targets", "at least one target is required")
	}
	for i, t := range s.Failover {
		rt, err := toTarget(t, fmt.Sprintf("%s/failover/%d", ptr, i), false)
		if err != nil {
			return nil, err
		}
		v.Failover = append(v.Failover, rt)
	}
	d := s.Delivery
	if len(d.Retry) > 0 && string(d.Retry) != "null" {
		return nil, unsupported(ptr+"/delivery/retry", "delivery.retry")
	}
	v.Delivery = registry.Delivery{Mode: registry.Mode(d.Mode), OnCircuitOpen: registry.OnCircuitOpen(d.OnCircuitOpen),
		Pending: registry.Pending(d.Pending), Polling: d.Polling}
	if d.WaitMS != nil {
		v.Delivery.WaitMS = *d.WaitMS
	}
	if d.TTLMS != nil {
		v.Delivery.TTL = registry.Duration(time.Duration(*d.TTLMS) * time.Millisecond)
	}
	if s.Breaker != nil && s.Breaker.Override != nil {
		o := s.Breaker.Override
		if len(o.FailureRatio) > 0 && string(o.FailureRatio) != "null" {
			return nil, unsupported(ptr+"/breaker/override/failure_ratio", "breaker.override.failure_ratio")
		}
		if len(o.MinCalls) > 0 && string(o.MinCalls) != "null" {
			return nil, unsupported(ptr+"/breaker/override/min_calls", "breaker.override.min_calls")
		}
		v.Breaker.Override.OpenAfterConsecutiveTimeouts = o.OpenAfterConsecutiveTimeouts
	}
	for _, w := range s.Webhooks {
		v.Webhooks = append(v.Webhooks, registry.Subscription{Event: w.Event, Endpoint: w.EndpointID})
	}
	v.Audit = registry.AuditPolicy{Payloads: registry.PayloadPolicy(s.Audit.Payloads)}
	if s.Audit.RetentionMS != nil {
		v.Audit.Retention = registry.Duration(time.Duration(*s.Audit.RetentionMS) * time.Millisecond)
	}
	if sh := s.Shadow; sh != nil {
		rt, err := toTarget(sh.Target, ptr+"/shadow/target", false)
		if err != nil {
			return nil, err
		}
		v.Shadow = &registry.Shadow{Target: rt, Mode: registry.ShadowMode(sh.Mode), SampleRatio: sh.SampleRatio}
	}
	normalize(v)
	return v, nil
}

// normalize fills the defaults the registry loader applies (registry.applyDefaults), so a
// stored draft equals its published file.
func normalize(v *registry.IntegrationVersion) {
	d := &v.Delivery
	if d.Mode == "" {
		d.Mode = registry.ModeWait
	}
	if d.Mode == registry.ModeWait && d.WaitMS == 0 {
		d.WaitMS = registry.DefaultWaitMS
	}
	if d.OnCircuitOpen == "" {
		d.OnCircuitOpen = registry.FailFast
	}
	if d.Pending == "" {
		d.Pending = registry.ReResolve
	}
	if d.TTL == 0 {
		d.TTL = registry.Duration(registry.DefaultTTL)
	}
	if v.Audit.Payloads == "" {
		v.Audit.Payloads = registry.PayloadMasked
	}
	if v.Audit.Retention == 0 {
		v.Audit.Retention = registry.Duration(registry.DefaultRetention)
	}
	if len(v.Targets) == 1 && v.Targets[0].Weight == 0 {
		v.Targets[0].Weight = 100 // a single target may omit its weight
	}
}

func renderPair(p registry.TransformerPair) map[string]any {
	m := map[string]any{"request": p.Request.String(), "response": nil}
	if !p.PassThrough() {
		m["response"] = p.Response.String()
	}
	return m
}

// renderOperations is the contract's IntegrationOperations; validate_input is always the
// effective value.
func renderOperations(ops map[string]*registry.Operation) map[string]any {
	out := make(map[string]any, len(ops))
	for name, op := range ops {
		if op == nil {
			continue
		}
		rs := map[string]any{"body": map[string]any{}}
		if op.RequestSchema.Body != nil && op.RequestSchema.Body.Doc != nil {
			rs["body"] = op.RequestSchema.Body.Doc
		}
		if op.RequestSchema.Headers != nil {
			rs["headers"] = op.RequestSchema.Headers.Doc
		}
		out[name] = map[string]any{"description": nullable(op.Description), "idempotent": op.Idempotent,
			"validate_input": op.Validates(), "request_schema": rs}
	}
	return out
}

func renderTarget(t registry.Target, weighted bool) map[string]any {
	m := map[string]any{"vendor_id": t.Vendor, "account_id": t.Account, "transformer": renderPair(t.Transformer)}
	if len(t.Operations) > 0 {
		ops := make(map[string]any, len(t.Operations))
		for op, p := range t.Operations {
			ops[op] = renderPair(p)
		}
		m["operations"] = ops
	}
	if weighted {
		m["weight"] = t.Weight
	}
	return m
}

// renderSpec is the contract IntegrationSpec of a registry version (generic JSON values, so
// diffs compare like with like).
func renderSpec(v *registry.IntegrationVersion) map[string]any {
	targets := make([]any, 0, len(v.Targets))
	for _, t := range v.Targets {
		if len(v.Targets) == 1 && t.Weight == 0 {
			t.Weight = 100
		}
		targets = append(targets, renderTarget(t, true))
	}
	failover := make([]any, 0, len(v.Failover))
	for _, t := range v.Failover {
		failover = append(failover, renderTarget(t, false))
	}
	d := v.Delivery
	delivery := map[string]any{"mode": string(d.Mode)}
	if d.WaitMS != 0 {
		delivery["wait_ms"] = d.WaitMS
	}
	if d.TTL != 0 {
		delivery["ttl_ms"] = d.TTL.Std().Milliseconds()
	}
	if d.OnCircuitOpen != "" {
		delivery["on_circuit_open"] = string(d.OnCircuitOpen)
	}
	if d.Pending != "" {
		delivery["pending"] = string(d.Pending)
	}
	if d.Polling {
		delivery["polling"] = true
	}
	webhooks := make([]any, 0, len(v.Webhooks))
	for _, w := range v.Webhooks {
		webhooks = append(webhooks, map[string]any{"event": w.Event, "endpoint_id": w.Endpoint})
	}
	audit := map[string]any{"payloads": string(v.Audit.Payloads)}
	if v.Audit.Retention != 0 {
		audit["retention_ms"] = v.Audit.Retention.Std().Milliseconds()
	}
	out := map[string]any{"operations": renderOperations(v.Operations), "targets": targets, "failover": failover, "delivery": delivery,
		"webhooks": webhooks, "audit": audit}
	if n := v.Breaker.Override.OpenAfterConsecutiveTimeouts; n != 0 {
		out["breaker"] = map[string]any{"override": map[string]any{"open_after_consecutive_timeouts": n}}
	}
	if sh := v.Shadow; sh != nil {
		shadow := map[string]any{"target": renderTarget(sh.Target, false), "mode": string(sh.Mode)}
		if sh.SampleRatio != nil {
			shadow["sample_ratio"] = *sh.SampleRatio
		}
		out["shadow"] = shadow
	}
	return generic(out)
}

// generic round-trips v through JSON (ints become float64, like a decoded request).
func generic(v map[string]any) map[string]any {
	b, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// versionYAML is the version document the publish pipeline completes (integration, version and
// publish metadata are set by control.versionDoc).
func versionYAML(v *registry.IntegrationVersion) ([]byte, error) {
	cp := *v
	cp.Integration, cp.Version, cp.OwnerTenant, cp.Grants = "", 0, "", nil
	cp.Publish = registry.PublishMeta{}
	return yaml.Marshal(&cp)
}

// parseVersionYAML reads a stored draft document.
func parseVersionYAML(b []byte) (*registry.IntegrationVersion, error) {
	var v registry.IntegrationVersion
	if err := yaml.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("draft document: %w", err)
	}
	return &v, nil
}

// registryNames maps registry YAML member names to the contract's.
var registryNames = map[string]string{"account": "account_id", "vendor": "vendor_id", "endpoint": "endpoint_id",
	"ttl": "ttl_ms", "retention": "retention_ms"}

var pathSegment = regexp.MustCompile(`[A-Za-z_]+`)

// contractPath renames the members of a registry problem path (targets[1].account →
// targets[1].account_id) so the pointer addresses the contract's IntegrationSpec. Operation
// names (operations.<op>, targets[0].operations.<op>) and what follows them in the operations
// block (schemas) are keys, never renamed.
func contractPath(p string) string {
	if strings.HasPrefix(p, "operations.") {
		return p
	}
	if head, op, ok := strings.Cut(p, ".operations."); ok {
		return contractPath(head) + ".operations." + op
	}
	return pathSegment.ReplaceAllStringFunc(p, func(s string) string {
		if n, ok := registryNames[s]; ok {
			return n
		}
		return s
	})
}

// specProblems rewrites the problems of integration's version files to contract paths.
func specProblems(ps registry.ConfigErrors, integration string) registry.ConfigErrors {
	out := make(registry.ConfigErrors, len(ps))
	prefix := "integrations/" + integration + "/versions/"
	for i, p := range ps {
		if strings.HasPrefix(p.File, prefix) {
			p.Path = contractPath(p.Path)
		}
		out[i] = p
	}
	return out
}

// integrationETag is the entity tag of an integration: a digest of its integration.yaml fields,
// its version numbers and whether a draft exists.
func integrationETag(in *registry.Integration, hasDraft bool) string {
	b, _ := json.Marshal(struct {
		F registry.IntegrationFile
		V []int
		D bool
	}{in.IntegrationFile, slices.Sorted(maps.Keys(in.Versions)), hasDraft})
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

// SpecOf renders a registry integration version as the contract's IntegrationSpec (hubctl
// apply sends it as a draft).
func SpecOf(v *registry.IntegrationVersion) map[string]any { return renderSpec(v) }
