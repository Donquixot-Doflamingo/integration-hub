package telemetry

import (
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// AllowedSpanAttributes is every span attribute key the Hub may emit (docs/tech/OBSERVABILITY.md
// §5.1): the hub.* ids and counters it sets itself, plus the OTel HTTP semantic-convention keys
// otelhttp sets on the server span. Values are ids, enums, counts and sizes, never a body, a
// header value carrying a credential, or a credential. A test fails on any key outside this set
// (SpanAttributeViolations), so a new attribute is a reviewed change to this list.
var AllowedSpanAttributes = map[attribute.Key]bool{
	// edge server span (hub.* set by the edge, the rest by otelhttp)
	"hub.request_id": true, "hub.tenant": true, "hub.integration_id": true, "hub.integration_version": true,
	"hub.mode": true, "hub.code": true,
	"http.route": true, "http.request.method": true, "http.request.method_original": true, "http.response.status_code": true,
	"url.scheme": true, "url.path": true, "server.address": true, "server.port": true,
	"network.protocol.version": true, "network.peer.address": true, "network.peer.port": true, "client.address": true,
	"user_agent.original": true, "http.request.body.size": true, "http.response.body.size": true,
	// transform stage spans
	"hub.transformer": true, "hub.transformer_version": true,
	// upstream client span
	"hub.vendor": true, "hub.account": true, "hub.endpoint": true, "hub.transport_error": true,
	// outbox producer and worker attempt
	"hub.pool": true, "hub.attempt": true, "hub.queue_wait_ms": true, "hub.outcome": true,
	// callback delivery
	"hub.endpoint_id": true, "hub.callback.attempt": true, "hub.callback.event": true,
}

// SpanAttributeViolations checks recorded spans against AllowedSpanAttributes and returns one
// line per problem: a key outside the allowlist, or a value (attribute of the span, its events
// or links, or the status description) that contains any of forbidden (credentials, body
// fragments). Empty means the spans are clean.
func SpanAttributeViolations(spans []sdktrace.ReadOnlySpan, forbidden ...string) []string {
	var out []string
	check := func(span, where string, kvs []attribute.KeyValue) {
		for _, kv := range kvs {
			if !AllowedSpanAttributes[kv.Key] {
				out = append(out, fmt.Sprintf("%s: %s key %q is not allowlisted", span, where, kv.Key))
			}
			if f := contains(kv.Value.String(), forbidden); f != "" {
				out = append(out, fmt.Sprintf("%s: %s %q carries forbidden value %q", span, where, kv.Key, f))
			}
		}
	}
	for _, s := range spans {
		check(s.Name(), "attribute", s.Attributes())
		for _, e := range s.Events() {
			check(s.Name(), "event "+e.Name, e.Attributes)
		}
		for _, l := range s.Links() {
			check(s.Name(), "link", l.Attributes)
		}
		if f := contains(s.Status().Description+" "+s.Name(), forbidden); f != "" {
			out = append(out, fmt.Sprintf("%s: name or status carries forbidden value %q", s.Name(), f))
		}
	}
	return out
}

func contains(v string, forbidden []string) string {
	for _, f := range forbidden {
		if f != "" && strings.Contains(v, f) {
			return f
		}
	}
	return ""
}
