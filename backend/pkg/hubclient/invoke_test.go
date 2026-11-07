package hubclient_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient"
)

func TestInvoke_Result(t *testing.T) {
	t.Parallel()
	ok := componentExample(t, "InvokeOk", "transformed")
	tests := []struct {
		name       string
		header     http.Header
		opts       []hubclient.InvokeOption
		wantReplay bool
		wantSess   string
		wantPath   string
	}{
		{name: "sync completion", wantPath: "/v1/integrations/int_evt_brandA/invoke"},
		{
			name:       "idempotent replay",
			header:     http.Header{"X-Hub-Idempotent-Replay": {"true"}},
			opts:       []hubclient.InvokeOption{hubclient.WithIdempotencyKey("order-8821-created")},
			wantReplay: true,
			wantPath:   "/v1/integrations/int_evt_brandA/invoke",
		},
		{
			name:     "step 1 of a multi-step operation returns the session",
			header:   http.Header{"X-Hub-Session": {"ses_01J9Z6E5F6G7H8J9K0M1N2P3QR"}, "X-Hub-Served-By": {"failover:1"}},
			opts:     []hubclient.InvokeOption{hubclient.WithOperation("generate")},
			wantSess: "ses_01J9Z6E5F6G7H8J9K0M1N2P3QR",
			wantPath: "/v1/integrations/int_evt_brandA/invoke/generate",
		},
		{
			name:     "debug-only integration version header is ignored",
			header:   http.Header{"X-Hub-Integration-Version": {"7"}},
			wantPath: "/v1/integrations/int_evt_brandA/invoke",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := newHub(t, true, reply{status: http.StatusOK, body: ok, header: tt.header})
			out, err := hub.client.Invoke(t.Context(), "int_evt_brandA", trackEvent(), tt.opts...)
			if err != nil {
				t.Fatalf("Invoke: %v", err)
			}
			res, isResult := out.(*hubclient.Result)
			if !isResult {
				t.Fatalf("outcome = %T, want *hubclient.Result", out)
			}
			jsonEqual(t, res.Body, ok)
			if res.RequestID != testRequestID {
				t.Errorf("RequestID = %q, want %q", res.RequestID, testRequestID)
			}
			if res.IdempotentReplay != tt.wantReplay {
				t.Errorf("IdempotentReplay = %v, want %v", res.IdempotentReplay, tt.wantReplay)
			}
			if res.Session != tt.wantSess {
				t.Errorf("Session = %q, want %q", res.Session, tt.wantSess)
			}
			var decoded struct {
				Status   string `json:"status"`
				Accepted int    `json:"accepted"`
			}
			if err := res.Decode(&decoded); err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if decoded.Status != "ok" || decoded.Accepted != 1 {
				t.Errorf("decoded = %+v", decoded)
			}
			if got := hub.last(t).Path; got != tt.wantPath {
				t.Errorf("path = %q, want %q", got, tt.wantPath)
			}
		})
	}
}

func TestInvoke_202IsOnlyQueued(t *testing.T) {
	t.Parallel()
	for _, ex := range componentExamples(t, "Accepted") {
		for _, replay := range []bool{false, true} {
			name := ex.Name
			if replay {
				name += " replayed"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				h := http.Header{"Location": {"/v1/requests/" + requestIDOf(ex.JSON)}}
				if replay {
					h.Set("X-Hub-Idempotent-Replay", "true")
				}
				hub := newHub(t, true, reply{status: http.StatusAccepted, body: ex.JSON, header: h})
				out, err := hub.client.Invoke(t.Context(), "int_evt_brandA", trackEvent(), hubclient.WithPreferAsync())
				if err != nil {
					t.Fatalf("Invoke: %v", err)
				}
				switch o := out.(type) {
				case *hubclient.Queued:
					var want struct {
						RequestID string `json:"request_id"`
						Status    string `json:"status"`
						Reason    string `json:"reason"`
						Poll      string `json:"poll"`
					}
					if err := json.Unmarshal(ex.JSON, &want); err != nil {
						t.Fatal(err)
					}
					if o.RequestID != want.RequestID || string(o.Status) != want.Status || o.Reason != want.Reason || o.Poll != want.Poll {
						t.Errorf("Queued = %+v, want %+v", *o, want)
					}
					if o.IdempotentReplay != replay {
						t.Errorf("IdempotentReplay = %v, want %v", o.IdempotentReplay, replay)
					}
				case *hubclient.Result:
					t.Fatalf("a 202 was returned as a Result: %+v", o)
				default:
					t.Fatalf("outcome = %T", out)
				}
			})
		}
	}
}

// TestOutcome_IsSealed proves no type outside the package can implement Outcome: every method
// of the interface is unexported.
func TestOutcome_IsSealed(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[hubclient.Outcome]()
	if typ.NumMethod() == 0 {
		t.Fatal("Outcome has no methods; any type would satisfy it")
	}
	for i := range typ.NumMethod() {
		if m := typ.Method(i); m.IsExported() {
			t.Errorf("Outcome method %s is exported; the interface is not sealed", m.Name)
		}
	}
	var _ hubclient.Outcome = (*hubclient.Result)(nil)
	var _ hubclient.Outcome = (*hubclient.Queued)(nil)
}

func TestInvoke_RequestHeaders(t *testing.T) {
	t.Parallel()
	ok := componentExample(t, "InvokeOk", "transformed")
	tests := []struct {
		name     string
		opts     []hubclient.InvokeOption
		want     map[string]string // header -> value; "" means absent
		wantPath string
	}{
		{
			name: "no options",
			want: map[string]string{"Idempotency-Key": "", "Prefer": "", "X-Request-Deadline": "", "X-Hub-Session": "", "Traceparent": ""},
		},
		{
			name: "every option",
			opts: []hubclient.InvokeOption{
				hubclient.WithIdempotencyKey("order-8821-created"),
				hubclient.WithOperation("verify"),
				hubclient.WithPreferAsync(),
				hubclient.WithDeadline(2 * time.Second),
				hubclient.WithSession("ses_01J9Z6E5F6G7H8J9K0M1N2P3QR"),
				hubclient.WithTraceparent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"),
			},
			want: map[string]string{
				"Idempotency-Key":    "order-8821-created",
				"Prefer":             "respond-async",
				"X-Request-Deadline": "2000",
				"X-Hub-Session":      "ses_01J9Z6E5F6G7H8J9K0M1N2P3QR",
				"Traceparent":        "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			},
			wantPath: "/v1/integrations/int_evt_brandA/invoke/verify",
		},
		{name: "deadline rounds up to whole ms", opts: []hubclient.InvokeOption{hubclient.WithDeadline(1500 * time.Microsecond)}, want: map[string]string{"X-Request-Deadline": "2"}},
		{name: "tiny deadline is at least 1 ms", opts: []hubclient.InvokeOption{hubclient.WithDeadline(time.Nanosecond)}, want: map[string]string{"X-Request-Deadline": "1"}},
		{name: "huge deadline is clamped to 10 min", opts: []hubclient.InvokeOption{hubclient.WithDeadline(time.Hour)}, want: map[string]string{"X-Request-Deadline": "600000"}},
		{name: "default operation", opts: []hubclient.InvokeOption{hubclient.WithOperation("default")}, wantPath: "/v1/integrations/int_evt_brandA/invoke/default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := newHub(t, true, reply{status: http.StatusOK, body: ok})
			if _, err := hub.client.Invoke(t.Context(), "int_evt_brandA", trackEvent(), tt.opts...); err != nil {
				t.Fatalf("Invoke: %v", err)
			}
			got := hub.last(t)
			if got.Method != http.MethodPost {
				t.Errorf("method = %s", got.Method)
			}
			wantPath := tt.wantPath
			if wantPath == "" {
				wantPath = "/v1/integrations/int_evt_brandA/invoke"
			}
			if got.Path != wantPath {
				t.Errorf("path = %q, want %q", got.Path, wantPath)
			}
			fixed := map[string]string{
				"Authorization": "Bearer " + testKey,
				"Content-Type":  "application/json",
				"Accept":        "application/json",
				"User-Agent":    "hubclient-go/" + hubclient.Version,
			}
			for k, v := range fixed {
				if g := got.Header.Get(k); g != v {
					t.Errorf("%s = %q, want %q", k, g, v)
				}
			}
			for k, v := range tt.want {
				if g := got.Header.Get(k); g != v {
					t.Errorf("%s = %q, want %q", k, g, v)
				}
			}
			jsonEqual(t, got.Body, mustJSON(t, trackEvent()))
		})
	}
}

func TestInvoke_Bodies(t *testing.T) {
	t.Parallel()
	ok := componentExample(t, "InvokeOk", "transformed")
	raw := `{"user":{"id":"u_1"},"event":{"name":"signup"}}`
	tests := []struct {
		name string
		body any
	}{
		{"map", map[string]any{"user": map[string]any{"id": "u_1"}, "event": map[string]any{"name": "signup"}}},
		{"json.RawMessage is sent as is", json.RawMessage(raw)},
		{"[]byte is sent as is", []byte(raw)},
		{"struct", struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
			Event struct {
				Name string `json:"name"`
			} `json:"event"`
		}{User: struct {
			ID string `json:"id"`
		}{"u_1"}, Event: struct {
			Name string `json:"name"`
		}{"signup"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := newHub(t, true, reply{status: http.StatusOK, body: ok})
			if _, err := hub.client.Invoke(t.Context(), "int_evt_brandA", tt.body); err != nil {
				t.Fatalf("Invoke: %v", err)
			}
			jsonEqual(t, hub.last(t).Body, []byte(raw))
		})
	}
}

func TestInvoke_RejectedBeforeSending(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		id      string
		body    any
		opts    []hubclient.InvokeOption
		wantErr string
	}{
		{name: "empty integration id", id: "", body: trackEvent(), wantErr: "integration id is empty"},
		{name: "nil body", id: "int_evt_brandA", body: nil, wantErr: "body is required"},
		{name: "raw body that is not JSON", id: "int_evt_brandA", body: json.RawMessage("{nope"), wantErr: "not valid JSON"},
		{name: "unmarshalable body", id: "int_evt_brandA", body: map[string]any{"f": func() {}}, wantErr: "encode request body"},
		{name: "idempotency key with a space", id: "int_evt_brandA", body: trackEvent(), opts: []hubclient.InvokeOption{hubclient.WithIdempotencyKey("order 1")}, wantErr: "idempotency key"},
		{name: "idempotency key too long", id: "int_evt_brandA", body: trackEvent(), opts: []hubclient.InvokeOption{hubclient.WithIdempotencyKey(strings.Repeat("k", 256))}, wantErr: "idempotency key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := newHub(t, true, reply{status: http.StatusOK, body: []byte(`{}`)})
			out, err := hub.client.Invoke(t.Context(), tt.id, tt.body, tt.opts...)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
			if out != nil {
				t.Errorf("outcome = %v, want nil", out)
			}
			var he *hubclient.Error
			if errors.As(err, &he) {
				t.Errorf("a client-side error must not be a Hub *Error: %v", he)
			}
			if n := len(hub.requests()); n != 0 {
				t.Errorf("%d request(s) sent, want none", n)
			}
		})
	}
}

func TestInvoke_MalformedSuccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rp      reply
		wantErr string
	}{
		{"200 that is not JSON", reply{status: http.StatusOK, body: []byte("<html>ok</html>"), header: http.Header{"Content-Type": {"text/html"}}}, "not valid JSON"},
		{"202 that is not JSON", reply{status: http.StatusAccepted, body: []byte("accepted")}, "decode 202"},
		{"202 without status", reply{status: http.StatusAccepted, body: []byte(`{"request_id":"` + testRequestID + `"}`)}, "lacks request_id or status"},
		{"undeclared 204", reply{status: http.StatusNoContent}, "unexpected HTTP 204"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := newHub(t, false, tt.rp) // off-contract on purpose
			out, err := hub.client.Invoke(t.Context(), "int_evt_brandA", trackEvent())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
			if out != nil {
				t.Errorf("outcome = %T, want nil: a malformed response must never look like success", out)
			}
		})
	}
}
