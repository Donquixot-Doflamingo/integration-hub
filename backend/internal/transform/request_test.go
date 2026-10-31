package transform

import (
	"errors"
	"strings"
	"testing"
)

var t1Input = Input{
	Req: map[string]any{
		"user":  map[string]any{"id": "u_48213"},
		"event": map[string]any{"name": "order_placed", "timestamp": "2026-10-31T09:15:00Z", "properties": map[string]any{"amount": 499.0}},
	},
	Account: map[string]any{"id": "ct-brandA-in", "region": "in", "config": map[string]any{"brand_code": "A"}},
	Creds:   map[string]string{"account_id": "fixture-account-id", "passcode": "fixture-passcode-secret"},
	Now:     fixedNow,
}

func compileDev(t testing.TB, rel string) *Program {
	t.Helper()
	p, err := Compile(devSpec(t, rel), Options{RequestSchema: eventsSchema(t)})
	if err != nil {
		t.Fatalf("compile %s: %v", rel, err)
	}
	return p
}

func TestBuildRequestT1(t *testing.T) {
	req, err := compileDev(t, "T1/7.yaml").BuildRequest(t1Input)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "POST" || req.Path != "/1/upload" {
		t.Fatalf("method/path %s %s", req.Method, req.Path)
	}
	if req.Headers["X-CleverTap-Account-Id"] != "fixture-account-id" || req.Headers["X-CleverTap-Passcode"] != "fixture-passcode-secret" || req.Headers["Content-Type"] != "application/json; charset=utf-8" {
		t.Fatalf("headers %v", req.Headers)
	}
	want := map[string]any{"d": []any{map[string]any{
		"identity": "u_48213", "type": "event", "evtName": "order_placed", "ts": 1793438100, "evtData": map[string]any{"amount": 499.0},
	}}}
	if !equalJSON(req.Body, want) {
		t.Fatalf("body %#v", req.Body)
	}
}

func TestBuildRequestT2UsesAccountConfig(t *testing.T) {
	req, err := compileDev(t, "T2/3.yaml").BuildRequest(t1Input)
	if err != nil {
		t.Fatal(err)
	}
	data := req.Body.(map[string]any)["d"].([]any)[0].(map[string]any)["evtData"]
	if !equalJSON(data, map[string]any{"amount": 499.0, "brand": "A", "campaign": "organic"}) {
		t.Fatalf("evtData %#v", data)
	}
}

func TestDryRunEqualsBuildWithSecretsRedacted(t *testing.T) {
	p := compileDev(t, "T1/7.yaml")
	built, err := p.BuildRequest(t1Input)
	if err != nil {
		t.Fatal(err)
	}
	dry, err := p.DryRun(t1Input)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Headers["X-CleverTap-Account-Id"] != Redacted || dry.Headers["X-CleverTap-Passcode"] != Redacted {
		t.Fatalf("creds not redacted: %v", dry.Headers)
	}
	if dry.Headers["Content-Type"] != built.Headers["Content-Type"] || dry.Method != built.Method || dry.Path != built.Path || !equalJSON(dry.Body, built.Body) {
		t.Fatal("dry-run differs from the built request beyond redaction")
	}
}

func TestDryRunRedactsCredsInAllowedBody(t *testing.T) {
	src := strings.Replace(reqHead, "kind: request\n", "kind: request\nallow_creds_in_body: true\n", 1) +
		"  body:\n    auth: format(\"%s:%s\", creds.account_id, creds.passcode)\n    id: req.user.id\n"
	p, err := compileSrc(t, src)
	if err != nil {
		t.Fatal(err)
	}
	dry, err := p.DryRun(t1Input)
	if err != nil {
		t.Fatal(err)
	}
	body := dry.Body.(map[string]any)
	if body["auth"] != Redacted+":"+Redacted || body["id"] != "u_48213" {
		t.Fatalf("body %v", body)
	}
}

func TestHeaderValuesMustBeScalars(t *testing.T) {
	p, err := compileSrc(t, reqHead+"  headers:\n    X-Props: req.event.properties\n    X-Missing: req.context.campaign\n")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.BuildRequest(t1Input)
	var ee *EvalError
	if !errors.As(err, &ee) || ee.Path != "request.headers.X-Props" || !strings.Contains(ee.Error(), "must evaluate to a string") {
		t.Fatalf("err %v", err)
	}
}

func TestNilHeaderIsOmitted(t *testing.T) {
	p, err := compileSrc(t, reqHead+"  headers:\n    X-Campaign: req.context.campaign\n  query:\n    v: '2'\n")
	if err != nil {
		t.Fatal(err)
	}
	req, err := p.BuildRequest(t1Input)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Headers["X-Campaign"]; ok || req.Query["v"] != "2" {
		t.Fatalf("headers %v query %v", req.Headers, req.Query)
	}
}
