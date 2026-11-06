package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Contract-driven admin tools (HUB-315, hubmcp-tools 1.1.0): 18 read tools and non-mutating
// checks, and 16 write tools. Each maps to exactly one hub-admin operation. The handler is
// derived from the tool's input schema and the operation's route:
//
//   - path parameters ({tenant_id}, {version}, ...) come from the input field of the same name;
//   - if_match becomes the If-Match header, confirm is consumed by the confirm guard (server.go);
//   - every other field goes to the query string (GET) or, unchanged, to the JSON body;
//   - reason gets reasonSuffix appended, so the change log shows the change came through MCP.
//
// The input schemas have additionalProperties false and contain no secret fields (checked at
// load and by tests), so nothing beyond the contracted fields can reach the Hub.

// reasonSuffix is appended to every reason a write tool forwards (contracts/mcp/README.md).
const reasonSuffix = " (via hubmcp)"

// Admin tools whose result is shaped beyond a pass-through.
const toolAdminCreateTransformerVersion = "hub_admin_create_transformer_version"

// adminToolHandlers returns the handlers of every admin tool except the three hand-written ones.
func (a *app) adminToolHandlers(c *toolContract) map[string]toolFunc {
	out := map[string]toolFunc{}
	for i := range c.Tools {
		spec := &c.Tools[i]
		if spec.Audience != audienceAdmin {
			continue
		}
		switch spec.Name {
		case toolAdminSearch, toolAdminTimeline, toolAdminBreakerStates:
			continue
		}
		out[spec.Name] = a.adminTool(spec)
	}
	return out
}

// adminTool builds the handler of one contract-driven admin tool.
func (a *app) adminTool(spec *toolSpec) toolFunc {
	if len(spec.MapsTo) != 1 || spec.MapsTo[0].Spec != adminSpec {
		return func(context.Context, *toolCall) toolResult {
			return failure(spec.Name, &errorObject{Code: "INTERNAL_ERROR", Message: "hubmcp: " + spec.Name + " has no single hub-admin operation"}, 0)
		}
	}
	op := spec.MapsTo[0].OperationID
	return func(ctx context.Context, c *toolCall) toolResult {
		call, info, err := buildAdminCall(op, c.args)
		if err != nil {
			return invalidArguments(spec.Name, err)
		}
		rep, err := a.admin.do(ctx, call)
		if err != nil {
			res := adminFailure(spec.Name, err)
			res.integrationID = info.integrationID
			return res
		}
		out, err := adminOutput(spec, rep)
		if err != nil {
			return adminFailure(spec.Name, &adminError{Status: rep.status, Err: err})
		}
		res := toolResult{structured: out, integrationID: info.integrationID}
		if raw, ok := out["change_id"]; ok {
			_ = json.Unmarshal(raw, &res.changeID)
		}
		return res
	}
}

// callInfo is what the tool_called log line takes from the arguments.
type callInfo struct {
	integrationID string
}

// buildAdminCall turns validated tool arguments into the admin request of operation op.
func buildAdminCall(op string, raw json.RawMessage) (adminCall, callInfo, error) {
	call := adminCall{op: op, path: map[string]string{}}
	var info callInfo
	route, ok := adminRoutes[op]
	if !ok {
		return call, info, fmt.Errorf("no route for %s", op)
	}
	args := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &args); err != nil {
			return call, info, err
		}
	}
	if v, ok := args["integration_id"]; ok {
		_ = json.Unmarshal(v, &info.integrationID)
	}
	for _, name := range route.pathParams() {
		v, ok := args[name]
		if !ok {
			return call, info, fmt.Errorf("%s is required", name)
		}
		s, err := scalarString(v)
		if err != nil {
			return call, info, fmt.Errorf("%s: %w", name, err)
		}
		call.path[name] = s
		delete(args, name)
	}
	delete(args, "confirm") // consumed by the confirm guard; never sent
	if v, ok := args["if_match"]; ok {
		if err := json.Unmarshal(v, &call.ifMatch); err != nil {
			return call, info, fmt.Errorf("if_match: %w", err)
		}
		delete(args, "if_match")
	}
	if v, ok := args["reason"]; ok {
		var reason string
		if err := json.Unmarshal(v, &reason); err != nil {
			return call, info, fmt.Errorf("reason: %w", err)
		}
		b, err := json.Marshal(reason + reasonSuffix)
		if err != nil {
			return call, info, err
		}
		args["reason"] = b
	}
	if route.method == http.MethodGet {
		if len(args) > 0 {
			call.query = url.Values{}
		}
		for k, v := range args {
			s, err := scalarString(v)
			if err != nil {
				return call, info, fmt.Errorf("%s: %w", k, err)
			}
			call.query.Set(k, s)
		}
		return call, info, nil
	}
	body, err := json.Marshal(args) // nulls the model sent (e.g. expected_active_version) stay null
	if err != nil {
		return call, info, err
	}
	call.body = body
	return call, info, nil
}

// scalarString renders a JSON string, number or boolean as a path or query value. Integral
// numbers are written without a fraction (8.0 → 8).
func scalarString(v json.RawMessage) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.UseNumber()
	var x any
	if err := dec.Decode(&x); err != nil {
		return "", err
	}
	switch t := x.(type) {
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return strconv.FormatInt(n, 10), nil
		}
		f, err := t.Float64()
		if err != nil {
			return "", err
		}
		if f == float64(int64(f)) {
			return strconv.FormatInt(int64(f), 10), nil
		}
		return t.String(), nil
	default:
		return "", errors.New("must be a string, number or boolean")
	}
}

// adminOutput is the tool's structured content: the admin response object, plus etag for get
// tools, with list items never null. create_transformer_version drops the yaml and fixtures the
// model just sent (the contract's output: "yaml and fixtures as sent").
func adminOutput(spec *toolSpec, rep *adminReply) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if err := json.Unmarshal(rep.body, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if out == nil {
		return nil, errors.New("decode response: not an object")
	}
	if spec.outputETag {
		out["etag"] = json.RawMessage("null")
		if rep.etag != "" {
			b, err := json.Marshal(rep.etag)
			if err != nil {
				return nil, err
			}
			out["etag"] = b
		}
	}
	if items, ok := out["items"]; ok && strings.TrimSpace(string(items)) == "null" {
		out["items"] = json.RawMessage("[]")
	}
	if spec.Name == toolAdminCreateTransformerVersion {
		var tv map[string]json.RawMessage
		if err := json.Unmarshal(out["transformer_version"], &tv); err == nil && tv != nil {
			delete(tv, "yaml")
			delete(tv, "fixtures")
			b, err := json.Marshal(tv)
			if err != nil {
				return nil, err
			}
			out["transformer_version"] = b
		}
	}
	return out, nil
}
