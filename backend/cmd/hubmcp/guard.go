package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Local guards that run before any tool reaches the Hub (contracts/mcp/README.md "Admin write
// tools"). Each refusal is an isError result and makes no Hub call.

// codeSecretInInput is the MCP-only refusal for arguments that contain a Hub secret.
const codeSecretInInput = "SECRET_IN_INPUT"

// findSecret returns the JSON pointer of the first string in args (a key or a value, at any depth)
// that matches one of the contract's secret_patterns, or "" when there is none. Arguments that
// are not valid JSON are left to the schema check.
func findSecret(args json.RawMessage, patterns []*regexp.Regexp) string {
	if len(bytes.TrimSpace(args)) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(args, &v) != nil {
		return ""
	}
	match := func(s string) bool {
		for _, re := range patterns {
			if re.MatchString(s) {
				return true
			}
		}
		return false
	}
	var walk func(v any, ptr string) string
	walk = func(v any, ptr string) string {
		switch x := v.(type) {
		case string:
			if match(x) {
				return orRoot(ptr)
			}
		case map[string]any:
			for k, vv := range x {
				p := ptr + "/" + escapePointer(k)
				if match(k) {
					return p
				}
				if found := walk(vv, p); found != "" {
					return found
				}
			}
		case []any:
			for i, vv := range x {
				if found := walk(vv, ptr+"/"+strconv.Itoa(i)); found != "" {
					return found
				}
			}
		}
		return ""
	}
	return walk(v, "")
}

func orRoot(ptr string) string {
	if ptr == "" {
		return "/"
	}
	return ptr
}

// escapePointer escapes a JSON Pointer reference token (RFC 6901).
func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// secretRefusal is the SECRET_IN_INPUT result. It names where the secret is, never the value.
func secretRefusal(tool, pointer string) toolResult {
	msg := "The arguments contain what looks like a Hub secret (an API key or a signing secret) at " + pointer +
		". Nothing was sent. Secrets never go through MCP: remove it, and refer to keys by key_id or prefix."
	e := &errorObject{Code: codeSecretInInput, Message: msg, Details: []errorDetail{{Pointer: pointer, Keyword: "secret", Message: msg}}}
	res := failure(tool, e, 0)
	res.outcome = "refused"
	res.note = "REFUSED by hubmcp, the Hub was not called: the arguments contain a secret."
	if tool == toolInvoke {
		res.structured = invokeOutput{Outcome: "refused", Error: e}
	}
	return res
}

// confirmGiven reports whether the arguments carry confirm: true.
func confirmGiven(args json.RawMessage) bool {
	var in struct {
		Confirm bool `json:"confirm"`
	}
	return json.Unmarshal(args, &in) == nil && in.Confirm
}

// confirmRefusal is the CONFIRMATION_REQUIRED result of a requires_confirm admin tool called
// without confirm: true. The message says what the model must show the user first.
func confirmRefusal(spec *toolSpec) toolResult {
	var show string
	switch spec.RequiresConfirm {
	case "mints_secret":
		show = "the owner (tenant), the scopes or URL and the expiry of the secret this mints"
	default:
		show = "exactly what changes for live traffic: for publish the validation result and the diff from the active version, " +
			"for rollback the diff to to_version, for weights the current next to the new weights, for updates the current and new values"
	}
	msg := spec.Name + " needs confirm=true. Nothing was sent. Show the user " + show +
		", get their explicit approval, then call it again with confirm=true."
	e := &errorObject{Code: codeConfirmationRequired, Message: msg, Details: []errorDetail{{Pointer: "/confirm", Keyword: "required", Message: msg}}}
	res := failure(spec.Name, e, 0)
	res.outcome = "refused"
	res.note = "REFUSED by hubmcp, the Hub was not called: " + spec.Name + " requires_confirm=" + spec.RequiresConfirm + "."
	return res
}
