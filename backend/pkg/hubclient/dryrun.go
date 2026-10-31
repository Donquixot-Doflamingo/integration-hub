package hubclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// DryRunResult is the upstream request the Hub would send for a canonical body, with secrets
// replaced by "***" (operation dryRunIntegration). It is the one documented place where the
// vendor request shape is visible: use it for onboarding and debugging only, never build logic
// on it, because it changes whenever the Hub cuts over.
type DryRunResult struct {
	RequestID       string          `json:"request_id"`
	Operation       string          `json:"operation"`
	UpstreamRequest UpstreamRequest `json:"upstream_request"`
	Redacted        []string        `json:"redacted"` // paths whose values were replaced by ***
	Warnings        []string        `json:"warnings"`
}

// UpstreamRequest is the HTTP request a dry run would send.
type UpstreamRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	// Body is the JSON body, or a JSON string when BodyEncoding is text or base64; nil when absent.
	Body         json.RawMessage `json:"body"`
	BodyEncoding string          `json:"body_encoding"` // json (default), text or base64
}

// DryRun validates and transforms body exactly like Invoke but sends nothing upstream. The API
// key needs the dry_run scope (otherwise an *Error with CodeForbidden, reason missing_scope).
// Of the InvokeOptions only WithOperation and WithSession apply; the others are ignored.
func (c *Client) DryRun(ctx context.Context, integrationID string, body any, opts ...InvokeOption) (*DryRunResult, error) {
	var cfg invokeConfig
	for _, o := range opts {
		o(&cfg)
	}
	id, err := segment("integration id", integrationID)
	if err != nil {
		return nil, err
	}
	b, err := marshalBody(body)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	if cfg.operation != "" {
		q.Set("operation", cfg.operation)
	}
	h := http.Header{}
	if cfg.session != "" {
		h.Set("X-Hub-Session", cfg.session)
	}
	resp, err := c.do(ctx, call{method: http.MethodPost, path: "/v1/integrations/" + id + "/dry-run", query: q, body: b, header: h})
	if err != nil {
		return nil, err
	}
	if resp.status != http.StatusOK {
		return nil, resp.unexpected()
	}
	var r DryRunResult
	if err := resp.decode(&r); err != nil {
		return nil, err
	}
	if r.UpstreamRequest.BodyEncoding == "" {
		r.UpstreamRequest.BodyEncoding = "json"
	}
	if string(r.UpstreamRequest.Body) == "null" {
		r.UpstreamRequest.Body = nil
	}
	return &r, nil
}
