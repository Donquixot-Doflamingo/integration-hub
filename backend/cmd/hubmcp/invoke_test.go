package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var trackBody = map[string]any{"user": map[string]any{"id": "u_48213"}, "event": map[string]any{"name": "order_placed"}}

// invokeTestApp connects to a fresh app whose retry sleep is instant and recorded.
func invokeTestApp(t *testing.T, hub *fakeHub) (*mcp.ClientSession, *app, *[]time.Duration) {
	t.Helper()
	a := testApp(t, hub.srv.URL, false, nil)
	var mu sync.Mutex
	slept := &[]time.Duration{}
	a.sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		*slept = append(*slept, d)
		return nil
	}
	return connect(t, a), a, slept
}

func withHeader(r reply, kv map[string]string) reply {
	if r.header == nil {
		r.header = map[string]string{}
	}
	for k, v := range kv {
		r.header[k] = v
	}
	return r
}

func TestInvoke(t *testing.T) {
	type want struct {
		outcome    string
		httpStatus any // float64 or nil
		code       string
		invokes    int
		retryWaits int
	}
	tests := []struct {
		name    string
		args    map[string]any
		replies func(h *fakeHub)
		want    want
		check   func(t *testing.T, h *fakeHub, out toolOutcome)
	}{
		{
			name: "completed with all headers",
			args: map[string]any{"integration_id": "int_evt_brandA", "body": trackBody, "idempotency_key": "order-8821-created",
				"prefer_async": true, "deadline_ms": 1500, "session": "ses_01J9Z3K8X2V7Q4M6N8P0R2S4T6"},
			replies: func(h *fakeHub) {
				h.on("invokeIntegration", withHeader(h.pub("invokeIntegration", 200, "events.track@v1"), leakHeaders))
			},
			want: want{outcome: "completed", httpStatus: 200.0, invokes: 1},
			check: func(t *testing.T, h *fakeHub, out toolOutcome) {
				c := h.callsTo("invokeIntegration")[0]
				for k, v := range map[string]string{"Idempotency-Key": "order-8821-created", "Prefer": "respond-async", "X-Request-Deadline": "1500", "X-Hub-Session": "ses_01J9Z3K8X2V7Q4M6N8P0R2S4T6"} {
					if got := c.Header.Get(k); got != v {
						t.Errorf("header %s = %q, want %q", k, got, v)
					}
				}
				if out.field(t, "result", "status") != "ok" || out.field(t, "idempotent_replay") != false || out.field(t, "accepted") != nil {
					t.Errorf("result: %s", out.json)
				}
			},
		},
		{
			name: "idempotent replay and session returned",
			args: map[string]any{"integration_id": "int_evt_brandA", "body": trackBody, "idempotency_key": "k1"},
			replies: func(h *fakeHub) {
				h.on("invokeIntegration", withHeader(h.pub("invokeIntegration", 200, "events.track@v1"),
					map[string]string{"X-Hub-Idempotent-Replay": "true", "X-Hub-Session": "ses_01J9Z3K8X2V7Q4M6N8P0R2S4T6"}))
			},
			want: want{outcome: "completed", httpStatus: 200.0, invokes: 1},
			check: func(t *testing.T, _ *fakeHub, out toolOutcome) {
				if out.field(t, "idempotent_replay") != true || out.field(t, "session") != "ses_01J9Z3K8X2V7Q4M6N8P0R2S4T6" {
					t.Errorf("replay/session: %s", out.json)
				}
			},
		},
		{
			name:    "202 queued is not success",
			args:    map[string]any{"integration_id": "int_evt_brandA", "body": trackBody},
			replies: func(h *fakeHub) { h.on("invokeIntegration", h.pub("invokeIntegration", 202, "queued")) },
			want:    want{outcome: "queued", httpStatus: 202.0, invokes: 1},
			check: func(t *testing.T, _ *fakeHub, out toolOutcome) {
				if out.res.IsError || out.field(t, "result") != nil || out.field(t, "accepted", "status") != "queued" ||
					out.field(t, "accepted", "poll") != "/v1/requests/"+fakeRequestID || out.field(t, "request_id") != fakeRequestID {
					t.Errorf("queued result: %s", out.json)
				}
				if text := out.res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "NOT done") || !strings.Contains(text, "hub_get_request") {
					t.Errorf("text does not say pending: %s", text)
				}
			},
		},
		{
			name:    "202 in flight",
			args:    map[string]any{"integration_id": "int_evt_brandA", "body": trackBody},
			replies: func(h *fakeHub) { h.on("invokeIntegration", h.pub("invokeIntegration", 202, "in_flight")) },
			want:    want{outcome: "queued", httpStatus: 202.0, invokes: 1},
		},
		{
			name: "named operation uses invokeIntegrationOperation",
			args: map[string]any{"integration_id": "int_evt_brandA", "operation": "send", "body": trackBody},
			replies: func(h *fakeHub) {
				h.on("invokeIntegrationOperation", h.pub("invokeIntegrationOperation", 200, "events.track@v1"))
			},
			want: want{outcome: "completed", httpStatus: 200.0, invokes: 1},
			check: func(t *testing.T, h *fakeHub, _ toolOutcome) {
				if c := h.callsTo("invokeIntegrationOperation"); len(c) != 1 || !strings.HasSuffix(c[0].Path, "/invoke/send") {
					t.Errorf("calls %+v", c)
				}
			},
		},
		{
			name: "retryable error without key: no retry, envelope surfaced",
			args: map[string]any{"integration_id": "int_evt_brandA", "body": trackBody},
			replies: func(h *fakeHub) {
				h.on("invokeIntegration", withHeader(h.pub("invokeIntegration", 503, "UPSTREAM_UNAVAILABLE"), map[string]string{"Retry-After": "30"}))
			},
			want: want{outcome: "error", httpStatus: 503.0, code: "UPSTREAM_UNAVAILABLE", invokes: 1},
			check: func(t *testing.T, _ *fakeHub, out toolOutcome) {
				if out.field(t, "error", "retryable") != true || out.field(t, "error", "retry_after_ms") != 30000.0 ||
					out.field(t, "error", "circuit") != "open" || out.field(t, "error", "reason") != "circuit_open" {
					t.Errorf("envelope: %s", out.json)
				}
			},
		},
		{
			name: "retryable error with key and short wait: retried once",
			args: map[string]any{"integration_id": "int_evt_brandA", "body": trackBody, "idempotency_key": "k2"},
			replies: func(h *fakeHub) {
				h.on("invokeIntegration", h.pub("invokeIntegration", 504, ""), h.pub("invokeIntegration", 200, "events.track@v1"))
			},
			want: want{outcome: "completed", httpStatus: 200.0, invokes: 2, retryWaits: 1},
			check: func(t *testing.T, h *fakeHub, _ toolOutcome) {
				c := h.callsTo("invokeIntegration")
				if c[0].Header.Get("Idempotency-Key") != "k2" || c[1].Header.Get("Idempotency-Key") != "k2" {
					t.Error("retry did not reuse the idempotency key")
				}
			},
		},
		{
			name: "retryable error twice: at most one retry",
			args: map[string]any{"integration_id": "int_evt_brandA", "body": trackBody, "idempotency_key": "k3"},
			replies: func(h *fakeHub) {
				h.on("invokeIntegration", withHeader(h.pub("invokeIntegration", 429, ""), map[string]string{"Retry-After": "2"}))
			},
			want: want{outcome: "error", httpStatus: 429.0, code: "UPSTREAM_RATE_LIMITED", invokes: 2, retryWaits: 1},
		},
		{
			name: "retryable error with a long wait: returned, not retried",
			args: map[string]any{"integration_id": "int_evt_brandA", "body": trackBody, "idempotency_key": "k4"},
			replies: func(h *fakeHub) {
				h.on("invokeIntegration", withHeader(h.pub("invokeIntegration", 503, "UPSTREAM_UNAVAILABLE"), map[string]string{"Retry-After": "30"}))
			},
			want: want{outcome: "error", httpStatus: 503.0, code: "UPSTREAM_UNAVAILABLE", invokes: 1},
		},
		{
			name: "non-retryable error with key: never retried",
			args: map[string]any{"integration_id": "int_evt_brandA", "body": trackBody, "idempotency_key": "k5"},
			replies: func(h *fakeHub) {
				h.on("invokeIntegration", withHeader(h.pub("invokeIntegration", 422, ""), leakHeaders))
			},
			want: want{outcome: "error", httpStatus: 422.0, code: "UPSTREAM_REJECTED", invokes: 1},
			check: func(t *testing.T, _ *fakeHub, out toolOutcome) {
				if out.field(t, "error", "upstream", "status") != 400.0 || out.field(t, "error", "upstream", "code") != "E_INVALID_EVENT" {
					t.Errorf("upstream detail: %s", out.json)
				}
			},
		},
		{
			name: "validation details surfaced",
			args: map[string]any{"integration_id": "int_evt_brandA", "body": map[string]any{"user": map[string]any{}}},
			replies: func(h *fakeHub) {
				h.on("invokeIntegration", h.pub("invokeIntegration", 400, "VALIDATION_FAILED"))
			},
			want: want{outcome: "error", httpStatus: 400.0, code: "VALIDATION_FAILED", invokes: 1},
			check: func(t *testing.T, _ *fakeHub, out toolOutcome) {
				if d, _ := out.field(t, "error", "details").([]any); len(d) == 0 {
					t.Errorf("no details: %s", out.json)
				}
			},
		},
		{
			name: "paused integration is not invoked",
			args: map[string]any{"integration_id": "int_evt_brandA", "body": trackBody},
			replies: func(h *fakeHub) {
				h.on("getIntegration", h.pub("getIntegration", 200, "", func(m map[string]any) { m["paused"] = true }))
			},
			want: want{outcome: "error", httpStatus: nil, code: "INTEGRATION_PAUSED", invokes: 0},
			check: func(t *testing.T, _ *fakeHub, out toolOutcome) {
				if out.field(t, "error", "retryable") != true || out.field(t, "request_id") != nil {
					t.Errorf("paused: %s", out.json)
				}
			},
		},
		{
			name:    "unknown integration stops at the pre-check",
			args:    map[string]any{"integration_id": "int_evt_brandZ", "body": trackBody},
			replies: func(h *fakeHub) { h.on("getIntegration", h.pub("getIntegration", 404, "")) },
			want:    want{outcome: "error", httpStatus: 404.0, code: "INTEGRATION_NOT_FOUND", invokes: 0},
		},
		{
			name: "missing body is refused locally",
			args: map[string]any{"integration_id": "int_evt_brandA"},
			want: want{outcome: "error", code: "VALIDATION_FAILED"},
		},
		{
			name: "idempotency key with a space is refused locally",
			args: map[string]any{"integration_id": "int_evt_brandA", "body": trackBody, "idempotency_key": "order 8821"},
			want: want{outcome: "error", code: "VALIDATION_FAILED"},
			check: func(t *testing.T, h *fakeHub, out toolOutcome) {
				if msg, _ := out.field(t, "error", "message").(string); !strings.Contains(msg, "printable ASCII") || len(h.callsTo("")) != 0 {
					t.Errorf("got %s with %d Hub calls", out.json, len(h.callsTo("")))
				}
			},
		},
		{
			name: "vendor-selecting argument is refused locally",
			args: map[string]any{"integration_id": "int_evt_brandA", "body": trackBody, "vendor": "clevertap"},
			want: want{outcome: "error", code: "VALIDATION_FAILED"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := newFakeHub(t)
			hub.on("getIntegration", hub.pub("getIntegration", 200, ""))
			if tt.replies != nil {
				// Replies registered by the case take precedence over the default getIntegration.
				hub.mu.Lock()
				delete(hub.replies, "getIntegration")
				hub.mu.Unlock()
				tt.replies(hub)
				if len(hub.replies["getIntegration"]) == 0 {
					hub.on("getIntegration", hub.pub("getIntegration", 200, ""))
				}
			}
			cs, _, slept := invokeTestApp(t, hub)
			out := callTool(t, cs, toolInvoke, tt.args)
			if got := out.field(t, "outcome"); got != tt.want.outcome {
				t.Fatalf("outcome %v, want %s: %s", got, tt.want.outcome, out.json)
			}
			if out.res.IsError != (tt.want.outcome == "error") {
				t.Errorf("isError %v for outcome %s", out.res.IsError, tt.want.outcome)
			}
			if tt.want.code != "" && out.field(t, "error", "code") != tt.want.code {
				t.Errorf("code %v, want %s", out.field(t, "error", "code"), tt.want.code)
			}
			if got := out.field(t, "http_status"); got != tt.want.httpStatus {
				t.Errorf("http_status %v, want %v", got, tt.want.httpStatus)
			}
			invokes := len(hub.callsTo("invokeIntegration")) + len(hub.callsTo("invokeIntegrationOperation"))
			if invokes != tt.want.invokes {
				t.Errorf("%d invoke calls, want %d", invokes, tt.want.invokes)
			}
			if len(*slept) != tt.want.retryWaits {
				t.Errorf("%d retry waits, want %d", len(*slept), tt.want.retryWaits)
			}
			if tt.check != nil {
				tt.check(t, hub, out)
			}
		})
	}
}

// TestInvokePreCheckIsCachedAtMost60s: getIntegration is read once per integration per minute.
func TestInvokePreCheckIsCachedAtMost60s(t *testing.T) {
	hub := newFakeHub(t)
	hub.on("getIntegration", hub.pub("getIntegration", 200, ""))
	hub.on("invokeIntegration", hub.pub("invokeIntegration", 200, "events.track@v1"))
	a := testApp(t, hub.srv.URL, false, nil)
	now := time.Unix(1_800_000_000, 0)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	a.integrations = newIntegrationCache(clock)
	cs := connect(t, a)
	args := map[string]any{"integration_id": "int_evt_brandA", "body": trackBody}
	callTool(t, cs, toolInvoke, args)
	callTool(t, cs, toolInvoke, args)
	if n := len(hub.callsTo("getIntegration")); n != 1 {
		t.Fatalf("%d pre-checks within 60 s, want 1", n)
	}
	mu.Lock()
	now = now.Add(integrationCacheTTL)
	mu.Unlock()
	callTool(t, cs, toolInvoke, args)
	if n := len(hub.callsTo("getIntegration")); n != 2 {
		t.Fatalf("%d pre-checks after 60 s, want 2", n)
	}
}

func TestGetRequest(t *testing.T) {
	tests := []struct {
		name      string
		reply     func(h *fakeHub) reply
		wantState string
		wantCode  string
		check     func(t *testing.T, out toolOutcome)
	}{
		{
			name:      "succeeded carries the result",
			reply:     func(h *fakeHub) reply { return h.pub("getRequestStatus", 200, "succeeded") },
			wantState: "succeeded",
			check: func(t *testing.T, out toolOutcome) {
				if out.field(t, "result", "status") != "ok" || out.field(t, "completed_at") != "2026-10-31T09:15:02Z" || out.field(t, "error") != nil {
					t.Errorf("%s", out.json)
				}
			},
		},
		{
			name:      "parked is pending",
			reply:     func(h *fakeHub) reply { return h.pub("getRequestStatus", 200, "parked") },
			wantState: "parked",
			check: func(t *testing.T, out toolOutcome) {
				if out.field(t, "result") != nil || out.field(t, "completed_at") != nil {
					t.Errorf("%s", out.json)
				}
			},
		},
		{
			name: "dead lettered carries the error",
			reply: func(h *fakeHub) reply {
				return h.pub("getRequestStatus", 200, "parked", func(m map[string]any) {
					m["status"] = "dead_lettered"
					m["completed_at"] = "2026-10-31T10:00:00Z"
					m["error"] = map[string]any{"code": "UPSTREAM_UNAVAILABLE", "message": "attempts exhausted", "retryable": false,
						"retry_after_ms": nil, "reason": "attempts_exhausted", "request_id": "req_01J9Z3M1A5B7C9D1E3F5G7H9JM"}
				})
			},
			wantState: "dead_lettered",
			check: func(t *testing.T, out toolOutcome) {
				if out.field(t, "error", "code") != "UPSTREAM_UNAVAILABLE" || out.field(t, "error", "reason") != "attempts_exhausted" {
					t.Errorf("%s", out.json)
				}
			},
		},
		{
			name:     "unknown or other tenant's request",
			reply:    func(h *fakeHub) reply { return h.pub("getRequestStatus", 404, "") },
			wantCode: "REQUEST_NOT_FOUND",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := newFakeHub(t)
			r := tt.reply(hub)
			r.header = leakHeaders
			hub.on("getRequestStatus", r)
			cs := connect(t, testApp(t, hub.srv.URL, false, nil))
			out := callTool(t, cs, toolGetRequest, map[string]any{"request_id": "req_01J9Z3M1A5B7C9D1E3F5G7H9JM"})
			if tt.wantCode != "" {
				if !out.res.IsError || out.field(t, "error", "code") != tt.wantCode {
					t.Fatalf("got %s", out.json)
				}
				return
			}
			if out.res.IsError || out.field(t, "status") != tt.wantState {
				t.Fatalf("got %s", out.json)
			}
			if tt.check != nil {
				tt.check(t, out)
			}
		})
	}
	t.Run("malformed id is refused locally", func(t *testing.T) {
		hub := newFakeHub(t)
		cs := connect(t, testApp(t, hub.srv.URL, false, nil))
		out := callTool(t, cs, toolGetRequest, map[string]any{"request_id": "req_../../admin"})
		if out.field(t, "error", "code") != "VALIDATION_FAILED" || len(hub.callsTo("")) != 0 {
			t.Fatalf("got %s", out.json)
		}
	})
}

func TestDryRun(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]any
		reply    func(h *fakeHub) reply
		wantCode string
		check    func(t *testing.T, h *fakeHub, out toolOutcome)
	}{
		{
			name:  "upstream request shown with secrets redacted",
			args:  map[string]any{"integration_id": "int_evt_brandA", "body": trackBody},
			reply: func(h *fakeHub) reply { return h.pub("dryRunIntegration", 200, "") },
			check: func(t *testing.T, h *fakeHub, out toolOutcome) {
				if out.field(t, "upstream_request", "headers", "X-CleverTap-Passcode") != "***" || out.field(t, "upstream_request", "method") != "POST" {
					t.Errorf("%s", out.json)
				}
				if w, _ := out.field(t, "warnings").([]any); len(w) != 0 {
					t.Errorf("unexpected warnings: %s", out.json)
				}
				if c := h.callsTo("dryRunIntegration")[0]; c.Query != "" {
					t.Errorf("default operation sent as query %q", c.Query)
				}
			},
		},
		{
			name: "a redacted header that slipped through is forced to ***",
			args: map[string]any{"integration_id": "int_evt_brandA", "operation": "send", "body": trackBody, "session": "ses_01J9Z3K8X2V7Q4M6N8P0R2S4T6"},
			reply: func(h *fakeHub) reply {
				return h.pub("dryRunIntegration", 200, "", func(m map[string]any) {
					m["upstream_request"].(map[string]any)["headers"].(map[string]any)["X-CleverTap-Passcode"] = "LEAKED-PASSCODE-123"
				})
			},
			check: func(t *testing.T, h *fakeHub, out toolOutcome) {
				if strings.Contains(string(out.json), "LEAKED-PASSCODE-123") || out.field(t, "upstream_request", "headers", "X-CleverTap-Passcode") != "***" {
					t.Errorf("leaked: %s", out.json)
				}
				if w, _ := out.field(t, "warnings").([]any); len(w) != 1 {
					t.Errorf("want one warning: %s", out.json)
				}
				c := h.callsTo("dryRunIntegration")[0]
				if c.Query != "operation=send" || c.Header.Get("X-Hub-Session") != "ses_01J9Z3K8X2V7Q4M6N8P0R2S4T6" {
					t.Errorf("query %q session %q", c.Query, c.Header.Get("X-Hub-Session"))
				}
			},
		},
		{
			name:     "key without dry_run scope",
			args:     map[string]any{"integration_id": "int_evt_brandA", "body": trackBody},
			reply:    func(h *fakeHub) reply { return h.pub("dryRunIntegration", 403, "") },
			wantCode: "FORBIDDEN",
		},
		{
			name:     "validation errors as structured content",
			args:     map[string]any{"integration_id": "int_evt_brandA", "body": map[string]any{}},
			reply:    func(h *fakeHub) reply { return h.pub("dryRunIntegration", 400, "VALIDATION_FAILED") },
			wantCode: "VALIDATION_FAILED",
			check: func(t *testing.T, _ *fakeHub, out toolOutcome) {
				if d, _ := out.field(t, "error", "details").([]any); len(d) == 0 {
					t.Errorf("no details: %s", out.json)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := newFakeHub(t)
			hub.on("dryRunIntegration", tt.reply(hub))
			cs := connect(t, testApp(t, hub.srv.URL, false, nil))
			out := callTool(t, cs, toolDryRun, tt.args)
			if tt.wantCode != "" && (!out.res.IsError || out.field(t, "error", "code") != tt.wantCode) {
				t.Fatalf("want %s, got %s", tt.wantCode, out.json)
			}
			if tt.wantCode == "" && out.res.IsError {
				t.Fatalf("unexpected error %s", out.json)
			}
			if tt.check != nil {
				tt.check(t, hub, out)
			}
		})
	}
}
