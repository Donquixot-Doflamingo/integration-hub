package edge

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/resolve"
)

// Discovery (contracts: listIntegrations, getIntegration). Every shape here is caller-facing: no
// vendor, account or integration version, ever (DESIGN §0). Operations and their request schemas
// come from the snapshot's active version (ADR-0023).

type integrationSummary struct {
	IntegrationID          string                `json:"integration_id"`
	Operations             []operationDescriptor `json:"operations"`
	DeliveryMode           string                `json:"delivery_mode"`
	Interactive            bool                  `json:"interactive"`
	Paused                 bool                  `json:"paused"`
	MovesMoneyOrQuota      bool                  `json:"moves_money_or_quota"`
	IdempotencyKeyRequired bool                  `json:"idempotency_key_required"`
	Description            *string               `json:"description"`
}

type integrationDetail struct {
	integrationSummary
	Owned          bool     `json:"owned"`
	WaitMS         *int64   `json:"wait_ms"`
	TTLMS          *int64   `json:"ttl_ms"`
	OnCircuitOpen  string   `json:"on_circuit_open"`
	CallbackEvents []string `json:"callback_events"`
}

type integrationList struct {
	Items      []integrationSummary `json:"items"`
	NextCursor *string              `json:"next_cursor"`
}

// operationDescriptor is the contract OperationDescriptor.
type operationDescriptor struct {
	Name          string        `json:"name"`
	Description   *string       `json:"description"`
	Idempotent    bool          `json:"idempotent"`
	RequestSchema requestSchema `json:"request_schema"`
}

type requestSchema struct {
	Body    json.RawMessage `json:"body"`
	Headers json.RawMessage `json:"headers,omitempty"`
}

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// operations describes the operations of v, "default" first, then by name.
func operations(v *registry.IntegrationVersion) []operationDescriptor {
	out := []operationDescriptor{}
	for _, name := range v.OperationNames() {
		op := v.Op(name)
		d := operationDescriptor{Name: name, Description: optional(op.Description), Idempotent: op.Idempotent,
			RequestSchema: requestSchema{Body: op.RequestSchema.Body.JSON()}}
		if h := op.RequestSchema.Headers; h != nil {
			d.RequestSchema.Headers = h.JSON()
		}
		out = append(out, d)
	}
	return out
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func summarize(in *registry.Integration) integrationSummary {
	return integrationSummary{
		IntegrationID: in.Integration, Operations: operations(in.Active()),
		DeliveryMode: string(in.Active().Delivery.Mode), Interactive: in.Interactive, Paused: in.Paused,
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
	ids := make([]string, 0, len(c.Snap.Integrations))
	for id, in := range c.Snap.Integrations {
		if id > after && resolve.Granted(in, c.Tenant.Tenant) {
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
		out.Items = append(out.Items, summarize(c.Snap.Integrations[id]))
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
		integrationSummary: summarize(in), Owned: in.OwnerTenant == c.Tenant.Tenant,
		OnCircuitOpen: string(d.OnCircuitOpen), CallbackEvents: []string{},
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

func queryError(pointer, msg string) *core.CanonicalError {
	return &core.CanonicalError{Code: core.CodeValidationFailed, Message: msg, Reason: core.Ptr("query"),
		Details: []core.ErrorDetail{{Pointer: pointer, Message: msg}}}
}
