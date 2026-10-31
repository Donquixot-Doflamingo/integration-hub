package transform

import (
	"os"
	"strings"
	"testing"
)

func eventsSchema(t testing.TB) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../contracts/schemas/capabilities/events.track.v1.request.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const reqHead = "transformer: X\nversion: 1\nkind: request\ncapability: events.track@v1\nrequest:\n  method: POST\n  path: /x\n"
const respHead = "transformer: R\nversion: 1\nkind: response\ncapability: events.track@v1\n"

func compileSrc(t testing.TB, src string) (*Program, error) {
	t.Helper()
	return Compile(specFromYAML(t, src), Options{RequestSchema: eventsSchema(t)})
}

func TestCompileRejects(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{"missing field", reqHead + "  body:\n    id: req.user.idd\n", `transformer X@1: request.body.id: req.user.idd: field "user.idd" is not in the capability schema`},
		{"missing top-level field", reqHead + "  body:\n    x: req.usr.id\n", `field "usr" is not in the capability schema`},
		{"res in request mapping", reqHead + "  body:\n    s: res.status\n", "request.body.s: res is not available in request mapping (DESIGN §7.2)"},
		{"req in response mapping", respHead + "response:\n  status: req.user.id\n", "response.status: req is not available in response mapping"},
		{"creds in body", reqHead + "  body:\n    key: creds.passcode\n", "request.body.key: creds.* may only be used in headers or query"},
		{"unknown variable", reqHead + "  body:\n    x: secrets.key\n", "request.body.x: unknown name secrets"},
		{"disabled loop builtin", reqHead + "  body:\n    x: 'reduce([1, 2], # + #acc, 0)'\n", "request.body.x: unknown name reduce"},
		{"syntax error", reqHead + "  body:\n    x: req.user.id ==\n", "request.body.x: syntax:"},
		{"too deep", reqHead + "  body:\n    x: " + strings.Repeat("(", 30) + "1" + strings.Repeat(")", 30) + "\n", "request.body.x: expression is nested 30 deep, limit 24"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compileSrc(t, tt.src)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v\nwant %q", err, tt.want)
			}
		})
	}
}

func TestCompileAcceptsSchemaPathsAndFreeFormObjects(t *testing.T) {
	src := reqHead + "  headers:\n    X-Key: creds.passcode\n  body:\n    id: req.user.id\n    name: req.event.name\n    any: req.event.properties.anything.deeper\n    campaign: req.context?.campaign ?? \"organic\"\n    brand: account.config.brand_code\n"
	if _, err := compileSrc(t, src); err != nil {
		t.Fatalf("valid transformer rejected: %v", err)
	}
}

func TestCredsInBodyAllowedWhenDeclared(t *testing.T) {
	src := strings.Replace(reqHead, "kind: request\n", "kind: request\nallow_creds_in_body: true\n", 1) + "  body:\n    key: creds.passcode\n"
	if _, err := compileSrc(t, src); err != nil {
		t.Fatalf("allow_creds_in_body ignored: %v", err)
	}
}

// TestNoCompileOnTheRequestPath: evaluating a compiled program never compiles again (HUB-061).
func TestNoCompileOnTheRequestPath(t *testing.T) {
	p, err := compileSrc(t, reqHead+"  body:\n    id: req.user.id\n    tags: 'map([1, 2, 3], # * 2)'\n")
	if err != nil {
		t.Fatal(err)
	}
	before := CompiledExpressions()
	env := p.env(Input{Req: map[string]any{"user": map[string]any{"id": "u_1"}}}, nil)
	for range 1000 {
		out, err := p.eval(p.body, env)
		if err != nil {
			t.Fatal(err)
		}
		if out.(map[string]any)["id"] != "u_1" {
			t.Fatalf("out %v", out)
		}
	}
	if after := CompiledExpressions(); after != before {
		t.Fatalf("compiled %d expressions while evaluating", after-before)
	}
}

func TestNilFieldsAreOmitted(t *testing.T) {
	p, err := compileSrc(t, reqHead+"  body:\n    id: req.user.id\n    props: req.event.properties\n")
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.eval(p.body, p.env(Input{Req: map[string]any{"user": map[string]any{"id": "u_1"}}}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, present := out.(map[string]any)["props"]; present {
		t.Fatalf("nil field sent: %v", out)
	}
}

func TestMissingParentObjectsReadAsNil(t *testing.T) {
	p, err := compileSrc(t, reqHead+"  body:\n    campaign: req.context.campaign ?? \"organic\"\n")
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.eval(p.body, p.env(Input{Req: map[string]any{"user": map[string]any{"id": "u"}}}, nil))
	if err != nil || out.(map[string]any)["campaign"] != "organic" {
		t.Fatalf("out %v err %v", out, err)
	}
}

func TestNestingDepth(t *testing.T) {
	for src, want := range map[string]int{`a`: 0, `f(g(x))`: 2, `"((("`: 0, `[{a: (1)}]`: 3, `'\'(' + (x)`: 1} {
		if got := nestingDepth(src); got != want {
			t.Errorf("nestingDepth(%q) = %d, want %d", src, got, want)
		}
	}
}
