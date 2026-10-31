package edge

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/resolve"
)

// Discovery (contracts: listIntegrations, getIntegration, getCapability). Every shape here is
// caller-facing: no vendor, account or integration version, ever (DESIGN §0).

type integrationSummary struct {
	IntegrationID          string   `json:"integration_id"`
	Capability             string   `json:"capability"`
	Operations             []string `json:"operations"`
	DeliveryMode           string   `json:"delivery_mode"`
	Interactive            bool     `json:"interactive"`
	Paused                 bool     `json:"paused"`
	MovesMoneyOrQuota      bool     `json:"moves_money_or_quota"`
	IdempotencyKeyRequired bool     `json:"idempotency_key_required"`
	Description            *string  `json:"description"`
}

type integrationDetail struct {
	integrationSummary
	Owned          bool     `json:"owned"`
	WaitMS         *int64   `json:"wait_ms"`
	TTLMS          *int64   `json:"ttl_ms"`
	OnCircuitOpen  string   `json:"on_circuit_open"`
	CallbackEvents []string `json:"callback_events"`
	CapabilityURL  string   `json:"capability_url"`
}

type integrationList struct {
	Items      []integrationSummary `json:"items"`
	NextCursor *string              `json:"next_cursor"`
}

type capabilityOperation struct {
	Name           string          `json:"name"`
	Description    *string         `json:"description"`
	Idempotent     bool            `json:"idempotent"`
	RequestSchema  json.RawMessage `json:"request_schema"`
	ResponseSchema json.RawMessage `json:"response_schema"`
}

type capabilityDescriptor struct {
	Capability  string                `json:"capability"`
	Name        string                `json:"name"`
	Major       int                   `json:"major"`
	Minor       int                   `json:"minor"`
	Description *string               `json:"description"`
	Interactive bool                  `json:"interactive"`
	Operations  []capabilityOperation `json:"operations"`
}

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// operationNames lists a capability's operations, "default" first, then by name.
func operationNames(c *registry.Capability) []string {
	names := make([]string, 0, len(c.Operations))
	for n := range c.Operations {
		names = append(names, n)
	}
	slices.SortFunc(names, func(a, b string) int {
		switch {
		case a == b:
			return 0
		case a == "default":
			return -1
		case b == "default":
			return 1
		}
		return strings.Compare(a, b)
	})
	return names
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func summarize(snap *registry.Snapshot, in *registry.Integration) integrationSummary {
	c := snap.Capabilities[in.Capability]
	return integrationSummary{
		IntegrationID: in.Integration, Capability: in.Capability, Operations: operationNames(c),
		DeliveryMode: string(in.Active().Delivery.Mode), Interactive: c.Interactive, Paused: in.Paused,
		MovesMoneyOrQuota: in.MovesMoneyOrQuota, IdempotencyKeyRequired: in.MovesMoneyOrQuota,
		Description: optional(in.Description),
	}
}

func (s *Server) listIntegrations(w http.ResponseWriter, r *http.Request) {
	c, _ := CallerFrom(r.Context())
	q := r.URL.Query()
	limit := defaultPageSize
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPageSize {
			WriteError(w, r, queryError("/limit", "limit must be 1.."+strconv.Itoa(maxPageSize)))
			return
		}
		limit = n
	}
	after := ""
	if v := q.Get("cursor"); v != "" {
		b, err := base64.RawURLEncoding.DecodeString(v)
		if err != nil || len(v) > 512 {
			WriteError(w, r, queryError("/cursor", "invalid cursor"))
			return
		}
		after = string(b)
	}
	capability := q.Get("capability")
	ids := make([]string, 0, len(c.Snap.Integrations))
	for id, in := range c.Snap.Integrations {
		if id > after && (capability == "" || in.Capability == capability) && resolve.Granted(in, c.Tenant.Tenant) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	out := integrationList{Items: []integrationSummary{}}
	if len(ids) > limit {
		ids = ids[:limit]
		out.NextCursor = core.Ptr(base64.RawURLEncoding.EncodeToString([]byte(ids[limit-1])))
	}
	for _, id := range ids {
		out.Items = append(out.Items, summarize(c.Snap, c.Snap.Integrations[id]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getIntegration(w http.ResponseWriter, r *http.Request) {
	c, _ := CallerFrom(r.Context())
	// Outside the tenant's set is 404, not 403, so ids cannot be enumerated (F-05 rule 3).
	in, ok := c.Snap.Integrations[r.PathValue("integration_id")]
	if !ok || !resolve.Granted(in, c.Tenant.Tenant) {
		WriteError(w, r, &core.CanonicalError{Code: core.CodeIntegrationNotFound, Message: "Unknown integration"})
		return
	}
	d := in.Active().Delivery
	out := integrationDetail{
		integrationSummary: summarize(c.Snap, in), Owned: in.OwnerTenant == c.Tenant.Tenant,
		OnCircuitOpen: string(d.OnCircuitOpen), CallbackEvents: []string{},
		CapabilityURL: "/v1/capabilities/" + in.Capability,
	}
	if d.Mode == registry.ModeWait {
		out.WaitMS = core.Ptr(int64(d.WaitMS))
	}
	if d.TTL > 0 {
		out.TTLMS = core.Ptr(d.TTL.Std().Milliseconds())
	}
	for _, sub := range in.Active().Webhooks {
		if !slices.Contains(out.CallbackEvents, sub.Event) {
			out.CallbackEvents = append(out.CallbackEvents, sub.Event)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// getCapability shows a capability to a tenant that can use at least one integration bound to it.
func (s *Server) getCapability(w http.ResponseWriter, r *http.Request) {
	c, _ := CallerFrom(r.Context())
	key := r.PathValue("capability")
	capability, ok := c.Snap.Capabilities[key]
	visible := false
	for _, in := range c.Snap.Integrations {
		if in.Capability == key && resolve.Granted(in, c.Tenant.Tenant) {
			visible = true
			break
		}
	}
	if !ok || !visible {
		WriteError(w, r, &core.CanonicalError{Code: core.CodeCapabilityNotFound, Message: "Unknown capability"})
		return
	}
	name, major, _ := strings.Cut(key, "@v")
	out := capabilityDescriptor{Capability: key, Name: name, Minor: capability.Minor, Interactive: capability.Interactive}
	out.Major, _ = strconv.Atoi(major)
	for _, n := range operationNames(capability) {
		op := capability.Operations[n]
		out.Operations = append(out.Operations, capabilityOperation{
			Name: n, Description: optional(op.Description), Idempotent: op.Idempotent,
			RequestSchema: op.RequestSchema, ResponseSchema: op.ResponseSchema,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func queryError(pointer, msg string) *core.CanonicalError {
	return &core.CanonicalError{Code: core.CodeValidationFailed, Message: msg, Reason: core.Ptr("query"),
		Details: []core.ErrorDetail{{Pointer: pointer, Message: msg}}}
}
