package registry

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// webhookEvents are the outbound event types a version may subscribe to (DESIGN §12.1).
var webhookEvents = []string{"request.completed", "request.failed", "request.expired", "delivery.status"}

// Validate checks every cross-reference and policy rule of the whole config set (DESIGN §5.2,
// §5.4, §6.4, §6.5) and reports all problems in one pass. It also fills defaults that need
// another object (TTL from the capability).
func Validate(c *Config) error {
	var ps ConfigErrors
	for _, id := range sortedKeys(c.Accounts) {
		a := c.Accounts[id]
		file := c.file("account", id)
		v, ok := c.Vendors[a.Vendor]
		if !ok {
			ps.add(file, "vendor", "unknown vendor %q", a.Vendor)
			continue
		}
		if _, ok := v.Regions[a.Region]; !ok {
			ps.add(file, "region", "vendor %q has no region %q (has %s)", a.Vendor, a.Region, strings.Join(sortedKeys(v.Regions), ", "))
		}
	}
	for _, id := range sortedKeys(c.Vendors) {
		v := c.Vendors[id]
		file := c.file("vendor", id)
		for region, base := range v.Regions {
			if u, err := url.Parse(base); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				ps.add(file, "regions."+region, "must be an absolute http(s) URL")
			}
		}
		if p := v.Defaults.HealthProbe; p != nil {
			if _, ok := v.Endpoints[p.Endpoint]; !ok {
				ps.add(file, "defaults.health_probe.endpoint", "unknown endpoint %q", p.Endpoint)
			}
		}
	}
	for _, ref := range sortedRefs(c.Transformers) {
		t := c.Transformers[ref]
		file := c.file("transformer", ref.String())
		if _, ok := c.Capabilities[t.Capability]; !ok {
			ps.add(file, "capability", "unknown capability %q", t.Capability)
		}
		if t.VendorEndpoint != "" {
			if _, _, err := c.endpoint(t.VendorEndpoint); err != nil {
				ps.add(file, "vendor_endpoint", "%v", err)
			}
		}
	}
	for _, id := range sortedKeys(c.Registrar) {
		r := c.Registrar[id]
		file := c.file("registrar", id)
		if _, ok := c.Tenants[r.Tenant]; !ok {
			ps.add(file, "tenant", "unknown tenant %q", r.Tenant)
		}
		if u, err := url.Parse(r.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			ps.add(file, "url", "must be an absolute http(s) URL")
		}
	}
	for _, id := range sortedKeys(c.Integrations) {
		c.validateIntegration(c.Integrations[id], &ps)
	}
	if len(ps) > 0 {
		return ps
	}
	return nil
}

// endpoint resolves "clevertap.upload@1".
func (c *Config) endpoint(ref string) (*Vendor, *Endpoint, error) {
	vendor, ep, ok := strings.Cut(ref, ".")
	if !ok {
		return nil, nil, fmt.Errorf("want <vendor>.<endpoint>@<n>, got %q", ref)
	}
	v, ok := c.Vendors[vendor]
	if !ok {
		return nil, nil, fmt.Errorf("unknown vendor %q", vendor)
	}
	e, ok := v.Endpoints[ep]
	if !ok {
		return nil, nil, fmt.Errorf("vendor %q has no endpoint %q", vendor, ep)
	}
	return v, &e, nil
}

func (c *Config) validateIntegration(in *Integration, ps *ConfigErrors) {
	file := in.File
	capability, capOK := c.Capabilities[in.Capability]
	if !capOK {
		ps.add(file, "capability", "unknown capability %q", in.Capability)
	}
	if _, ok := c.Tenants[in.OwnerTenant]; !ok {
		ps.add(file, "owner_tenant", "unknown tenant %q", in.OwnerTenant)
	}
	for i, g := range in.Grants {
		if _, ok := c.Tenants[g]; !ok {
			ps.add(file, fmt.Sprintf("grants[%d]", i), "unknown tenant %q", g)
		}
	}
	if len(in.Versions) == 0 {
		ps.add(file, "", "integration has no versions (versions/<n>.yaml)")
		return
	}
	if _, ok := in.Versions[in.ActiveVersion]; !ok {
		ps.add(file, "active_version", "version %d does not exist (have %v)", in.ActiveVersion, sortedKeys(in.Versions))
	}
	for _, n := range sortedKeys(in.Versions) {
		v := in.Versions[n]
		vf := in.VersionFiles[n]
		if v.Integration != in.Integration {
			ps.add(vf, "integration", "%q does not match the integration %q", v.Integration, in.Integration)
		}
		if v.OwnerTenant != "" && v.OwnerTenant != in.OwnerTenant {
			ps.add(vf, "owner_tenant", "%q does not match integration.yaml (%q)", v.OwnerTenant, in.OwnerTenant)
		}
		if v.Capability != "" && v.Capability != in.Capability {
			ps.add(vf, "capability", "%q does not match integration.yaml (%q); a schema change needs a new integration ID (D11)", v.Capability, in.Capability)
		}
		if v.Grants != nil && !slices.Equal(slices.Sorted(slices.Values(v.Grants)), slices.Sorted(slices.Values(in.Grants))) {
			ps.add(vf, "grants", "%v does not match integration.yaml (%v)", v.Grants, in.Grants)
		}
		c.validateVersion(in, v, vf, capability, n == in.ActiveVersion, ps)
	}
}

func (c *Config) validateVersion(in *Integration, v *IntegrationVersion, file string, capability *Capability, active bool, ps *ConfigErrors) {
	if len(v.Targets) == 0 {
		ps.add(file, "targets", "at least one target is required")
	}
	sum := 0
	for i, t := range v.Targets {
		c.validateTarget(in, capability, t, file, fmt.Sprintf("targets[%d]", i), active, ps)
		if t.Weight < 0 || t.Weight > 100 {
			ps.add(file, fmt.Sprintf("targets[%d].weight", i), "must be 0..100")
		}
		sum += t.Weight
	}
	if len(v.Targets) == 1 && v.Targets[0].Weight == 0 {
		sum = 100 // a single target may omit its weight
	}
	if len(v.Targets) > 0 && sum != 100 {
		ps.add(file, "targets", "weights must total 100, got %d", sum)
	}
	for i, t := range v.Failover {
		c.validateTarget(in, capability, t, file, fmt.Sprintf("failover[%d]", i), active, ps)
		if t.Weight != 0 {
			ps.add(file, fmt.Sprintf("failover[%d].weight", i), "failover entries are tried in order and take no weight")
		}
	}
	if sh := v.Shadow; sh != nil {
		c.validateTarget(in, capability, sh.Target, file, "shadow.target", active, ps)
		if sh.Target.Weight != 0 {
			ps.add(file, "shadow.target.weight", "a shadow target takes no weight")
		}
		switch sh.Mode {
		case ShadowSend, ShadowDryRun:
		default:
			ps.add(file, "shadow.mode", "must be send or dry_run")
		}
		if r := sh.Ratio(); r < 0 || r > 1 {
			ps.add(file, "shadow.sample_ratio", "must be 0..1")
		}
		// A copy must not cause a second side effect: only idempotent endpoints are sent for real,
		// for every pair the shadow target uses (one per operation, HUB-343).
		if sh.Mode == ShadowSend {
			seen := map[string]bool{}
			for _, pair := range sh.Target.Pairs() {
				if ref, ep, ok := c.requestEndpoint(pair); ok && !ep.Idempotent && !seen[ref] {
					seen[ref] = true
					ps.add(file, "shadow.mode", "%s is not idempotent (it has side effects): its shadow must be dry_run", ref)
				}
			}
		}
	}

	d := &v.Delivery
	switch d.Mode {
	case ModeSync, ModeWait, ModeAsync, ModeAsyncCallback:
	default:
		ps.add(file, "delivery.mode", "must be sync, wait, async or async_callback")
	}
	switch d.OnCircuitOpen {
	case FailFast, Failover, QueueIfAllowed:
	default:
		ps.add(file, "delivery.on_circuit_open", "must be fail_fast, failover or queue_if_allowed")
	}
	switch d.Pending {
	case ReResolve, Pinned:
	default:
		ps.add(file, "delivery.pending", "must be re_resolve or pinned")
	}
	if d.Mode == ModeWait && (d.WaitMS <= 0 || d.WaitMS > MaxWaitMS) {
		ps.add(file, "delivery.wait_ms", "must be 1..%d", MaxWaitMS)
	}
	if d.Mode != ModeWait && d.WaitMS != 0 {
		ps.add(file, "delivery.wait_ms", "only applies to mode wait")
	}
	if d.OnCircuitOpen == Failover && len(v.Failover) == 0 {
		ps.add(file, "delivery.on_circuit_open", "failover needs a failover chain")
	}
	if capability != nil {
		if capability.Interactive && d.Mode != ModeSync {
			ps.add(file, "delivery.mode", "capability %s is interactive: only sync is allowed (DESIGN §5.4)", capability.Key)
		}
		if d.TTL == 0 {
			d.TTL = Duration(capability.DefaultTTL)
		}
	}
	if d.TTL.Std() < 0 || d.TTL.Std() > 30*24*time.Hour {
		ps.add(file, "delivery.ttl", "must be 0..30d")
	}
	switch d.Mode {
	case ModeSync:
		// The caller gets the result in the response; nothing else to declare.
	case ModeWait, ModeAsync:
		if !d.Polling && len(v.Webhooks) == 0 {
			ps.add(file, "delivery", "mode %s needs polling: true or a webhook subscription (DESIGN §5.2)", d.Mode)
		}
	case ModeAsyncCallback:
		if len(v.Webhooks) == 0 {
			ps.add(file, "webhooks", "mode async_callback needs at least one webhook subscription")
		}
	}

	for i, w := range v.Webhooks {
		at := fmt.Sprintf("webhooks[%d]", i)
		if !slices.Contains(webhookEvents, w.Event) {
			ps.add(file, at+".event", "unknown event %q (want one of %s)", w.Event, strings.Join(webhookEvents, ", "))
		}
		r, ok := c.Registrar[w.Endpoint]
		switch {
		case !ok:
			ps.add(file, at+".endpoint", "unknown registrar endpoint %q", w.Endpoint)
		case r.Tenant != in.OwnerTenant:
			ps.add(file, at+".endpoint", "registrar endpoint %q belongs to tenant %q, not the owner %q", w.Endpoint, r.Tenant, in.OwnerTenant)
		}
	}

	switch v.Audit.Payloads {
	case PayloadFull, PayloadMasked, PayloadSealed, PayloadMetadataOnly:
	default:
		ps.add(file, "audit.payloads", "must be full, masked, sealed or metadata_only")
	}
	if v.Publish.Author == "" || v.Publish.Reason == "" {
		ps.add(file, "publish", "author and reason are required")
	}
}

func (c *Config) validateTarget(in *Integration, capability *Capability, t Target, file, at string, active bool, ps *ConfigErrors) {
	a, ok := c.Accounts[t.Account]
	if !ok {
		ps.add(file, at+".account", "unknown account %q", t.Account)
	} else {
		if a.Vendor != t.Vendor {
			ps.add(file, at+".vendor", "account %q belongs to vendor %q, not %q", t.Account, a.Vendor, t.Vendor)
		}
		if active && a.State == AccountRetired {
			ps.add(file, at+".account", "account %q is retired and cannot be targeted by the active version (DESIGN §6.5)", t.Account)
		}
	}
	if _, ok := c.Vendors[t.Vendor]; !ok {
		ps.add(file, at+".vendor", "unknown vendor %q", t.Vendor)
	}
	c.validatePair(in, t, t.Transformer, file, at+".transformer", ps)
	if t.Operations != nil && len(t.Operations) == 0 {
		ps.add(file, at+".operations", "name at least one operation, or omit operations")
	}
	for _, op := range sortedKeys(t.Operations) {
		c.validatePair(in, t, t.Operations[op], file, at+".operations."+op, ps)
	}
	c.checkOperations(capability, t, file, at, ps)
}

// validatePair checks that both transformers of a pair exist, have the right kind, map the
// integration's capability and (request) call an endpoint of the target's vendor.
func (c *Config) validatePair(in *Integration, t Target, pair TransformerPair, file, at string, ps *ConfigErrors) {
	for _, slot := range []struct {
		name string
		ref  TransformerRef
		kind TransformerKind
	}{{"request", pair.Request, KindRequest}, {"response", pair.Response, KindResponse}} {
		p := at + "." + slot.name
		if slot.ref.ID == "" {
			ps.add(file, p, "required")
			continue
		}
		tr, ok := c.Transformers[slot.ref]
		if !ok {
			ps.add(file, p, "unknown transformer %s", slot.ref)
			continue
		}
		if tr.Kind != slot.kind {
			ps.add(file, p, "%s is a %s transformer, want %s", slot.ref, tr.Kind, slot.kind)
		}
		if tr.Capability != in.Capability {
			ps.add(file, p, "%s maps capability %s, integration uses %s", slot.ref, tr.Capability, in.Capability)
		}
		if slot.kind == KindRequest && tr.VendorEndpoint != "" && !strings.HasPrefix(tr.VendorEndpoint, t.Vendor+".") {
			ps.add(file, p, "%s targets %s, not vendor %q", slot.ref, tr.VendorEndpoint, t.Vendor)
		}
	}
}

// CheckOperationsHaveTransformers is the publish check (admin contract ValidationCheck) that
// every operation of the capability is served by a pair compiled for it (HUB-343, HUB-345). Its
// problems say "is not an operation of" or "compiled for operation".
const CheckOperationsHaveTransformers = "operations_have_transformers"

// checkOperations is the operations_have_transformers check for one target: every key of
// Operations is an operation of the capability, and every operation of the capability is
// served (by its own pair, or by the default pair) by transformers compiled for that operation.
// A multi-operation capability whose only pair is compiled for one operation therefore fails
// for every other operation, at <at>.transformer (HUB-345): without it the other operations
// would run that operation's transformers. The registry build (config files, hubctl apply) and
// the admin API's validate and publish all run this one check.
func (c *Config) checkOperations(capability *Capability, t Target, file, at string, ps *ConfigErrors) {
	if capability == nil {
		return // unknown capability: reported on integration.yaml
	}
	for _, op := range sortedKeys(t.Operations) {
		if _, ok := capability.Operations[op]; !ok {
			ps.add(file, at+".operations."+op, "%q is not an operation of capability %s (it declares %s)",
				op, capability.Key, strings.Join(sortedKeys(capability.Operations), ", "))
		}
	}
	for _, op := range sortedKeys(capability.Operations) {
		_, listed := t.Operations[op]
		pair := t.Pair(op)
		for _, ref := range []TransformerRef{pair.Request, pair.Response} {
			tr, ok := c.Transformers[ref]
			if !ok || tr.Capability != capability.Key {
				continue // a missing or foreign transformer is reported by validatePair
			}
			got, err := c.operationFor(tr)
			if err != nil || got.Name == op {
				continue // an unusable operation: field is reported when the transformer compiles
			}
			if listed {
				ps.add(file, at+".operations."+op, "%s is compiled for operation %s, not %s", ref, got.Name, op)
				continue
			}
			ps.add(file, at+".transformer", "operation %s uses the default pair, but %s is compiled for operation %s: give %s its own pair under operations",
				op, ref, got.Name, op)
		}
	}
}

// requestEndpoint is the vendor endpoint the pair's request transformer calls.
func (c *Config) requestEndpoint(pair TransformerPair) (string, Endpoint, bool) {
	tr, ok := c.Transformers[pair.Request]
	if !ok {
		return "", Endpoint{}, false
	}
	vendor, name, ok := strings.Cut(tr.VendorEndpoint, ".")
	if !ok {
		return "", Endpoint{}, false
	}
	v, ok := c.Vendors[vendor]
	if !ok {
		return "", Endpoint{}, false
	}
	ep, ok := v.Endpoints[name]
	return tr.VendorEndpoint, ep, ok
}

func sortedKeys[K interface{ ~string | ~int }, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func sortedRefs(m map[TransformerRef]*TransformerFile) []TransformerRef {
	refs := make([]TransformerRef, 0, len(m))
	for r := range m {
		refs = append(refs, r)
	}
	slices.SortFunc(refs, func(a, b TransformerRef) int {
		if a.ID != b.ID {
			return strings.Compare(a.ID, b.ID)
		}
		return a.Version - b.Version
	})
	return refs
}
