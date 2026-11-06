package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient"
)

// Admin read-only tools (HUB-305). They are registered only with HUB_ADMIN_TOKEN, use the
// GET-only adminClient, and never call unseal, replay, purge, pause or force operations
// (contract rule 4). Control-plane rows legitimately name vendor, account and version.

// defaultSearchWindow is the time range hub_admin_search_requests uses when none is given
// (the contract's description: "default to the last hour").
const defaultSearchWindow = time.Hour

// payloadKeys are body-carrying fields removed from search rows: the tool returns summaries only.
var payloadKeys = []string{"result", "payload", "body", "request_body", "response_body"}

// pageOutput is the {items, next_cursor} output of the admin list tools.
type pageOutput struct {
	Items      []map[string]json.RawMessage `json:"items"`
	NextCursor *string                      `json:"next_cursor"`
}

type adminPage struct {
	Items      []map[string]json.RawMessage `json:"items"`
	NextCursor *string                      `json:"next_cursor"`
}

func (p adminPage) output() pageOutput {
	if p.Items == nil {
		p.Items = []map[string]json.RawMessage{}
	}
	return pageOutput(p)
}

func (a *app) adminSearchRequests(ctx context.Context, c *toolCall) toolResult {
	var in struct {
		TenantID       string   `json:"tenant_id"`
		IntegrationID  string   `json:"integration_id"`
		Status         []string `json:"status"`
		Code           []string `json:"code"`
		VendorID       string   `json:"vendor_id"`
		AccountID      string   `json:"account_id"`
		IdempotencyKey string   `json:"idempotency_key"`
		From           string   `json:"from"`
		To             string   `json:"to"`
		Cursor         string   `json:"cursor"`
		Limit          int      `json:"limit"`
	}
	if err := json.Unmarshal(c.args, &in); err != nil {
		return invalidArguments(toolAdminSearch, err)
	}
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("tenant_id", in.TenantID)
	set("integration_id", in.IntegrationID)
	set("status", strings.Join(in.Status, ",")) // style form, explode false
	set("code", strings.Join(in.Code, ","))
	set("vendor_id", in.VendorID)
	set("account_id", in.AccountID)
	set("idempotency_key", in.IdempotencyKey)
	set("cursor", in.Cursor)
	if in.Limit > 0 {
		q.Set("limit", strconv.Itoa(in.Limit))
	}
	if in.From == "" {
		to := a.now().UTC()
		if in.To != "" {
			if t, err := time.Parse(time.RFC3339Nano, in.To); err == nil {
				to = t
			}
		}
		in.From = to.Add(-defaultSearchWindow).Format(time.RFC3339)
	}
	set("from", in.From)
	set("to", in.To)

	var page adminPage
	if err := a.admin.get(ctx, "/requests", q, &page); err != nil {
		return adminFailure(toolAdminSearch, err)
	}
	for _, row := range page.Items {
		for _, k := range payloadKeys {
			delete(row, k)
		}
	}
	return toolResult{structured: page.output(), integrationID: in.IntegrationID}
}

type timelineOutput struct {
	RequestID     string                       `json:"request_id"`
	PayloadPolicy string                       `json:"payload_policy,omitempty"`
	Events        []map[string]json.RawMessage `json:"events"`
	Attempts      []map[string]json.RawMessage `json:"attempts"`
}

func (a *app) adminRequestTimeline(ctx context.Context, c *toolCall) toolResult {
	var in struct {
		RequestID       string `json:"request_id"`
		IncludePayloads bool   `json:"include_payloads"`
	}
	if err := json.Unmarshal(c.args, &in); err != nil {
		return invalidArguments(toolAdminTimeline, err)
	}
	id := url.PathEscape(in.RequestID)
	var tl struct {
		RequestID     string                       `json:"request_id"`
		PayloadPolicy string                       `json:"payload_policy"`
		Events        []map[string]json.RawMessage `json:"events"`
	}
	if err := a.admin.get(ctx, "/requests/"+id+"/timeline", nil, &tl); err != nil {
		res := adminFailure(toolAdminTimeline, err)
		res.requestID = in.RequestID
		return res
	}
	var at struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := a.admin.get(ctx, "/requests/"+id+"/attempts", nil, &at); err != nil {
		res := adminFailure(toolAdminTimeline, err)
		res.requestID = in.RequestID
		return res
	}
	out := timelineOutput{RequestID: tl.RequestID, PayloadPolicy: tl.PayloadPolicy, Events: tl.Events, Attempts: at.Items}
	if out.Events == nil {
		out.Events = []map[string]json.RawMessage{}
	}
	if out.Attempts == nil {
		out.Attempts = []map[string]json.RawMessage{}
	}
	if !in.IncludePayloads {
		// Metadata, sha256 and sizes only (saves context); masked/sealed policy stays the Hub's.
		for _, ev := range out.Events {
			delete(ev, "payload")
		}
	}
	return toolResult{structured: out, requestID: in.RequestID}
}

func (a *app) adminBreakerStates(ctx context.Context, c *toolCall) toolResult {
	var in struct {
		VendorID  string `json:"vendor_id"`
		AccountID string `json:"account_id"`
		State     string `json:"state"`
		Cursor    string `json:"cursor"`
		Limit     int    `json:"limit"`
	}
	if err := json.Unmarshal(c.args, &in); err != nil {
		return invalidArguments(toolAdminBreakerStates, err)
	}
	q := url.Values{}
	for k, v := range map[string]string{"vendor_id": in.VendorID, "account_id": in.AccountID, "state": in.State, "cursor": in.Cursor} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if in.Limit > 0 {
		q.Set("limit", strconv.Itoa(in.Limit))
	}
	var page adminPage
	if err := a.admin.get(ctx, "/breakers", q, &page); err != nil {
		return adminFailure(toolAdminBreakerStates, err)
	}
	return toolResult{structured: page.output()}
}

// adminFailure turns an admin client error into an isError result: the AdminError object
// verbatim when the control plane sent one, else a local description.
func adminFailure(tool string, err error) toolResult {
	var ae *adminError
	if errors.As(err, &ae) && ae.Object != nil {
		res := toolResult{isError: true, outcome: "error", structured: errorEnvelope{Error: ae.Object}}
		var obj struct {
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(ae.Object, &obj) == nil {
			res.requestID = obj.RequestID
		}
		return res
	}
	msg := "The admin API answered HTTP " + strconv.Itoa(statusOf(ae)) + " without an error envelope."
	if ae == nil || ae.Err != nil {
		msg = "hubmcp could not read the admin API (" + err.Error() + "). Check HUB_ADMIN_BASE_URL (default HUB_BASE_URL + /admin/v1)."
	}
	return failure(tool, &errorObject{Code: string(hubclient.CodeInternalError), Message: msg}, 0)
}

func statusOf(ae *adminError) int {
	if ae == nil {
		return 0
	}
	return ae.Status
}
