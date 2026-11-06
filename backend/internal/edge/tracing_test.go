package edge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

const callerTrace = "4bf92f3577b34da6a3ce929d0e0e4736"

// tracedEdge is the edge behind telemetry.HTTPHandler, as the gateway role serves it.
func tracedEdge(t *testing.T, up *fakeUpstream) (http.Handler, *tracetest.SpanRecorder) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp, err := telemetry.NewTracerProvider(t.Context(), telemetry.TraceConfig{Role: "gateway", SampleRatio: 0, SpanProcessor: rec})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })
	h, _ := invokeServer(t, devSnapshot(t), up)
	return telemetry.HTTPHandler(h, tp, "gateway"), rec
}

func tracedPost(t *testing.T, h http.Handler, key, path, body string, sampled bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	flags := "00"
	if sampled {
		flags = "01"
	}
	req.Header.Set("traceparent", "00-"+callerTrace+"-00f067aa0ba902b7-"+flags)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func spanNamed(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range spans {
		if s.Name() == name {
			return s
		}
	}
	names := make([]string, 0, len(spans))
	for _, s := range spans {
		names = append(names, s.Name())
	}
	t.Fatalf("no span %q in %v", name, names)
	return nil
}

func attrs(s sdktrace.ReadOnlySpan) map[attribute.Key]string {
	m := map[attribute.Key]string{}
	for _, kv := range s.Attributes() {
		m[kv.Key] = kv.Value.String()
	}
	return m
}

// TestSyncSpans is HUB-231 for the sync path: the caller's trace continues into the edge server
// span (named after the route, tagged with ids), the transform stages are its children, and no
// attribute is outside the allowlist or carries a credential, the API key or a body fragment.
func TestSyncSpans(t *testing.T) {
	h, rec := tracedEdge(t, &fakeUpstream{steps: []step{ok200()}})
	res := tracedPost(t, h, devKey, "/v1/integrations/int_evt_brandA/invoke", trackBody, true)
	if res.Code != 200 {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
	spans := rec.Ended()
	edge := spanNamed(t, spans, "POST /v1/integrations/{integration_id}/invoke")
	if edge.SpanContext().TraceID().String() != callerTrace || edge.SpanKind() != trace.SpanKindServer {
		t.Fatalf("edge span trace %s kind %v", edge.SpanContext().TraceID(), edge.SpanKind())
	}
	a := attrs(edge)
	for k, want := range map[attribute.Key]string{
		"hub.request_id": res.Header().Get("X-Hub-Request-Id"), "hub.tenant": "orders-service", "hub.integration_id": "int_evt_brandA",
		"hub.mode": "wait", "hub.integration_version": "2", "http.route": "POST /v1/integrations/{integration_id}/invoke",
	} {
		if a[k] != want {
			t.Errorf("edge %s = %q, want %q", k, a[k], want)
		}
	}
	for _, name := range []string{"hub.transform.request", "hub.transform.response"} {
		s := spanNamed(t, spans, name)
		if s.Parent().SpanID() != edge.SpanContext().SpanID() {
			t.Errorf("%s is not a child of the edge span", name)
		}
		if ta := attrs(s); ta["hub.transformer"] == "" || ta["hub.transformer_version"] == "" {
			t.Errorf("%s attributes %v", name, ta)
		}
	}
	if v := telemetry.SpanAttributeViolations(spans, forbiddenInSpans()...); len(v) > 0 {
		t.Fatalf("span attribute violations:\n%s", strings.Join(v, "\n"))
	}
}

// TestUnsampledRequestHasNoStageSpans: an unsampled caller trace records nothing (stage spans
// are created only when the trace is sampled).
func TestUnsampledRequestHasNoStageSpans(t *testing.T) {
	h, rec := tracedEdge(t, &fakeUpstream{steps: []step{ok200()}})
	if res := tracedPost(t, h, devKey, "/v1/integrations/int_evt_brandA/invoke", trackBody, false); res.Code != 200 {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
	if n := len(rec.Ended()); n != 0 {
		t.Fatalf("unsampled request recorded %d spans", n)
	}
}

// TestFailedRequestTagsCode: the canonical code is on the edge span (here a refused key).
func TestFailedRequestTagsCode(t *testing.T) {
	h, rec := tracedEdge(t, &fakeUpstream{steps: []step{ok200()}})
	if res := tracedPost(t, h, "hk_wrong_key_value", "/v1/integrations/int_evt_brandA/invoke", trackBody, true); res.Code != 401 {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
	edge := spanNamed(t, rec.Ended(), "POST /v1/integrations/{integration_id}/invoke")
	if got := attrs(edge)["hub.code"]; got != "UNAUTHORIZED" {
		t.Fatalf("hub.code = %q", got)
	}
	if v := telemetry.SpanAttributeViolations(rec.Ended(), "hk_wrong_key_value"); len(v) > 0 {
		t.Fatalf("span attribute violations:\n%s", strings.Join(v, "\n"))
	}
}

// forbiddenInSpans are values that must never reach a span: the tenant API key, the vendor
// credentials, the callback signing secret and fragments of the canonical and vendor bodies.
func forbiddenInSpans() []string {
	return []string{devKey, HashKey(devKey), fakeCreds["account_id"], fakeCreds["passcode"], "fake-signing-secret-0123456789",
		"u_48213", "ord_8821", "order_placed", "unprocessed"}
}

// TestSpanHelpersDoNotAllocateUnsampled guards the hot-path budget (DESIGN §18.5): with no span
// or an unsampled one, tagging the edge span costs no allocation.
func TestSpanHelpersDoNotAllocateUnsampled(t *testing.T) {
	unsampled := trace.ContextWithRemoteSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}}))
	p := &prepared{integration: devSnapshot(t).Integrations["int_evt_brandA"]}
	for name, ctx := range map[string]context.Context{"no span": t.Context(), "unsampled": unsampled} {
		r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/integrations/int_evt_brandA/invoke", nil)
		r.Pattern = "POST /v1/integrations/{integration_id}/invoke"
		if n := testing.AllocsPerRun(100, func() {
			tagRoute(r)
			tagTenant(ctx, "orders-service")
			tagIntegration(ctx, p)
			tagCode(ctx, "UNAUTHORIZED")
		}); n != 0 {
			t.Errorf("%s: %v allocs per request", name, n)
		}
	}
}
