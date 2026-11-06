package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient"
)

// toolFunc runs one tool after its arguments passed the contract's input schema.
type toolFunc func(ctx context.Context, c *toolCall) toolResult

// toolCall is one tool invocation: its validated arguments and the Hub client to use (the
// process's HUB_API_KEY client over stdio, the HTTP client's own key over Streamable HTTP).
type toolCall struct {
	args   json.RawMessage
	hub    *hubclient.Client
	tenant string // cache partition: "" for stdio, the key fingerprint over HTTP
}

// toolResult is what a tool returns: the structured content (which must match the contract's
// outputSchema unless isError is set) plus fields for the tool_called log line.
type toolResult struct {
	structured any
	isError    bool
	// note is an optional human-readable line shown before the JSON text content (for example the
	// summary a confirmation refusal asks the model to show the user).
	note string

	integrationID string
	requestID     string
	outcome       string
}

// app holds what tool handlers need. It never holds a database or snapshot handle: hubmcp is a
// client of the Hub's contracted HTTP APIs only (F-20 rule 1).
type app struct {
	hub     *hubclient.Client // stdio: HUB_API_KEY; nil over HTTP, where each client brings its key
	newHub  func(key string) (*hubclient.Client, error)
	admin   *adminClient // nil when HUB_ADMIN_TOKEN is unset: admin tools are not registered
	log     *slog.Logger
	secrets *secretSet
	now     func() time.Time
}

// newMCPServer builds the MCP server with the contract's tools. Admin tools are registered only
// when an admin client is configured (contract config: HUB_ADMIN_TOKEN). It fails if a contract
// tool has no handler or a handler has no contract tool, so the advertised set always equals the
// contract.
func newMCPServer(c *toolContract, a *app, version string) (*mcp.Server, error) {
	handlers := a.handlers()
	instructions := c.Server.Description
	if a.admin == nil {
		instructions += " Admin tools (hub_admin_*) are disabled: HUB_ADMIN_TOKEN is not set."
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: c.Server.Name, Title: c.Server.Title, Version: version},
		&mcp.ServerOptions{Instructions: instructions})
	known := map[string]bool{}
	for i := range c.Tools {
		spec := &c.Tools[i]
		known[spec.Name] = true
		fn, ok := handlers[spec.Name]
		if !ok {
			return nil, fmt.Errorf("contract tool %s has no handler", spec.Name)
		}
		if spec.Audience == audienceAdmin && a.admin == nil {
			continue
		}
		srv.AddTool(mcpTool(spec), a.handle(spec, fn))
	}
	for name := range handlers {
		if !known[name] {
			return nil, fmt.Errorf("handler %s is not a contract tool", name)
		}
	}
	return srv, nil
}

// mcpTool converts a contract tool into the SDK's tool definition, schemas verbatim.
func mcpTool(spec *toolSpec) *mcp.Tool {
	destructive, openWorld := spec.Annotations.DestructiveHint, spec.Annotations.OpenWorldHint
	return &mcp.Tool{
		Name:         spec.Name,
		Title:        spec.Title,
		Description:  spec.Description,
		InputSchema:  spec.InputSchema,
		OutputSchema: spec.OutputSchema,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    spec.Annotations.ReadOnlyHint,
			DestructiveHint: &destructive,
			IdempotentHint:  spec.Annotations.IdempotentHint,
			OpenWorldHint:   &openWorld,
		},
	}
}

// handle wraps a toolFunc: input validation, secret redaction, rendering and the tool_called log.
// Tool failures are results with isError set, never protocol errors (contract rule 5).
func (a *app) handle(spec *toolSpec, fn toolFunc) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := a.now()
		call := &toolCall{hub: a.hub}
		if req.Params != nil {
			call.args = req.Params.Arguments
		}
		hub, keySecrets, tenant := sessionAuth(req)
		if hub != nil {
			call.hub, call.tenant = hub, tenant
		}
		var res toolResult
		if call.hub == nil {
			// Unreachable: the HTTP transport authenticates every request before the MCP layer.
			res = failure(spec.Name, &errorObject{Code: string(hubclient.CodeUnauthorized), Message: "no tenant API key for this session"}, 0)
		} else if err := spec.validateArgs(call.args); err != nil {
			res = invalidArguments(spec.Name, err)
		} else {
			res = fn(ctx, call)
		}
		out, err := a.render(res, keySecrets)
		if err != nil {
			return nil, err
		}
		attrs := []slog.Attr{
			slog.String("tool", spec.Name),
			slog.Bool("is_error", res.isError),
			slog.Int64("duration_ms", a.now().Sub(start).Milliseconds()),
		}
		if res.integrationID != "" {
			attrs = append(attrs, slog.String("integration_id", res.integrationID))
		}
		if res.requestID != "" {
			attrs = append(attrs, slog.String("request_id", res.requestID))
		}
		if res.outcome != "" {
			attrs = append(attrs, slog.String("outcome", res.outcome))
		}
		a.log.LogAttrs(ctx, slog.LevelInfo, "tool_called", attrs...)
		return out, nil
	}
}

// render turns a toolResult into the MCP result: structuredContent plus the same JSON as text
// (for clients that ignore structured content). Configured secrets are replaced by *** in both.
func (a *app) render(res toolResult, extra *secretSet) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(res.structured)
	if err != nil {
		return nil, fmt.Errorf("encode tool result: %w", err)
	}
	b = a.secrets.redactJSON(b)
	if extra != nil {
		b = extra.redactJSON(b)
		res.note = extra.redact(res.note)
	}
	var structured any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&structured); err != nil {
		return nil, fmt.Errorf("decode redacted tool result: %w", err)
	}
	text := string(b)
	if res.note != "" {
		text = a.secrets.redact(res.note) + "\n\n" + text
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: text}},
		StructuredContent: structured,
		IsError:           res.isError,
	}, nil
}

// errorObject is the canonical error object (contracts/schemas/events/canonical-error.json) as
// hubmcp returns it. Locally produced errors (refusals, argument errors, an unreachable Hub) have
// no request_id (null).
type errorObject struct {
	Code         string         `json:"code"`
	Message      string         `json:"message"`
	Retryable    bool           `json:"retryable"`
	RetryAfterMS *int64         `json:"retry_after_ms"`
	Circuit      *string        `json:"circuit,omitempty"`
	Reason       *string        `json:"reason,omitempty"`
	Upstream     *upstreamError `json:"upstream,omitempty"`
	Details      []errorDetail  `json:"details,omitempty"`
	RequestID    *string        `json:"request_id"`
}

type upstreamError struct {
	Status *int    `json:"status"`
	Code   *string `json:"code"`
}

type errorDetail struct {
	Pointer string `json:"pointer,omitempty"`
	Keyword string `json:"keyword,omitempty"`
	Message string `json:"message"`
}

// hubErrorObject converts a hubclient error into the canonical error object. A transport failure
// (the Hub is unreachable) becomes a local INTERNAL_ERROR naming the base URL, not the API key.
func (a *app) hubErrorObject(err error) (*errorObject, int) {
	var he *hubclient.Error
	if !errors.As(err, &he) {
		return &errorObject{
			Code:    string(hubclient.CodeInternalError),
			Message: "hubmcp could not reach the Hub (" + err.Error() + "). Check HUB_BASE_URL and that the gateway is running.",
		}, 0
	}
	e := &errorObject{Code: string(he.Code), Message: he.Message, Retryable: he.Retryable, RequestID: optString(he.RequestID)}
	if he.RetryAfter > 0 {
		ms := he.RetryAfter.Milliseconds()
		e.RetryAfterMS = &ms
	}
	e.Circuit, e.Reason = optString(he.Circuit), optString(he.Reason)
	if he.Upstream != nil {
		e.Upstream = &upstreamError{Code: optString(he.Upstream.Code)}
		if he.Upstream.Status != 0 {
			s := he.Upstream.Status
			e.Upstream.Status = &s
		}
	}
	for _, d := range he.Details {
		e.Details = append(e.Details, errorDetail{Pointer: d.Pointer, Keyword: d.Keyword, Message: d.Message})
	}
	if he.Code == hubclient.CodeUnauthorized {
		e.Message += " (check HUB_API_KEY)"
	}
	return e, he.Status
}

// errorEnvelope is the structured content of a failed tool other than hub_invoke.
type errorEnvelope struct {
	Error any `json:"error"`
}

// failure is the toolResult for a failed tool. hub_invoke keeps its own output shape.
func failure(tool string, e *errorObject, httpStatus int) toolResult {
	res := toolResult{isError: true, outcome: "error"}
	if e.RequestID != nil {
		res.requestID = *e.RequestID
	}
	if tool == toolInvoke {
		res.structured = invokeOutput{Outcome: "error", HTTPStatus: optInt(httpStatus), RequestID: e.RequestID, Error: e}
		return res
	}
	res.structured = errorEnvelope{Error: e}
	return res
}

// invalidArguments reports arguments that break the tool's input schema, so the model can fix
// them (MCP: input validation errors are tool execution errors, not protocol errors).
func invalidArguments(tool string, err error) toolResult {
	return failure(tool, &errorObject{
		Code:    string(hubclient.CodeValidationFailed),
		Message: "Arguments do not match the " + tool + " input schema: " + err.Error(),
	}, 0)
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}
