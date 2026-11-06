package main

import (
	"context"
	"encoding/json"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient"
)

// Discovery tools (HUB-302): hub_list_integrations and hub_describe_integration. Their output is
// built field by field from the caller-API types, so nothing outside the contract's output schema
// (in particular no vendor, account or version) can pass through.

// integrationItem is one hub_list_integrations item.
type integrationItem struct {
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

func summaryItem(s hubclient.IntegrationSummary) integrationItem {
	ops := s.Operations
	if ops == nil {
		ops = []string{}
	}
	return integrationItem{
		IntegrationID: s.IntegrationID, Capability: s.Capability, Operations: ops, DeliveryMode: s.DeliveryMode,
		Interactive: s.Interactive, Paused: s.Paused, MovesMoneyOrQuota: s.MovesMoneyOrQuota,
		IdempotencyKeyRequired: s.IdempotencyKeyRequired, Description: optString(s.Description),
	}
}

type listOutput struct {
	Items      []integrationItem `json:"items"`
	NextCursor *string           `json:"next_cursor"`
}

func (a *app) listIntegrations(ctx context.Context, c *toolCall) toolResult {
	var in struct {
		Capability string `json:"capability"`
		Cursor     string `json:"cursor"`
		Limit      int    `json:"limit"`
	}
	if err := json.Unmarshal(c.args, &in); err != nil {
		return invalidArguments(toolListIntegrations, err)
	}
	page, err := c.hub.ListIntegrations(ctx, hubclient.ListOptions{Capability: in.Capability, Cursor: in.Cursor, Limit: in.Limit})
	if err != nil {
		e, status := a.hubErrorObject(err)
		return failure(toolListIntegrations, e, status)
	}
	out := listOutput{Items: make([]integrationItem, 0, len(page.Items)), NextCursor: optString(page.NextCursor)}
	for _, it := range page.Items {
		out.Items = append(out.Items, summaryItem(it))
	}
	return toolResult{structured: out}
}

// integrationDetail is the integration part of hub_describe_integration.
type integrationDetail struct {
	integrationItem
	WaitMS         *int64   `json:"wait_ms"`
	TTLMS          *int64   `json:"ttl_ms"`
	OnCircuitOpen  string   `json:"on_circuit_open,omitempty"`
	CallbackEvents []string `json:"callback_events"`
}

type capabilityOut struct {
	Capability  string         `json:"capability"`
	Interactive bool           `json:"interactive"`
	Operations  []operationOut `json:"operations"`
}

type operationOut struct {
	Name           string          `json:"name"`
	Idempotent     bool            `json:"idempotent"`
	RequestSchema  json.RawMessage `json:"request_schema"`
	ResponseSchema json.RawMessage `json:"response_schema"`
}

type describeOutput struct {
	Integration integrationDetail `json:"integration"`
	Capability  capabilityOut     `json:"capability"`
}

func (a *app) describeIntegration(ctx context.Context, c *toolCall) toolResult {
	var in struct {
		IntegrationID string `json:"integration_id"`
	}
	if err := json.Unmarshal(c.args, &in); err != nil {
		return invalidArguments(toolDescribe, err)
	}
	d, err := c.hub.GetIntegration(ctx, in.IntegrationID)
	if err != nil {
		e, status := a.hubErrorObject(err)
		res := failure(toolDescribe, e, status)
		res.integrationID = in.IntegrationID
		return res
	}
	cp, err := c.hub.GetCapability(ctx, d.Capability)
	if err != nil {
		e, status := a.hubErrorObject(err)
		res := failure(toolDescribe, e, status)
		res.integrationID = in.IntegrationID
		return res
	}
	out := describeOutput{
		Integration: integrationDetail{
			integrationItem: summaryItem(d.IntegrationSummary),
			WaitMS:          optMillis(d.Wait.Milliseconds()),
			TTLMS:           optMillis(d.TTL.Milliseconds()),
			OnCircuitOpen:   d.OnCircuitOpen,
			CallbackEvents:  d.CallbackEvents,
		},
		Capability: capabilityOut{Capability: cp.Capability, Interactive: cp.Interactive, Operations: make([]operationOut, 0, len(cp.Operations))},
	}
	if out.Integration.CallbackEvents == nil {
		out.Integration.CallbackEvents = []string{}
	}
	for _, op := range cp.Operations {
		out.Capability.Operations = append(out.Capability.Operations, operationOut{
			Name: op.Name, Idempotent: op.Idempotent, RequestSchema: objectOrEmpty(op.RequestSchema), ResponseSchema: objectOrEmpty(op.ResponseSchema),
		})
	}
	return toolResult{structured: out, integrationID: in.IntegrationID}
}

func optMillis(ms int64) *int64 {
	if ms <= 0 {
		return nil
	}
	return &ms
}

// objectOrEmpty keeps a JSON object as is and turns anything else (absent, null) into {}.
func objectOrEmpty(b json.RawMessage) json.RawMessage {
	if len(b) > 0 && b[0] == '{' {
		return b
	}
	return json.RawMessage("{}")
}
