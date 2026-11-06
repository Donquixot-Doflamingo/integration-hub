//go:build integration

package edge

import (
	"net/http"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
)

func linkedTo(s sdktrace.ReadOnlySpan, target trace.SpanContext) bool {
	for _, l := range s.Links() {
		if l.SpanContext.TraceID() == target.TraceID() && l.SpanContext.SpanID() == target.SpanID() {
			return true
		}
	}
	return false
}

// TestAsyncTraceLinksCallerToCallback is HUB-231 acceptance 1 and 2: for an async request, the
// caller's trace continues into the gateway's edge span and its hub.queue.enqueue span; the
// envelope carries that context across the outbox; the worker's attempt is a new root linked to
// it (sampled because the accepting trace was, though the root ratio is 0); the upstream span is
// the attempt's child and the vendor receives its traceparent; the transform stages are the
// attempt's children; the callback delivery links to the attempt. No span attribute is outside
// the allowlist or carries a credential, the API key or a body fragment.
func TestAsyncTraceLinksCallerToCallback(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp, err := telemetry.NewTracerProvider(t.Context(), telemetry.TraceConfig{Role: "itest", SampleRatio: 0, SpanProcessor: rec})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })
	q, rc, _ := tracedCallbackRig(t, nil, tp)
	q.h = telemetry.HTTPHandler(q.h, tp, "gateway")
	q.route(t, mockvendor.Behaviour{Status: 200, Body: okVendor})

	const callerSpan = "00f067aa0ba902b7"
	res, body := q.invoke(t, "int_evt_brandB", trackBody, "traceparent", "00-"+callerTrace+"-"+callerSpan+"-01")
	if res.Code != 202 {
		t.Fatalf("%d %v", res.Code, body)
	}
	id := body["request_id"].(string)

	// caller → gateway: the edge span continues the caller's trace; the enqueue span is its child.
	edge := spanNamed(t, rec.Ended(), "POST /v1/integrations/{integration_id}/invoke")
	if edge.SpanContext().TraceID().String() != callerTrace || edge.Parent().SpanID().String() != callerSpan {
		t.Fatalf("edge span trace %s parent %s", edge.SpanContext().TraceID(), edge.Parent().SpanID())
	}
	if a := attrs(edge); a["hub.request_id"] != id || a["hub.mode"] != "async_callback" {
		t.Fatalf("edge attributes %v", a)
	}
	enqueue := spanNamed(t, rec.Ended(), "hub.queue.enqueue")
	if enqueue.Parent().SpanID() != edge.SpanContext().SpanID() || enqueue.SpanKind() != trace.SpanKindProducer {
		t.Fatalf("enqueue span parent %s kind %v", enqueue.Parent().SpanID(), enqueue.SpanKind())
	}
	// The envelope carries the enqueue span's context across the outbox; the ledger row the trace id.
	var stored, ledgerTrace string
	if err := q.db.QueryRow(t.Context(), `SELECT envelope->'trace'->>'traceparent' FROM hub.outbox WHERE request_id = $1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if want := "00-" + callerTrace + "-" + enqueue.SpanContext().SpanID().String() + "-01"; stored != want {
		t.Fatalf("envelope traceparent %q, want %q", stored, want)
	}
	if err := q.db.QueryRow(t.Context(), `SELECT trace_id FROM hub.requests WHERE request_id = $1`, id).Scan(&ledgerTrace); err != nil || ledgerTrace != callerTrace {
		t.Fatalf("requests.trace_id %q %v", ledgerTrace, err)
	}

	// gateway → worker: a new root per attempt, linked to the enqueue span, sampled through it.
	if n, err := q.worker.ProcessOnce(t.Context()); err != nil || n != 1 {
		t.Fatalf("worker %d %v", n, err)
	}
	attempt := spanNamed(t, rec.Ended(), "hub.worker.attempt")
	if attempt.Parent().IsValid() || attempt.SpanContext().TraceID().String() == callerTrace || !linkedTo(attempt, enqueue.SpanContext()) {
		t.Fatalf("attempt span parent %v trace %s links %v", attempt.Parent(), attempt.SpanContext().TraceID(), attempt.Links())
	}
	if a := attrs(attempt); a["hub.request_id"] != id || a["hub.attempt"] != "1" || a["hub.outcome"] != "succeeded" || attempt.SpanKind() != trace.SpanKindConsumer {
		t.Fatalf("attempt attributes %v kind %v", a, attempt.SpanKind())
	}

	// worker → vendor: the upstream span is the attempt's child and the vendor got its traceparent.
	var up sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if strings.HasPrefix(s.Name(), "upstream ") && s.Parent().SpanID() == attempt.SpanContext().SpanID() {
			up = s
		}
	}
	if up == nil || up.SpanKind() != trace.SpanKindClient {
		t.Fatal("no upstream client span under the worker attempt")
	}
	reqs := q.mv.Requests(t, nil)
	if len(reqs) != 1 {
		t.Fatalf("vendor saw %d requests", len(reqs))
	}
	want := "00-" + attempt.SpanContext().TraceID().String() + "-" + up.SpanContext().SpanID().String() + "-01"
	if got := http.Header(reqs[0].Headers).Get("Traceparent"); got != want {
		t.Fatalf("vendor traceparent %q, want %q", got, want)
	}
	for _, name := range []string{"hub.transform.request", "hub.transform.response"} {
		found := false
		for _, s := range rec.Ended() {
			found = found || (s.Name() == name && s.Parent().SpanID() == attempt.SpanContext().SpanID())
		}
		if !found {
			t.Errorf("no %s span under the worker attempt", name)
		}
	}

	// worker → callback: the delivery span links to the attempt that enqueued it.
	eventually(t, "the callback span", func() bool {
		for _, s := range rec.Ended() {
			if s.Name() == "hub.callback.deliver" {
				return true
			}
		}
		return false
	})
	cb := spanNamed(t, rec.Ended(), "hub.callback.deliver")
	if !linkedTo(cb, attempt.SpanContext()) || cb.Parent().IsValid() {
		t.Fatalf("callback span links %v parent %v", cb.Links(), cb.Parent())
	}
	if a := attrs(cb); a["hub.request_id"] != id || a["hub.outcome"] != "delivered" {
		t.Fatalf("callback attributes %v", a)
	}
	if ev, _, _ := rc.get(id); ev == nil {
		t.Fatal("receiver got no callback")
	}

	if v := telemetry.SpanAttributeViolations(rec.Ended(), forbiddenInSpans()...); len(v) > 0 {
		t.Fatalf("span attribute violations:\n%s", strings.Join(v, "\n"))
	}
}
