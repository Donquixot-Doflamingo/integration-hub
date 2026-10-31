package hubclient

import (
	"context"
	"encoding/json"
	"iter"
	"net/url"
	"strconv"
	"time"
)

// IntegrationSummary is the caller-facing description of an integration. It never names the
// vendor, account or version behind it.
type IntegrationSummary struct {
	IntegrationID string   `json:"integration_id"`
	Capability    string   `json:"capability"`
	Operations    []string `json:"operations"`
	DeliveryMode  string   `json:"delivery_mode"` // sync, wait, async, async_callback
	Interactive   bool     `json:"interactive"`   // a person is waiting; never returns 202
	Paused        bool     `json:"paused"`        // invocations return INTEGRATION_PAUSED
	// MovesMoneyOrQuota means: confirm before invoking and always send an Idempotency-Key.
	MovesMoneyOrQuota bool `json:"moves_money_or_quota"`
	// IdempotencyKeyRequired means a call without WithIdempotencyKey gets IDEMPOTENCY_KEY_REQUIRED.
	IdempotencyKeyRequired bool   `json:"idempotency_key_required"`
	Description            string `json:"description"`
}

// IntegrationPage is one page of ListIntegrations.
type IntegrationPage struct {
	Items      []IntegrationSummary
	NextCursor string // empty on the last page
}

// ListOptions filters and pages ListIntegrations. The zero value lists the first page with the
// Hub's default page size.
type ListOptions struct {
	Capability string // filter by capability key, e.g. "events.track@v1"
	Cursor     string // NextCursor of the previous page
	Limit      int    // page size, 1 to 200; 0 uses the Hub default (50)
}

// ListIntegrations returns one page of the integrations the calling tenant can invoke
// (operation listIntegrations).
func (c *Client) ListIntegrations(ctx context.Context, opts ListOptions) (*IntegrationPage, error) {
	q := url.Values{}
	if opts.Capability != "" {
		q.Set("capability", opts.Capability)
	}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	var w struct {
		Items      []IntegrationSummary `json:"items"`
		NextCursor *string              `json:"next_cursor"`
	}
	if err := c.getJSON(ctx, "/v1/integrations", q, &w); err != nil {
		return nil, err
	}
	return &IntegrationPage{Items: w.Items, NextCursor: deref(w.NextCursor)}, nil
}

// AllIntegrations iterates over every integration the tenant can invoke, fetching pages as
// needed, starting at opts.Cursor. On an error it yields the error once and stops.
//
//	for in, err := range client.AllIntegrations(ctx, hubclient.ListOptions{}) {
//		if err != nil { return err }
//		...
//	}
func (c *Client) AllIntegrations(ctx context.Context, opts ListOptions) iter.Seq2[IntegrationSummary, error] {
	return func(yield func(IntegrationSummary, error) bool) {
		for {
			page, err := c.ListIntegrations(ctx, opts)
			if err != nil {
				yield(IntegrationSummary{}, err)
				return
			}
			for _, it := range page.Items {
				if !yield(it, nil) {
					return
				}
			}
			if page.NextCursor == "" || page.NextCursor == opts.Cursor {
				return
			}
			opts.Cursor = page.NextCursor
		}
	}
}

// IntegrationDetail describes one integration and its delivery policy (operation getIntegration).
type IntegrationDetail struct {
	IntegrationSummary
	Owned          bool          // false: the tenant is a grantee
	Wait           time.Duration // wait window in wait mode; 0 otherwise
	TTL            time.Duration // how long an accepted request may wait before it expires; 0 when unset
	OnCircuitOpen  string        // fail_fast, failover, queue_if_allowed
	CallbackEvents []string      // event types delivered to the tenant's endpoints
	CapabilityURL  string        // relative URL for GetCapability
}

// UnmarshalJSON decodes the contract's IntegrationDetail object, turning *_ms into durations.
func (d *IntegrationDetail) UnmarshalJSON(b []byte) error {
	var s IntegrationSummary
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	var w struct {
		Owned          bool     `json:"owned"`
		WaitMS         *int64   `json:"wait_ms"`
		TTLMS          *int64   `json:"ttl_ms"`
		OnCircuitOpen  string   `json:"on_circuit_open"`
		CallbackEvents []string `json:"callback_events"`
		CapabilityURL  string   `json:"capability_url"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*d = IntegrationDetail{
		IntegrationSummary: s,
		Owned:              w.Owned,
		Wait:               time.Duration(deref(w.WaitMS)) * time.Millisecond,
		TTL:                time.Duration(deref(w.TTLMS)) * time.Millisecond,
		OnCircuitOpen:      w.OnCircuitOpen,
		CallbackEvents:     w.CallbackEvents,
		CapabilityURL:      w.CapabilityURL,
	}
	return nil
}

// GetIntegration describes one integration (operation getIntegration).
func (c *Client) GetIntegration(ctx context.Context, integrationID string) (*IntegrationDetail, error) {
	id, err := segment("integration id", integrationID)
	if err != nil {
		return nil, err
	}
	var d IntegrationDetail
	if err := c.getJSON(ctx, "/v1/integrations/"+id, nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Capability is a capability's canonical schemas per operation (operation getCapability).
type Capability struct {
	Capability  string                `json:"capability"` // key, e.g. "events.track@v1"
	Name        string                `json:"name"`
	Major       int                   `json:"major"`
	Minor       int                   `json:"minor"`
	Description string                `json:"description"`
	Interactive bool                  `json:"interactive"`
	Operations  []CapabilityOperation `json:"operations"`
}

// CapabilityOperation is one operation with its JSON Schema 2020-12 documents.
type CapabilityOperation struct {
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Idempotent     bool            `json:"idempotent"`
	RequestSchema  json.RawMessage `json:"request_schema"`
	ResponseSchema json.RawMessage `json:"response_schema"`
}

// GetCapability returns the canonical request and response schemas of a capability, such as
// "events.track@v1" (operation getCapability).
func (c *Client) GetCapability(ctx context.Context, capability string) (*Capability, error) {
	key, err := segment("capability", capability)
	if err != nil {
		return nil, err
	}
	var cp Capability
	if err := c.getJSON(ctx, "/v1/capabilities/"+key, nil, &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}
