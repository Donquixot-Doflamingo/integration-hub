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
	IntegrationID string `json:"integration_id"`
	// Operations are what can be invoked, each with the request schema to follow.
	Operations   []Operation `json:"operations"`
	DeliveryMode string      `json:"delivery_mode"` // sync, wait, async, async_callback
	Interactive  bool        `json:"interactive"`   // a person is waiting; never returns 202
	Paused       bool        `json:"paused"`        // invocations return INTEGRATION_PAUSED
	// MovesMoneyOrQuota means: confirm before invoking and always send an Idempotency-Key.
	MovesMoneyOrQuota bool `json:"moves_money_or_quota"`
	// IdempotencyKeyRequired means a call without WithIdempotencyKey gets IDEMPOTENCY_KEY_REQUIRED.
	IdempotencyKeyRequired bool   `json:"idempotency_key_required"`
	Description            string `json:"description"`
}

// Operation is one operation of an integration (contract OperationDescriptor).
type Operation struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Idempotent  bool   `json:"idempotent"` // repeating the call has no extra side effect
	// RequestSchema is what to send: Body is the JSON Schema 2020-12 of the request body, Headers
	// (optional) the one of the caller headers the integration reads (send them with
	// WithHeaders). Fields in a schema's required list are required, the others optional.
	RequestSchema struct {
		Body    json.RawMessage `json:"body"`
		Headers json.RawMessage `json:"headers,omitempty"`
	} `json:"request_schema"`
}

// OperationNames lists the operation names of s, in the order the Hub lists them.
func (s IntegrationSummary) OperationNames() []string {
	out := make([]string, len(s.Operations))
	for i, op := range s.Operations {
		out[i] = op.Name
	}
	return out
}

// Operation returns the operation name of s, nil when the integration has none of that name.
func (s IntegrationSummary) Operation(name string) *Operation {
	for i := range s.Operations {
		if s.Operations[i].Name == name {
			return &s.Operations[i]
		}
	}
	return nil
}

// IntegrationPage is one page of ListIntegrations.
type IntegrationPage struct {
	Items      []IntegrationSummary
	NextCursor string // empty on the last page
}

// ListOptions filters and pages ListIntegrations. The zero value lists the first page with the
// Hub's default page size.
type ListOptions struct {
	Cursor string // NextCursor of the previous page
	Limit  int    // page size, 1 to 200; 0 uses the Hub default (50)
}

// ListIntegrations returns one page of the integrations the calling tenant can invoke
// (operation listIntegrations).
func (c *Client) ListIntegrations(ctx context.Context, opts ListOptions) (*IntegrationPage, error) {
	q := url.Values{}
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
