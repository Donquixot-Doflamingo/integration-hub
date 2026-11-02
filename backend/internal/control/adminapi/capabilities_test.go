package adminapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func capOps(t *testing.T, req, resp string) map[string]capOperation {
	t.Helper()
	yes := true
	return map[string]capOperation{"default": {Idempotent: &yes, RequestSchema: json.RawMessage(req), ResponseSchema: json.RawMessage(resp)}}
}

const (
	baseReq  = `{"type":"object","required":["user"],"additionalProperties":false,"properties":{"user":{"type":"string","maxLength":64,"x-pii":"identifier"},"kind":{"type":"string","enum":["a","b"]}}}`
	baseResp = `{"type":"object","required":["status"],"properties":{"status":{"type":"string"}}}`
)

func TestAdditive(t *testing.T) {
	cases := []struct {
		name      string
		req, resp string
		want      string // "" = additive; else a substring of the first problem
	}{
		{"same", baseReq, baseResp, ""},
		{"new optional request field", strings.Replace(baseReq, `"kind":`, `"campaign":{"type":"string"},"kind":`, 1), baseResp, ""},
		{"new response field", baseReq, strings.Replace(baseResp, `"status":{`, `"ref":{"type":"string"},"status":{`, 1), ""},
		{"looser bound", strings.Replace(baseReq, `"maxLength":64`, `"maxLength":128`, 1), baseResp, ""},
		{"enum value added", strings.Replace(baseReq, `["a","b"]`, `["a","b","c"]`, 1), baseResp, ""},
		{"field removed", strings.Replace(baseReq, `,"kind":{"type":"string","enum":["a","b"]}`, ``, 1), baseResp, `"kind" was removed`},
		{"newly required", strings.Replace(baseReq, `"required":["user"]`, `"required":["user","kind"]`, 1), baseResp, `"kind" is newly required`},
		{"tighter bound", strings.Replace(baseReq, `"maxLength":64`, `"maxLength":32`, 1), baseResp, "maxLength was tightened"},
		{"pattern added", strings.Replace(baseReq, `"maxLength":64`, `"maxLength":64,"pattern":"^u"`, 1), baseResp, "pattern was added"},
		{"enum value removed", strings.Replace(baseReq, `["a","b"]`, `["a"]`, 1), baseResp, "enum value b was removed"},
		{"type changed", strings.Replace(baseReq, `"user":{"type":"string"`, `"user":{"type":"integer"`, 1), baseResp, "type changed"},
		{"x-pii removed", strings.Replace(baseReq, `,"x-pii":"identifier"`, ``, 1), baseResp, "x-pii"},
		{"response field not required", baseReq, strings.Replace(baseResp, `"required":["status"]`, `"required":[]`, 1), `"status" is no longer required`},
		{"response closed", baseReq, strings.Replace(baseResp, `"type":"object",`, `"type":"object","additionalProperties":false,`, 1), "additionalProperties was closed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := additive(capOps(t, baseReq, baseResp), capOps(t, c.req, c.resp))
			switch {
			case c.want == "" && len(ps) > 0:
				t.Fatalf("refused: %v", ps)
			case c.want != "" && (len(ps) == 0 || !strings.Contains(ps[0].Message, c.want)):
				t.Fatalf("want %q, got %v", c.want, ps)
			}
		})
	}
	if ps := additive(capOps(t, baseReq, baseResp), map[string]capOperation{}); len(ps) != 1 || !strings.Contains(ps[0].Message, "removed") {
		t.Fatalf("removed operation: %v", ps)
	}
	e := (&additiveError{additive(capOps(t, baseReq, baseResp), capOps(t, strings.Replace(baseReq, `"maxLength":64`, `"maxLength":1`, 1), baseResp))}).adminError()
	if e.Status != 422 || e.Code != CodePublishValidationFailed || e.Details[0].Keyword != "additive_change" ||
		e.Details[0].Pointer != "/operations/default/request_schema/properties/user/maxLength" {
		t.Fatalf("admin error %+v", e)
	}
}

func TestSchemaFileNaming(t *testing.T) {
	if got := schemaFile("events.track", 1, "default", "request"); got != "schemas/capabilities/events.track.v1.request.json" {
		t.Fatal(got)
	}
	if got := schemaFile("kyc.aadhaar_otp", 1, "verify", "response"); got != "schemas/capabilities/kyc.aadhaar_otp.v1.verify.response.json" {
		t.Fatal(got)
	}
	if name, major, ok := splitCapKey("kyc.aadhaar_otp@v12"); !ok || name != "kyc.aadhaar_otp" || major != 12 {
		t.Fatal(name, major, ok)
	}
	if _, _, ok := splitCapKey("events@v1"); ok {
		t.Fatal("a one-segment name parsed")
	}
}
