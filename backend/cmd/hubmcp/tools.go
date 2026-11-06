package main

import (
	"context"
	"encoding/json"
)

// Tool names of contracts/mcp/hubmcp-tools.json.
const (
	toolListIntegrations   = "hub_list_integrations"
	toolDescribe           = "hub_describe_integration"
	toolDryRun             = "hub_dry_run"
	toolInvoke             = "hub_invoke"
	toolGetRequest         = "hub_get_request"
	toolAdminSearch        = "hub_admin_search_requests"
	toolAdminTimeline      = "hub_admin_request_timeline"
	toolAdminBreakerStates = "hub_admin_breaker_states"
)

// handlers maps every contract tool to its implementation.
func (a *app) handlers() map[string]toolFunc {
	return map[string]toolFunc{
		toolListIntegrations:   a.listIntegrations,
		toolDescribe:           a.describeIntegration,
		toolDryRun:             a.dryRun,
		toolInvoke:             a.invoke,
		toolGetRequest:         a.getRequest,
		toolAdminSearch:        a.notImplemented(toolAdminSearch),
		toolAdminTimeline:      a.notImplemented(toolAdminTimeline),
		toolAdminBreakerStates: a.notImplemented(toolAdminBreakerStates),
	}
}

// notImplemented is the placeholder of a tool whose ticket has not landed yet.
func (a *app) notImplemented(tool string) toolFunc {
	return func(context.Context, *toolCall) toolResult {
		return failure(tool, &errorObject{Code: "INTERNAL_ERROR", Message: tool + " is not implemented yet in this hubmcp build"}, 0)
	}
}

// invokeOutput is hub_invoke's structured content (contract outputSchema). Every field is always
// present; absent values are null.
type invokeOutput struct {
	Outcome          string          `json:"outcome"` // completed, queued, error, refused
	HTTPStatus       *int            `json:"http_status"`
	RequestID        *string         `json:"request_id"`
	IdempotentReplay bool            `json:"idempotent_replay"`
	Result           json.RawMessage `json:"result"`
	Accepted         *acceptedBody   `json:"accepted"`
	Session          *string         `json:"session"`
	Error            *errorObject    `json:"error"`
}

// acceptedBody is the 202 body of a queued invoke.
type acceptedBody struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Poll   string `json:"poll,omitempty"`
}
