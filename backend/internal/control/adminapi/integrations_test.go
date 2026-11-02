package adminapi

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
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
	v, err := fromAPISpec(s, "/spec", 6*time.Hour)
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
		_, err := fromAPISpec(s, "/spec", 0)
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
