package hubclient_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient"
)

func TestGetRequest(t *testing.T) {
	t.Parallel()
	failed := []byte(`{"request_id":"req_01J9Z3M1A5B7C9D1E3F5G7H9JK","integration_id":"int_evt_brandA",
		"operation":"default","mode":"async","status":"dead_lettered","idempotency_key":"order-8822-created","attempts":1,
		"created_at":"2026-10-31T11:00:00Z","updated_at":"2026-10-31T11:02:40Z","completed_at":"2026-10-31T11:02:40Z",
		"next_attempt_at":null,"expires_at":null,"result":null,
		"error":{"code":"UPSTREAM_REJECTED","message":"event name must not contain spaces","retryable":false,"retry_after_ms":null,
		"circuit":null,"reason":null,"upstream":{"status":400,"code":"E_INVALID_EVENT"},"request_id":"req_01J9Z3M1A5B7C9D1E3F5G7H9JK"}}`)
	tests := []struct {
		name        string
		body        []byte
		wantState   hubclient.RequestState
		wantResult  bool
		wantErrCode hubclient.Code
		wantKey     string
		terminal    bool
	}{
		{name: "succeeded", body: pathExample(t, "/v1/requests/{request_id}", http.MethodGet, 200, "succeeded"), wantState: hubclient.StateSucceeded, wantResult: true, wantKey: "order-8821-created", terminal: true},
		{name: "parked", body: pathExample(t, "/v1/requests/{request_id}", http.MethodGet, 200, "parked"), wantState: hubclient.StateParked},
		{name: "dead lettered with error", body: failed, wantState: hubclient.StateDeadLettered, wantErrCode: hubclient.CodeUpstreamRejected, wantKey: "order-8822-created", terminal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := newHub(t, true, reply{status: http.StatusOK, body: tt.body})
			id := requestIDOf(tt.body)
			st, err := hub.client.GetRequest(t.Context(), id)
			if err != nil {
				t.Fatalf("GetRequest: %v", err)
			}
			if got := hub.last(t); got.Method != http.MethodGet || got.Path != "/v1/requests/"+id {
				t.Errorf("sent %s %s", got.Method, got.Path)
			}
			if st.RequestID != id || st.Status != tt.wantState || st.Status.Terminal() != tt.terminal || st.IdempotencyKey != tt.wantKey {
				t.Errorf("status = %+v", st)
			}
			if st.CreatedAt.IsZero() || st.UpdatedAt.IsZero() {
				t.Errorf("timestamps not decoded: %+v", st)
			}
			if (st.Result != nil) != tt.wantResult {
				t.Errorf("Result = %s, want present=%v", st.Result, tt.wantResult)
			}
			if tt.wantErrCode == "" {
				if st.Error != nil {
					t.Errorf("Error = %+v, want nil", st.Error)
				}
				return
			}
			if st.Error == nil || st.Error.Code != tt.wantErrCode || st.Error.Status != 0 || st.Error.Upstream == nil || st.Error.Upstream.Status != 400 {
				t.Errorf("Error = %+v", st.Error)
			}
		})
	}

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		body := componentExample(t, "RequestNotFound", "")
		hub := newHub(t, true, reply{status: http.StatusNotFound, body: body})
		_, err := hub.client.GetRequest(t.Context(), "req_01J9Z3M1A5B7C9D1E3F5G7H9JZ")
		var he *hubclient.Error
		if !errors.As(err, &he) {
			t.Fatalf("err = %v", err)
		}
		checkAgainstExample(t, he, http.StatusNotFound, body)
	})
}

func TestDryRun(t *testing.T) {
	t.Parallel()
	ok := pathExample(t, "/v1/integrations/{integration_id}/dry-run", http.MethodPost, 200, "")
	tests := []struct {
		name      string
		opts      []hubclient.InvokeOption
		wantQuery string
		wantSess  string
	}{
		{name: "default operation"},
		{
			name: "operation and session; invoke-only options are not sent",
			opts: []hubclient.InvokeOption{
				hubclient.WithOperation("generate"), hubclient.WithSession("ses_01J9Z6E5F6G7H8J9K0M1N2P3QR"),
				hubclient.WithIdempotencyKey("k1"), hubclient.WithPreferAsync(), hubclient.WithDeadline(time.Second),
			},
			wantQuery: "operation=generate",
			wantSess:  "ses_01J9Z6E5F6G7H8J9K0M1N2P3QR",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := newHub(t, true, reply{status: http.StatusOK, body: ok})
			r, err := hub.client.DryRun(t.Context(), "int_evt_brandA", trackEvent(), tt.opts...)
			if err != nil {
				t.Fatalf("DryRun: %v", err)
			}
			got := hub.last(t)
			if got.Path != "/v1/integrations/int_evt_brandA/dry-run" || got.Query != tt.wantQuery {
				t.Errorf("sent %s?%s", got.Path, got.Query)
			}
			if got.Header.Get("X-Hub-Session") != tt.wantSess {
				t.Errorf("X-Hub-Session = %q", got.Header.Get("X-Hub-Session"))
			}
			for _, h := range []string{"Idempotency-Key", "Prefer", "X-Request-Deadline"} {
				if v := got.Header.Get(h); v != "" {
					t.Errorf("%s = %q, want it not sent on a dry run", h, v)
				}
			}
			u := r.UpstreamRequest
			if r.RequestID != "req_01J9Z3K8X2V7Q4M6N8P0R2S4T7" || r.Operation != "default" || u.Method != http.MethodPost || u.BodyEncoding != "json" {
				t.Errorf("result = %+v", r)
			}
			if _, err := url.Parse(u.URL); err != nil || u.URL == "" {
				t.Errorf("URL = %q", u.URL)
			}
			if u.Headers["X-CleverTap-Passcode"] != "***" || len(r.Redacted) != 2 {
				t.Errorf("redaction not decoded: headers=%v redacted=%v", u.Headers, r.Redacted)
			}
			var body map[string]any
			if err := json.Unmarshal(u.Body, &body); err != nil || body["d"] == nil {
				t.Errorf("Body = %s (%v)", u.Body, err)
			}
		})
	}

	t.Run("missing scope", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"error":{"code":"FORBIDDEN","message":"API key lacks scope dry_run","retryable":false,"retry_after_ms":null,
			"circuit":null,"reason":"missing_scope","upstream":null,"request_id":"req_01J9Z3M1A5B7C9D1E3F5G7H9JV"}}`)
		hub := newHub(t, true, reply{status: http.StatusForbidden, body: body})
		_, err := hub.client.DryRun(t.Context(), "int_evt_brandA", trackEvent())
		var he *hubclient.Error
		if !errors.As(err, &he) {
			t.Fatalf("err = %v", err)
		}
		checkAgainstExample(t, he, http.StatusForbidden, body)
	})
}

func TestListIntegrations(t *testing.T) {
	t.Parallel()
	ok := pathExample(t, "/v1/integrations", http.MethodGet, 200, "")
	tests := []struct {
		name      string
		opts      hubclient.ListOptions
		wantQuery url.Values
	}{
		{name: "first page", wantQuery: url.Values{}},
		{name: "page", opts: hubclient.ListOptions{Cursor: "eyJrIjoiaW50X2V2dF9icmFuZEEifQ", Limit: 2},
			wantQuery: url.Values{"cursor": {"eyJrIjoiaW50X2V2dF9icmFuZEEifQ"}, "limit": {"2"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := newHub(t, true, reply{status: http.StatusOK, body: ok})
			page, err := hub.client.ListIntegrations(t.Context(), tt.opts)
			if err != nil {
				t.Fatalf("ListIntegrations: %v", err)
			}
			got := hub.last(t)
			q, _ := url.ParseQuery(got.Query)
			if got.Path != "/v1/integrations" || q.Encode() != tt.wantQuery.Encode() {
				t.Errorf("sent %s?%s, want query %s", got.Path, got.Query, tt.wantQuery.Encode())
			}
			if len(page.Items) != 2 || page.NextCursor != "" {
				t.Fatalf("page = %+v", page)
			}
			first := page.Items[0]
			if first.IntegrationID != "int_evt_brandA" || first.DeliveryMode != "wait" ||
				first.Description != "Brand A product analytics events" || len(first.Operations) != 1 {
				t.Errorf("item = %+v", first)
			}
			// ADR-0023: each operation carries the request schema to follow.
			op := first.Operation("default")
			var schema struct {
				Required []string `json:"required"`
			}
			if op == nil || !op.Idempotent || json.Unmarshal(op.RequestSchema.Body, &schema) != nil || len(schema.Required) != 2 ||
				len(op.RequestSchema.Headers) == 0 || first.OperationNames()[0] != "default" {
				t.Errorf("operation = %+v", op)
			}
		})
	}
}

func TestAllIntegrations(t *testing.T) {
	t.Parallel()
	item := func(id string) string {
		return `{"integration_id":"` + id + `","operations":[{"name":"default","description":null,"idempotent":true,"request_schema":{"body":{"type":"object"}}}],"delivery_mode":"wait",` +
			`"interactive":false,"paused":false,"moves_money_or_quota":false,"idempotency_key_required":false,"description":null}`
	}
	p1 := []byte(`{"items":[` + item("int_a") + `,` + item("int_b") + `],"next_cursor":"c2"}`)
	p2 := []byte(`{"items":[` + item("int_c") + `],"next_cursor":null}`)

	t.Run("walks every page", func(t *testing.T) {
		t.Parallel()
		hub := newHub(t, true, reply{status: 200, body: p1}, reply{status: 200, body: p2})
		var ids []string
		for it, err := range hub.client.AllIntegrations(t.Context(), hubclient.ListOptions{Limit: 2}) {
			if err != nil {
				t.Fatalf("iterate: %v", err)
			}
			ids = append(ids, it.IntegrationID)
		}
		if want := []string{"int_a", "int_b", "int_c"}; len(ids) != 3 || ids[0] != want[0] || ids[2] != want[2] {
			t.Errorf("ids = %v, want %v", ids, want)
		}
		reqs := hub.requests()
		if len(reqs) != 2 || reqs[1].Query != "cursor=c2&limit=2" {
			t.Errorf("requests = %+v", reqs)
		}
	})

	t.Run("stops when the caller breaks", func(t *testing.T) {
		t.Parallel()
		hub := newHub(t, true, reply{status: 200, body: p1}, reply{status: 200, body: p2})
		var got int
		for _, err := range hub.client.AllIntegrations(t.Context(), hubclient.ListOptions{}) {
			if err != nil {
				t.Fatal(err)
			}
			if got++; got == 1 {
				break
			}
		}
		if n := len(hub.requests()); n != 1 {
			t.Errorf("%d requests, want 1", n)
		}
	})

	t.Run("yields the error once", func(t *testing.T) {
		t.Parallel()
		hub := newHub(t, true, reply{status: 401, body: componentExample(t, "Unauthorized", "")})
		var errs int
		for _, err := range hub.client.AllIntegrations(t.Context(), hubclient.ListOptions{}) {
			var he *hubclient.Error
			if !errors.As(err, &he) || he.Code != hubclient.CodeUnauthorized {
				t.Errorf("err = %v", err)
			}
			errs++
		}
		if errs != 1 {
			t.Errorf("%d errors yielded, want 1", errs)
		}
	})
}

func TestGetIntegration(t *testing.T) {
	t.Parallel()
	hub := newHub(t, true, reply{status: http.StatusOK, body: pathExample(t, "/v1/integrations/{integration_id}", http.MethodGet, 200, "")})
	d, err := hub.client.GetIntegration(t.Context(), "int_evt_brandA")
	if err != nil {
		t.Fatalf("GetIntegration: %v", err)
	}
	if got := hub.last(t).Path; got != "/v1/integrations/int_evt_brandA" {
		t.Errorf("path = %s", got)
	}
	if d.IntegrationID != "int_evt_brandA" || !d.Owned || d.Wait != 3*time.Second || d.TTL != 6*time.Hour ||
		d.OnCircuitOpen != "queue_if_allowed" || len(d.CallbackEvents) != 2 || d.Operation("default") == nil {
		t.Errorf("detail = %+v", d)
	}

	t.Run("errors", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			component string
			status    int
		}{{"IntegrationNotFound", 404}, {"Forbidden", 403}, {"RateLimited", 429}} {
			body := componentExample(t, tc.component, "")
			h := http.Header{}
			if tc.status == 429 {
				h.Set("Retry-After", "2")
			}
			hub := newHub(t, true, reply{status: tc.status, body: body, header: h})
			_, err := hub.client.GetIntegration(t.Context(), "int_evt_brandZ")
			var he *hubclient.Error
			if !errors.As(err, &he) {
				t.Fatalf("%s: err = %v", tc.component, err)
			}
			checkAgainstExample(t, he, tc.status, body)
		}
	})
}

func TestEmptyIdentifiersAreRejected(t *testing.T) {
	t.Parallel()
	hub := newHub(t, true, reply{status: http.StatusOK, body: []byte(`{}`)})
	ctx := t.Context()
	calls := map[string]func() error{
		"GetRequest":     func() error { _, err := hub.client.GetRequest(ctx, ""); return err },
		"GetIntegration": func() error { _, err := hub.client.GetIntegration(ctx, ""); return err },
		"DryRun":         func() error { _, err := hub.client.DryRun(ctx, "", trackEvent()); return err },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s with an empty id: no error", name)
		}
	}
	if n := len(hub.requests()); n != 0 {
		t.Errorf("%d requests sent, want none", n)
	}
}
