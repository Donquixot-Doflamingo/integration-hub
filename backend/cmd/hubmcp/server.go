package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
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
	changeID      string // admin write tools: the change-log entry of the mutation
}

// app holds what tool handlers need. It never holds a database or snapshot handle: hubmcp is a
// client of the Hub's contracted HTTP APIs only (F-20 rule 1).
type app struct {
	hub    *hubclient.Client // stdio: HUB_API_KEY; nil over HTTP, where each client brings its key
	newHub func(key string) (*hubclient.Client, error)
	// newISFC builds a client that acts as an ISFC backend client (HUB_ISFC_*, or per request).
	newISFC func(clientID, apiKey, tenant string) (*hubclient.Client, error)
	admin   *adminClient // nil when HUB_ADMIN_TOKEN is unset: admin tools are not registered
	log     *slog.Logger
	secrets *secretSet
	now     func() time.Time
	// sleep waits d or until ctx ends (hub_invoke's single auto-retry); tests make it instant.
	sleep func(ctx context.Context, d time.Duration) error
	// integrations caches getIntegration for hub_invoke's pre-check (≤ 60 s, contract rule 2).
	integrations *integrationCache
	// transport is stdio or http; write tools are registered on stdio only.
	transport string
	// secretRes are the contract's secret_patterns: arguments matching one are refused.
	secretRes []*regexp.Regexp
	// http is the method-guarded client every Hub call goes through (no DELETE can leave).
	http *http.Client
}

// registeredTools is the tool set a server offers (contracts/mcp/README.md): tenant tools always;
// admin tools only with HUB_ADMIN_TOKEN; tools with requires.transport (the write tools: stdio)
// only on those transports, so Streamable HTTP never offers a write tool, loopback included.
func registeredTools(c *toolContract, transport string, admin bool) []*toolSpec {
	var out []*toolSpec
	for i := range c.Tools {
		spec := &c.Tools[i]
		if spec.Audience == audienceAdmin && !admin {
			continue
		}
		if !spec.allowedOn(transport) {
			continue
		}
		out = append(out, spec)
	}
	return out
}

// adminOperations is the admin client's allowlist: operationId -> the HTTP method maps_to
// declares, for the admin operations of the registered tools only.
func adminOperations(tools []*toolSpec) map[string]string {
	ops := map[string]string{}
	for _, t := range tools {
		for _, m := range t.MapsTo {
			if m.Spec == adminSpec {
				ops[m.OperationID] = m.HTTPMethod
			}
		}
	}
	return ops
}

// newMCPServer builds the MCP server with the contract's tools (registeredTools decides which).
// It fails if a contract tool has no handler or a handler has no contract tool, so the advertised
// set always equals the contract.
func newMCPServer(c *toolContract, a *app, version string) (*mcp.Server, error) {
	handlers := a.handlers(c)
	instructions := c.Server.Description
	switch {
	case a.admin == nil:
		instructions += " Admin tools (hub_admin_*) are disabled: HUB_ADMIN_TOKEN is not set."
	case a.transport == transportHTTP:
		instructions += " Admin write tools are not available over Streamable HTTP: to create or change control-plane objects, " +
			"run hubmcp over stdio with your own HUB_ADMIN_TOKEN."
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: c.Server.Name, Title: c.Server.Title, Version: version},
		&mcp.ServerOptions{Instructions: instructions})
	known := map[string]bool{}
	for i := range c.Tools {
		spec := &c.Tools[i]
		known[spec.Name] = true
		if _, ok := handlers[spec.Name]; !ok {
			return nil, fmt.Errorf("contract tool %s has no handler", spec.Name)
		}
	}
	for _, spec := range registeredTools(c, a.transport, a.admin != nil) {
		srv.AddTool(mcpTool(spec), a.handle(spec, handlers[spec.Name]))
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

// handle wraps a toolFunc: the secret-in-input refusal, input validation, the confirm guard,
// secret redaction, rendering and the tool_called log. Refusals make no Hub call. Tool failures
// are results with isError set, never protocol errors (contract rule 5).
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
		} else if ptr := findSecret(call.args, a.secretRes); ptr != "" {
			res = secretRefusal(spec.Name, ptr) // before the schema check, whose errors may quote values
		} else if err := spec.validateArgs(call.args); err != nil {
			res = invalidArguments(spec.Name, err)
		} else if spec.RequiresConfirm != "" && !confirmGiven(call.args) {
			res = confirmRefusal(spec)
		} else {
			res = fn(ctx, call)
		}
		var minted []secret
		if !res.isError && len(spec.SensitiveOutput) > 0 {
			minted = sensitiveValues(res.structured, spec.SensitiveOutput)
			res.note = sensitiveNote(spec.SensitiveOutput)
		}
		out, err := a.render(res, keySecrets)
		// From now on the minted secret is redacted by value from every log line and every later
		// result: it is returned in this one result only.
		a.secrets.add(minted...)
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
		if spec.mutating() {
			attrs = append(attrs, slog.Bool("mutating", true))
		}
		if res.changeID != "" {
			attrs = append(attrs, slog.String("change_id", res.changeID))
		}
		a.log.LogAttrs(ctx, slog.LevelInfo, "tool_called", attrs...)
		return out, nil
	}
}

// sensitiveValues returns the write-once secrets at the given top-level JSON pointers of a result.
func sensitiveValues(structured any, pointers []string) []secret {
	b, err := json.Marshal(structured)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	var out []secret
	for _, p := range pointers {
		if s, ok := m[p[1:]].(string); ok && s != "" {
			out = append(out, secret(s))
		}
	}
	return out
}

// sensitiveNote is the line shown before a result that carries a write-once secret.
func sensitiveNote(pointers []string) string {
	return "SENSITIVE OUTPUT, shown once: " + fmt.Sprint(pointers) + ". Give it to the user verbatim, once, to store in their secret " +
		"manager now. Never repeat it, never put it in another tool call, file, code sample, commit or memory; afterwards refer " +
		"to it by its id or prefix. hubmcp redacts it from everything after this result."
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

// MCP-only refusal codes of hub_invoke (contract rule 2). They never come from the Hub.
const (
	codeConfirmationRequired   = "CONFIRMATION_REQUIRED"
	codeIdempotencyKeyRequired = "IDEMPOTENCY_KEY_REQUIRED"
)

// hubErrorObject converts a hubclient error into the canonical error object. A transport failure
// (the Hub is unreachable) becomes a local INTERNAL_ERROR naming the base URL, not the API key.
func (a *app) hubErrorObject(err error) (*errorObject, int) {
	var he *hubclient.Error
	if !errors.As(err, &he) {
		msg := "hubmcp could not use the Hub's answer: " + err.Error()
		var ue *url.Error
		if errors.As(err, &ue) {
			msg = "hubmcp could not reach the Hub (" + err.Error() + "). Check HUB_BASE_URL and that the gateway is running."
		}
		return &errorObject{Code: string(hubclient.CodeInternalError), Message: msg}, 0
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
		e.Message += " (check HUB_API_KEY or HUB_ISFC_API_KEY)"
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
