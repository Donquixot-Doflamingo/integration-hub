package adminapi

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/multiop"
)

func decodeJSON(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDiffJSON(t *testing.T) {
	for _, tc := range []struct {
		name, a, b string
		want       []string // "op path"
	}{
		{"equal", `{"a":1}`, `{"a":1}`, nil},
		{"replace leaf", `{"targets":[{"weight":100}]}`, `{"targets":[{"weight":90}]}`, []string{"replace /targets/0/weight"}},
		{"add element", `{"t":[1]}`, `{"t":[1,2]}`, []string{"add /t/1"}},
		{"remove elements from the end", `{"t":[1,2,3]}`, `{"t":[1]}`, []string{"remove /t/2", "remove /t/1"}},
		{"add and remove members", `{"a":1,"b":{"x":1}}`, `{"b":{"x":1},"c":2}`, []string{"remove /a", "add /c"}},
		{"type change", `{"a":{"x":1}}`, `{"a":[1]}`, []string{"replace /a"}},
		{"escaped key", `{"a/b":1}`, `{"a/b":2}`, []string{"replace /a~1b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, op := range diffJSON("", decodeJSON(t, tc.a), decodeJSON(t, tc.b)) {
				got = append(got, op.Op+" "+op.Path)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnifiedDiff(t *testing.T) {
	a := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"}
	b := []string{"a", "b", "c", "D", "e", "f", "g", "h", "i", "j", "k", "l", "m"}
	got := unifiedDiff("v7", "v8", a, b)
	want := "--- v7\n+++ v8\n@@ -1,7 +1,7 @@\n a\n b\n c\n-d\n+D\n e\n f\n g\n@@ -10,3 +10,4 @@\n j\n k\n l\n+m\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if got := unifiedDiff("v1", "v2", a, a); got != "--- v1\n+++ v2\n" {
		t.Fatalf("no change: %q", got)
	}
}

// TestSpecRoundTrip: a contract spec converts to a registry version and renders back to the
// same spec (defaults filled); unsupported members are refused at their pointer.
func TestSpecRoundTrip(t *testing.T) {
	in := `{"targets":[{"vendor_id":"clevertap","account_id":"ct-brandA-in","transformer":{"request":"T1@7","response":"R1@2"},"weight":90},
	  {"vendor_id":"clevertap","account_id":"ct-brandB-us","transformer":{"request":"T2@3","response":"R1@2"},"weight":10}],
	  "failover":[],"delivery":{"mode":"wait","polling":true},"breaker":{"override":{"open_after_consecutive_timeouts":3}},
	  "webhooks":[{"event":"delivery.status","endpoint_id":"reg_orders_cb"}],"audit":{"payloads":"masked"}}`
	var s apiSpec
	if err := json.Unmarshal([]byte(in), &s); err != nil {
		t.Fatal(err)
	}
	v, err := fromAPISpec(s, 6*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got := renderSpec(v)
	want := decodeJSON(t, `{"targets":[{"vendor_id":"clevertap","account_id":"ct-brandA-in","transformer":{"request":"T1@7","response":"R1@2"},"weight":90},
	  {"vendor_id":"clevertap","account_id":"ct-brandB-us","transformer":{"request":"T2@3","response":"R1@2"},"weight":10}],
	  "failover":[],"delivery":{"mode":"wait","wait_ms":3000,"ttl_ms":21600000,"on_circuit_open":"fail_fast","pending":"re_resolve","polling":true},
	  "breaker":{"override":{"open_after_consecutive_timeouts":3}},
	  "webhooks":[{"event":"delivery.status","endpoint_id":"reg_orders_cb"}],"audit":{"payloads":"masked","retention_ms":2592000000}}`)
	if !reflect.DeepEqual(any(got), want) {
		t.Fatalf("render\n%v\nwant\n%v", got, want)
	}
	doc, err := versionYAML(v)
	if err != nil {
		t.Fatal(err)
	}
	back, err := parseVersionYAML(doc)
	if err != nil || !reflect.DeepEqual(renderSpec(back), got) {
		t.Fatalf("YAML round trip: %v\n%s", err, doc)
	}

	for _, tc := range []struct{ body, pointer string }{
		{`{"targets":[{"vendor_id":"v","account_id":"a","transformer":{"request":"T1@1","response":"R1@1"},"params":{"model":"x"}}],"delivery":{"mode":"sync"},"audit":{"payloads":"masked"}}`, "/spec/targets/0/params"},
		{`{"targets":[{"vendor_id":"v","account_id":"a","transformer":{"request":"T1@1","response":"R1@1"}}],"delivery":{"mode":"sync","retry":{"max_attempts":2}},"audit":{"payloads":"masked"}}`, "/spec/delivery/retry"},
		{`{"targets":[{"vendor_id":"v","account_id":"a","transformer":{"request":"T1@1","response":"R1@1"}}],"delivery":{"mode":"sync"},"breaker":{"override":{"min_calls":5}},"audit":{"payloads":"masked"}}`, "/spec/breaker/override/min_calls"},
		{`{"targets":[{"vendor_id":"v","account_id":"a","transformer":{"request":"T1@1","response":"R1@1"}}],"failover":[{"vendor_id":"v","account_id":"b","transformer":{"request":"T1@1","response":"R1@1"},"weight":5}],"delivery":{"mode":"sync"},"audit":{"payloads":"masked"}}`, "/spec/failover/0/weight"},
		{`{"targets":[],"delivery":{"mode":"sync"},"audit":{"payloads":"masked"}}`, "/spec/targets"},
	} {
		var s apiSpec
		if err := json.Unmarshal([]byte(tc.body), &s); err != nil {
			t.Fatal(err)
		}
		_, err := fromAPISpec(s, 0)
		var e *Error
		if !errors.As(err, &e) || e.Status != 400 || len(e.Details) != 1 || e.Details[0].Pointer != tc.pointer {
			t.Errorf("%s: %v", tc.pointer, err)
		}
	}
}

func TestSpecProblemsUseContractNames(t *testing.T) {
	ps := specProblems(registry.ConfigErrors{
		{File: "integrations/int_x/versions/3.yaml", Path: "targets[1].account", Message: "account is retired"},
		{File: "integrations/int_x/versions/3.yaml", Path: "webhooks[0].endpoint", Message: "unknown"},
		{File: "accounts/a.yaml", Path: "vendor", Message: "unknown vendor"},
	}, "int_x")
	got := []string{pointer(ps[0].Path), pointer(ps[1].Path), pointer(ps[2].Path)}
	if want := []string{"/targets/1/account_id", "/webhooks/0/endpoint_id", "/vendor"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if !strings.HasPrefix(ps[0].File, "integrations/") {
		t.Fatal("file lost")
	}
}

// TestSpecOperationsRoundTrip is HUB-343: TargetRef.operations converts to registry
// Target.Operations and renders back (targets, failover, shadow); malformed members are 400 at
// their pointer.
func TestSpecOperationsRoundTrip(t *testing.T) {
	const pairs = `"transformer":{"request":"GEN@1","response":"GEN_R@1"},"operations":{"verify":{"request":"VER@1","response":"VER_R@1"}}`
	in := `{"targets":[{"vendor_id":"kycdemo","account_id":"kycdemo-in",` + pairs + `,"weight":100}],
	  "failover":[{"vendor_id":"kycdemo","account_id":"kycdemo-b",` + pairs + `}],
	  "shadow":{"target":{"vendor_id":"kycdemo","account_id":"kycdemo-c",` + pairs + `},"mode":"dry_run"},
	  "delivery":{"mode":"sync","on_circuit_open":"failover"},"webhooks":[],"audit":{"payloads":"sealed"}}`
	var s apiSpec
	if err := json.Unmarshal([]byte(in), &s); err != nil {
		t.Fatal(err)
	}
	v, err := fromAPISpec(s, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	want := registry.TransformerPair{Request: registry.TransformerRef{ID: "VER", Version: 1}, Response: registry.TransformerRef{ID: "VER_R", Version: 1}}
	for _, tg := range []registry.Target{v.Targets[0], v.Failover[0], v.Shadow.Target} {
		if tg.Operations["verify"] != want || tg.Pair("generate").Request.ID != "GEN" {
			t.Fatalf("%s: operations %v", tg.Account, tg.Operations)
		}
	}
	got := renderSpec(v)
	for _, ptr := range []func(map[string]any) any{
		func(m map[string]any) any { return m["targets"].([]any)[0] },
		func(m map[string]any) any { return m["failover"].([]any)[0] },
		func(m map[string]any) any { return m["shadow"].(map[string]any)["target"] },
	} {
		ops := ptr(got).(map[string]any)["operations"]
		if !reflect.DeepEqual(ops, decodeJSON(t, `{"verify":{"request":"VER@1","response":"VER_R@1"}}`)) {
			t.Fatalf("rendered operations %v", ops)
		}
	}
	doc, err := versionYAML(v)
	if err != nil {
		t.Fatal(err)
	}
	back, err := parseVersionYAML(doc)
	if err != nil || !reflect.DeepEqual(renderSpec(back), got) {
		t.Fatalf("YAML round trip: %v\n%s", err, doc)
	}
	// A single-pair target renders no operations member.
	if _, ok := renderTarget(registry.Target{Transformer: v.Targets[0].Transformer}, false)["operations"]; ok {
		t.Fatal("operations rendered for a target without them")
	}

	target := func(ops string) string {
		return `{"targets":[{"vendor_id":"v","account_id":"a","transformer":{"request":"T1@1","response":"R1@1"},"operations":` + ops + `}],"delivery":{"mode":"sync"},"audit":{"payloads":"masked"}}`
	}
	for _, tc := range []struct{ body, pointer string }{
		{target(`{}`), "/spec/targets/0/operations"},
		{target(`{"Verify":{"request":"V@1","response":"VR@1"}}`), "/spec/targets/0/operations/Verify"},
		{target(`{"verify":{"request":"V","response":"VR@1"}}`), "/spec/targets/0/operations/verify/request"},
		{target(`{"verify":{"request":"V@1"}}`), "/spec/targets/0/operations/verify/response"},
	} {
		var s apiSpec
		if err := json.Unmarshal([]byte(tc.body), &s); err != nil {
			t.Fatal(err)
		}
		_, err := fromAPISpec(s, 0)
		var e *Error
		if !errors.As(err, &e) || e.Status != 400 || len(e.Details) != 1 || e.Details[0].Pointer != tc.pointer {
			t.Errorf("%s: %v", tc.pointer, err)
		}
	}
}

// TestOperationsProblemsMapToTheirCheck: registry operations_have_transformers problems report
// that check with a contract pointer; operation names are never renamed like member names.
func TestOperationsProblemsMapToTheirCheck(t *testing.T) {
	const f = "integrations/int_x/versions/3.yaml"
	ps := specProblems(registry.ConfigErrors{
		{File: f, Path: "targets[0].transformer", Message: "operation verify uses the default pair, but G@1 is compiled for operation generate: give verify its own pair under operations"},
		{File: f, Path: "failover[1].operations.account", Message: `"account" is not an operation of capability x@v1 (it declares generate, verify)`},
		{File: f, Path: "shadow.target.operations.verify", Message: "G@1 is compiled for operation generate, not verify"},
		{File: f, Path: "targets[0].operations.verify.request", Message: "unknown transformer V@9"},
	}, "int_x")
	want := []struct{ ptr, check string }{
		{"/targets/0/transformer", registry.CheckOperationsHaveTransformers},
		{"/failover/1/operations/account", registry.CheckOperationsHaveTransformers},
		{"/shadow/target/operations/verify", registry.CheckOperationsHaveTransformers},
		{"/targets/0/operations/verify/request", "references_exist"},
	}
	for i, w := range want {
		if got := pointer(ps[i].Path); got != w.ptr || checkOf(ps[i]) != w.check {
			t.Errorf("%d: %s %s, want %s %s", i, got, checkOf(ps[i]), w.ptr, w.check)
		}
	}
	report := validationReport(ps)
	if report["valid"] != false {
		t.Fatal("report valid")
	}
	if !slices.Contains(knownChecks, registry.CheckOperationsHaveTransformers) {
		t.Fatal("operations_have_transformers is not a known check")
	}
}

// perOpSnapshot builds the dev config plus the two-operation overlay (internal/testkit/multiop).
func perOpSnapshot(t *testing.T) *registry.Snapshot {
	t.Helper()
	merge := func(fsyss ...fs.FS) fstest.MapFS {
		m := fstest.MapFS{}
		for _, fsys := range fsyss {
			for p, s := range multiop.Files(fsys) {
				m[p] = &fstest.MapFile{Data: []byte(s)}
			}
		}
		return m
	}
	snap, err := registry.Build(registry.Sources{Config: merge(os.DirFS("../../../config"), multiop.Config()),
		Contracts: merge(os.DirFS("../../../../contracts"), multiop.Contracts())}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// TestPerOperationEndpointsAreListed: breakers, transformer usage and the paired endpoint of a
// response transformer account for every per-operation pair, not only the default one.
func TestPerOperationEndpointsAreListed(t *testing.T) {
	snap := perOpSnapshot(t)
	refs := breakerTargets(snap)
	for _, account := range []string{"sv-primary", "sv-backup"} {
		for _, ep := range []string{"send@1", "verify@1"} {
			if got := refs[breakerRef{Account: account, Vendor: "stepvendor", Endpoint: ep}]; !slices.Equal(got, []string{multiop.Integration}) {
				t.Errorf("breaker %s %s: %v", account, ep, got)
			}
		}
	}
	if n := usedByActive(snap, "TS-VERIFY-REQ"); n != 1 {
		t.Errorf("TS-VERIFY-REQ used by %d active integrations", n)
	}
	if ep := pairedEndpoint(snap, registry.TransformerRef{ID: "TS-VERIFY-RES", Version: 1}); ep != "stepvendor.verify@1" {
		t.Errorf("paired endpoint %q", ep)
	}
}
