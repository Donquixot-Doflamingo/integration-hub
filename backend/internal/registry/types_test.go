package registry

import (
	"bytes"
	"os"
	"reflect"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// decodeFile strictly decodes a testdata file (unknown fields fail).
func decodeFile(t *testing.T, name string, v any) {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

// roundTrip marshals v and strictly decodes it into out.
func roundTrip(t *testing.T, v, out any) {
	t.Helper()
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		t.Fatalf("re-decode: %v\n%s", err, b)
	}
}

// The DESIGN §3.4 and §8.3 examples are extracted verbatim into testdata/design.
func TestDesignIntegrationVersionRoundTrips(t *testing.T) {
	var v IntegrationVersion
	decodeFile(t, "testdata/design/3.4-integration-version.yaml", &v)
	if v.Integration != "int_evt_brandB_min" || v.Version != 3 || v.Op("default") == nil || !v.Op("default").Idempotent {
		t.Fatalf("header %+v", v)
	}
	tg := v.Targets[0]
	if tg.Account != "ct-brandB-us" || tg.Transformer.Request != (TransformerRef{"T2", 3}) || tg.Transformer.Response != (TransformerRef{"R2", 1}) || tg.Weight != 100 {
		t.Fatalf("target %+v", tg)
	}
	if v.Failover[0].Vendor != "moengage" || v.Failover[0].Transformer.Request.String() != "T4@1" {
		t.Fatalf("failover %+v", v.Failover)
	}
	d := v.Delivery
	if d.Mode != ModeWait || d.WaitMS != 3000 || d.TTL.Std() != 6*time.Hour || d.OnCircuitOpen != QueueIfAllowed || d.Pending != ReResolve {
		t.Fatalf("delivery %+v", d)
	}
	if v.Breaker.Override.OpenAfterConsecutiveTimeouts != 3 || v.Webhooks[0] != (Subscription{"delivery.status", "reg_orders_cb"}) {
		t.Fatalf("breaker/webhooks %+v %+v", v.Breaker, v.Webhooks)
	}
	if v.Audit.Payloads != PayloadMasked || v.Audit.Retention.Std() != 30*24*time.Hour || v.Publish.Author != "platform-team" {
		t.Fatalf("audit/publish %+v %+v", v.Audit, v.Publish)
	}
	var back IntegrationVersion
	roundTrip(t, v, &back)
	if back.Delivery != v.Delivery || !reflect.DeepEqual(back.Targets, v.Targets) || back.Audit != v.Audit || back.Publish != v.Publish {
		t.Fatalf("round trip changed the record:\n%+v\n%+v", v, back)
	}
}

func TestDesignVendorAndAccountRoundTrip(t *testing.T) {
	var v Vendor
	decodeFile(t, "testdata/design/8.3-vendor.yaml", &v)
	ep := v.Endpoints["upload@1"]
	if v.Vendor != "clevertap" || v.Auth != "static_headers" || v.Regions["in"] != "https://in1.api.clevertap.com" ||
		ep.Method != "POST" || ep.Path != "/1/upload" || ep.TimeoutMS != 3000 || ep.ConnectTimeoutMS != 500 || !ep.Idempotent || ep.BatchMax != 1000 {
		t.Fatalf("vendor %+v", v)
	}
	b := v.Defaults.Breaker
	if v.Defaults.RateLimit.PerAccountRPS != 50 || b.WindowS != 30 || b.MinCalls != 20 || b.FailureRatio != 0.5 ||
		b.ConsecutiveTimeouts != 5 || b.OpenS != 30 || b.OpenMaxS != 600 || b.HalfOpenProbes != 3 || v.Defaults.HealthProbe.EveryS != 30 {
		t.Fatalf("defaults %+v", v.Defaults)
	}
	var vb Vendor
	roundTrip(t, v, &vb)
	if !reflect.DeepEqual(vb.Endpoints["upload@1"], ep) || vb.Defaults.Breaker != b {
		t.Fatalf("vendor round trip: %+v", vb)
	}

	var a Account
	decodeFile(t, "testdata/design/8.3-account.yaml", &a)
	if a.Account != "ct-brandA-in" || a.State != AccountActive || a.CredentialsRef != "kms://hub/ct-brandA-in" ||
		a.Config["brand_code"] != "A" || a.Quota != (Quota{RPS: 50, Daily: 5000000}) {
		t.Fatalf("account %+v", a)
	}
	var ab Account
	roundTrip(t, a, &ab)
	if ab.Quota != a.Quota || ab.Config["project_id"] != a.Config["project_id"] {
		t.Fatalf("account round trip: %+v", ab)
	}
}

func TestDuration(t *testing.T) {
	tests := map[string]time.Duration{"30d": 30 * 24 * time.Hour, "6h": 6 * time.Hour, "500ms": 500 * time.Millisecond, "1h30m": 90 * time.Minute}
	for in, want := range tests {
		var d Duration
		if err := yaml.Unmarshal([]byte(in), &d); err != nil || d.Std() != want {
			t.Errorf("%q: %v %v", in, d.Std(), err)
		}
	}
	for _, bad := range []string{"30x", "-1d", "d"} {
		var d Duration
		if err := yaml.Unmarshal([]byte(bad), &d); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	out, _ := yaml.Marshal(Duration(30 * 24 * time.Hour))
	if string(out) != "30d\n" {
		t.Errorf("marshal 30d = %q", out)
	}
}

func TestParseTransformerRef(t *testing.T) {
	if r, err := ParseTransformerRef("T2@3"); err != nil || r != (TransformerRef{"T2", 3}) {
		t.Fatalf("%v %v", r, err)
	}
	for _, bad := range []string{"T2", "T2@", "@3", "T2@0", "T2@x"} {
		if _, err := ParseTransformerRef(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
