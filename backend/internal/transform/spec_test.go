package transform

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// specFile mirrors the registry's TransformerFile so tests can read config files directly.
type specFile struct {
	Transformer      string    `yaml:"transformer"`
	Version          int       `yaml:"version"`
	Kind             string    `yaml:"kind"`
	Capability       string    `yaml:"capability"`
	VendorEndpoint   string    `yaml:"vendor_endpoint"`
	DocsURL          string    `yaml:"docs_url"`
	AllowCredsInBody bool      `yaml:"allow_creds_in_body"`
	Request          yaml.Node `yaml:"request"`
	Response         yaml.Node `yaml:"response"`
	Errors           yaml.Node `yaml:"errors"`
	Batch            yaml.Node `yaml:"batch"`
}

func specFromYAML(t testing.TB, src string) Spec {
	t.Helper()
	var f specFile
	if err := yaml.Unmarshal([]byte(src), &f); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	return Spec{ID: f.Transformer, Version: f.Version, Kind: f.Kind, Capability: f.Capability,
		Request: f.Request, Response: f.Response, Errors: f.Errors, Batch: f.Batch, AllowCredsInBody: f.AllowCredsInBody}
}

func devSpec(t testing.TB, rel string) Spec {
	t.Helper()
	b, err := os.ReadFile("../../config/transformers/" + rel)
	if err != nil {
		t.Fatal(err)
	}
	return specFromYAML(t, string(b))
}

func TestParseT1(t *testing.T) {
	p, err := parse(devSpec(t, "T1/7.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if p.method != "POST" || p.path != "/1/upload" || len(p.headers) != 3 || p.batchPath != "d" || p.batchMax != 1000 {
		t.Fatalf("request header %+v", p)
	}
	if h := p.headers[0]; h.key != "X-CleverTap-Account-Id" || h.node.expr != "creds.account_id" {
		t.Fatalf("header 0 %+v", h)
	}
	d := p.body.fields[0]
	if d.key != "d" || d.node.kind != nodeArray || len(d.node.items) != 1 {
		t.Fatalf("body.d %+v", d)
	}
	rec := d.node.items[0]
	want := map[string]string{"identity": "req.user.id", "type": `"event"`, "evtName": "req.event.name",
		"ts": "epochSeconds(req.event.timestamp ?? now())", "evtData": "req.event.properties"}
	for _, f := range rec.fields {
		if f.node.expr != want[f.key] {
			t.Errorf("%s = %q, want %q", f.node.path, f.node.expr, want[f.key])
		}
	}
	if rec.fields[0].node.path != "request.body.d[0].identity" {
		t.Errorf("path %q", rec.fields[0].node.path)
	}
}

func TestParseR1(t *testing.T) {
	p, err := parse(devSpec(t, "R1/2.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if p.response == nil || len(p.response.fields) != 5 || p.response.fields[0].key != "status" {
		t.Fatalf("response %+v", p.response)
	}
	if len(p.errors) != 4 || p.errors[0].code != "UPSTREAM_AUTH_FAILED" || p.errors[1].retryAfterMS == "" || p.errors[3].retryable {
		t.Fatalf("errors %+v", p.errors)
	}
}

func TestParseRejects(t *testing.T) {
	const reqHead = "transformer: X\nversion: 1\nkind: request\ncapability: events.track@v1\n"
	const respHead = "transformer: R\nversion: 1\nkind: response\ncapability: events.track@v1\n"
	tests := []struct {
		name, src, want string
	}{
		{"unknown request key", reqHead + "request:\n  method: POST\n  path: /x\n  heders: {}\n", "request.heders: unknown key"},
		{"duplicate body target", reqHead + "request:\n  method: POST\n  path: /x\n  body:\n    a: req.x\n    a: req.y\n", "request.body.a: target defined twice"},
		{"header case conflict", reqHead + "request:\n  method: POST\n  path: /x\n  headers:\n    Content-Type: '\"a\"'\n    content-type: '\"b\"'\n", "request.headers.content-type: conflicts with request.headers.Content-Type"},
		{"header not an expression", reqHead + "request:\n  method: POST\n  path: /x\n  headers:\n    X-Count: 5\n", "request.headers.X-Count: must be an expression"},
		{"missing method", reqHead + "request:\n  path: /x\n", "request.method: required"},
		{"relative path", reqHead + "request:\n  method: POST\n  path: x\n", "request.path: must be a literal path"},
		{"response section in request transformer", reqHead + "request:\n  method: GET\n  path: /x\nresponse:\n  a: res.status\n", "response: not allowed in a request transformer"},
		{"request section in response transformer", respHead + "request:\n  method: GET\n  path: /x\nresponse:\n  a: res.status\n", "request: not allowed in a response transformer"},
		{"mapped TRANSFORM_FAILED", respHead + "response:\n  a: res.status\nerrors:\n  - when: res.status >= 500\n    code: TRANSFORM_FAILED\n", "errors[0].code: must be one of"},
		{"unknown error key", respHead + "response:\n  a: res.status\nerrors:\n  - when: 'true'\n    code: UPSTREAM_REJECTED\n    retry: true\n", "errors[0].retry: unknown key"},
		{"batch path not an array", reqHead + "request:\n  method: POST\n  path: /x\n  body:\n    d: req.x\nbatch: { path: d, max: 10 }\n", `batch.path: "d" must name an array`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse(specFromYAML(t, tt.src))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want %q", err, tt.want)
			}
		})
	}
}
