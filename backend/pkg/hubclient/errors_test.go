package hubclient_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient"
)

// allCodes lists every exported code constant. Missing a constant does not compile; the test
// below fails when this list and the contract's enum differ.
var allCodes = []hubclient.Code{
	hubclient.CodeValidationFailed, hubclient.CodeIdempotencyKeyRequired, hubclient.CodeUnauthorized,
	hubclient.CodeForbidden, hubclient.CodeIntegrationNotFound, hubclient.CodeOperationNotFound,
	hubclient.CodeCapabilityNotFound, hubclient.CodeRequestNotFound, hubclient.CodeSessionNotFound,
	hubclient.CodeIdempotencyConflict, hubclient.CodeIdempotentReplay, hubclient.CodePayloadTooLarge,
	hubclient.CodeTransformFailed, hubclient.CodeInternalError, hubclient.CodeUpstreamTimeout,
	hubclient.CodeUpstreamUnavailable, hubclient.CodeUpstreamRateLimited, hubclient.CodeUpstreamRejected,
	hubclient.CodeUpstreamAuthFailed, hubclient.CodeIntegrationPaused, hubclient.CodeNotReady,
	hubclient.CodeExpired,
}

func contractCodes(t *testing.T) []string {
	t.Helper()
	dir, err := contract.Dir()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "schemas", "events", "canonical-error.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Properties struct {
			Code struct {
				Enum []string `json:"enum"`
			} `json:"code"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s.Properties.Code.Enum
}

func TestCodes_MirrorContractEnum(t *testing.T) {
	t.Parallel()
	enum := contractCodes(t)
	if len(enum) == 0 {
		t.Fatal("contract enum is empty")
	}
	var ours []string
	for _, c := range allCodes {
		ours = append(ours, string(c))
	}
	slices.Sort(enum)
	slices.Sort(ours)
	if !slices.Equal(enum, ours) {
		t.Errorf("codes differ from canonical-error.json\n contract: %v\n sdk:      %v", enum, ours)
	}
}

// TestError_EachCodeDecodes sends every code of the contract enum in an envelope and checks it
// comes back as that code (statuses here are synthetic, so the exchange is not validated).
func TestError_EachCodeDecodes(t *testing.T) {
	t.Parallel()
	for _, code := range contractCodes(t) {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			body := fmt.Sprintf(`{"error":{"code":%q,"message":"m","retryable":true,"retry_after_ms":1500,"request_id":%q}}`, code, testRequestID)
			hub := newHub(t, false, reply{status: http.StatusConflict, body: []byte(body)})
			_, err := hub.client.Invoke(t.Context(), "int_evt_brandA", trackEvent())
			var he *hubclient.Error
			if !errors.As(err, &he) {
				t.Fatalf("err = %v (%T), want *hubclient.Error", err, err)
			}
			if string(he.Code) != code || he.Status != http.StatusConflict || !he.Retryable || he.RetryAfter != 1500*time.Millisecond || he.RequestID != testRequestID {
				t.Errorf("decoded %+v", he)
			}
		})
	}
}

// envelopeWant is the contract example's error object, read independently of the SDK.
type envelopeWant struct {
	Code         string  `json:"code"`
	Message      string  `json:"message"`
	Retryable    bool    `json:"retryable"`
	RetryAfterMS *int64  `json:"retry_after_ms"`
	Circuit      *string `json:"circuit"`
	Reason       *string `json:"reason"`
	Upstream     *struct {
		Status *int    `json:"status"`
		Code   *string `json:"code"`
	} `json:"upstream"`
	Details []struct {
		Pointer string `json:"pointer"`
		Keyword string `json:"keyword"`
		Message string `json:"message"`
	} `json:"details"`
	RequestID string `json:"request_id"`
}

func checkAgainstExample(t *testing.T, he *hubclient.Error, status int, example []byte) {
	t.Helper()
	var env struct {
		Error envelopeWant `json:"error"`
	}
	if err := json.Unmarshal(example, &env); err != nil {
		t.Fatal(err)
	}
	w := env.Error
	if he.Status != status {
		t.Errorf("Status = %d, want %d", he.Status, status)
	}
	if string(he.Code) != w.Code || he.Message != w.Message || he.Retryable != w.Retryable || he.RequestID != w.RequestID {
		t.Errorf("got %+v, want %+v", he, w)
	}
	var wantRA time.Duration
	if w.RetryAfterMS != nil {
		wantRA = time.Duration(*w.RetryAfterMS) * time.Millisecond
	}
	if he.RetryAfter != wantRA {
		t.Errorf("RetryAfter = %v, want %v", he.RetryAfter, wantRA)
	}
	if w.Reason != nil && he.Reason != *w.Reason || w.Reason == nil && he.Reason != "" {
		t.Errorf("Reason = %q, want %v", he.Reason, w.Reason)
	}
	if w.Circuit != nil && he.Circuit != *w.Circuit || w.Circuit == nil && he.Circuit != "" {
		t.Errorf("Circuit = %q, want %v", he.Circuit, w.Circuit)
	}
	if (w.Upstream == nil) != (he.Upstream == nil) {
		t.Fatalf("Upstream = %+v, want %+v", he.Upstream, w.Upstream)
	}
	if w.Upstream != nil {
		if w.Upstream.Status != nil && he.Upstream.Status != *w.Upstream.Status || w.Upstream.Code != nil && he.Upstream.Code != *w.Upstream.Code {
			t.Errorf("Upstream = %+v, want %+v", he.Upstream, w.Upstream)
		}
	}
	if len(he.Details) != len(w.Details) {
		t.Fatalf("Details = %+v, want %+v", he.Details, w.Details)
	}
	for i, d := range w.Details {
		if he.Details[i] != (hubclient.ErrorDetail{Pointer: d.Pointer, Keyword: d.Keyword, Message: d.Message}) {
			t.Errorf("Details[%d] = %+v, want %+v", i, he.Details[i], d)
		}
	}
	if IsRetryable := hubclient.IsRetryable(he); IsRetryable != w.Retryable {
		t.Errorf("IsRetryable = %v, want %v", IsRetryable, w.Retryable)
	}
	if !strings.Contains(he.Error(), w.Code) || !strings.Contains(he.Error(), w.RequestID) {
		t.Errorf("Error() = %q lacks code or request id", he.Error())
	}
}

// TestInvoke_ContractErrors replays every error example the contract declares for the invoke
// operations and checks each decodes into the matching *Error.
func TestInvoke_ContractErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		component string
		status    int
		operation string        // non-empty: invokeIntegrationOperation
		header    http.Header   // Retry-After where the contract declares it
		wantRA    time.Duration // when the example has no retry_after_ms; 0 = take it from the example
	}{
		{component: "BadRequest", status: 400},
		{component: "Unauthorized", status: 401},
		{component: "Forbidden", status: 403},
		{component: "IntegrationNotFound", status: 404},
		{component: "InvokeNotFound", status: 404, operation: "verify"},
		{component: "IdempotencyConflict", status: 409},
		{component: "PayloadTooLarge", status: 413},
		{component: "UpstreamRejected", status: 422},
		{component: "RateLimited", status: 429, header: http.Header{"Retry-After": {"2"}}},
		{component: "InternalError", status: 500},
		{component: "UpstreamAuthFailed", status: 502},
		{component: "ServiceUnavailable", status: 503, header: http.Header{"Retry-After": {"30"}}},
		{component: "UpstreamTimeout", status: 504},
	}
	for _, tt := range tests {
		for _, ex := range componentExamples(t, tt.component) {
			t.Run(tt.component+"/"+ex.Name, func(t *testing.T) {
				t.Parallel()
				hub := newHub(t, true, reply{status: tt.status, body: ex.JSON, header: tt.header})
				var opts []hubclient.InvokeOption
				if tt.operation != "" {
					opts = append(opts, hubclient.WithOperation(tt.operation))
				}
				out, err := hub.client.Invoke(t.Context(), "int_evt_brandA", trackEvent(), opts...)
				if out != nil {
					t.Fatalf("outcome = %T, want nil on error", out)
				}
				var he *hubclient.Error
				if !errors.As(err, &he) {
					t.Fatalf("err = %v (%T), want *hubclient.Error", err, err)
				}
				checkAgainstExample(t, he, tt.status, ex.JSON)
			})
		}
	}
}

func TestError_RetryAfter(t *testing.T) {
	t.Parallel()
	envelope := func(ms string) []byte {
		return []byte(`{"error":{"code":"UPSTREAM_UNAVAILABLE","message":"m","retryable":true,"retry_after_ms":` + ms + `,"request_id":"` + testRequestID + `"}}`)
	}
	tests := []struct {
		name   string
		body   []byte
		header string
		want   time.Duration
		slack  time.Duration
	}{
		{name: "envelope ms wins over the rounded-up header", body: envelope("1500"), header: "2", want: 1500 * time.Millisecond},
		{name: "envelope ms without header", body: envelope("30000"), want: 30 * time.Second},
		{name: "null ms falls back to header seconds", body: envelope("null"), header: "7", want: 7 * time.Second},
		{name: "no hint at all", body: envelope("null"), want: 0},
		{name: "zero ms and no header", body: envelope("0"), want: 0},
		{name: "invalid header is ignored", body: envelope("null"), header: "soon", want: 0},
		{name: "negative header is ignored", body: envelope("null"), header: "-5", want: 0},
		{name: "absurd header is capped at 24 h", body: envelope("null"), header: "999999999999", want: 24 * time.Hour},
		{name: "HTTP-date header", body: envelope("null"), header: time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat), want: 90 * time.Second, slack: 3 * time.Second},
		{name: "HTTP-date in the past", body: envelope("null"), header: time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), want: 0},
		{name: "non-envelope 503 with header", body: []byte("<html>busy</html>"), header: "12", want: 12 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			if tt.header != "" {
				h.Set("Retry-After", tt.header)
			}
			hub := newHub(t, false, reply{status: http.StatusServiceUnavailable, body: tt.body, header: h})
			_, err := hub.client.Invoke(t.Context(), "int_evt_brandA", trackEvent())
			var he *hubclient.Error
			if !errors.As(err, &he) {
				t.Fatalf("err = %v, want *hubclient.Error", err)
			}
			if d := he.RetryAfter - tt.want; d > 0 || -d > tt.slack {
				t.Errorf("RetryAfter = %v, want %v (±%v)", he.RetryAfter, tt.want, tt.slack)
			}
		})
	}
}

// TestError_NonEnvelope covers failures that do not come from the Hub itself, such as a proxy
// in front of it. They are off-contract by nature, so exchanges are not validated.
func TestError_NonEnvelope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		status        int
		body          string
		header        http.Header
		wantRetryable bool
		wantRequestID string
	}{
		{name: "proxy 502 HTML", status: 502, body: "<html><body>Bad Gateway</body></html>", header: http.Header{"Content-Type": {"text/html"}, "X-Hub-Request-Id": nil}, wantRetryable: true},
		{name: "503 empty body", status: 503, wantRetryable: true},
		{name: "504 plain text", status: 504, body: "upstream request timeout", header: http.Header{"Content-Type": {"text/plain"}}, wantRetryable: true},
		{name: "429 from a load balancer", status: 429, body: "slow down", wantRetryable: true},
		{name: "500 JSON that is not an envelope", status: 500, body: `{"message":"boom"}`, wantRequestID: testRequestID},
		{name: "404 envelope without code", status: 404, body: `{"error":{"message":"x"}}`, wantRequestID: testRequestID},
		{name: "413 from a proxy", status: 413, body: "too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := tt.header
			if h == nil {
				h = http.Header{}
			}
			if tt.wantRequestID != "" {
				h.Set("X-Hub-Request-Id", tt.wantRequestID)
			} else {
				h["X-Hub-Request-Id"] = nil // a proxy does not set it
			}
			srvHub := newHub(t, false, reply{status: tt.status, body: []byte(tt.body), header: h})
			_, err := srvHub.client.Invoke(t.Context(), "int_evt_brandA", trackEvent())
			var he *hubclient.Error
			if !errors.As(err, &he) {
				t.Fatalf("err = %v (%T), want *hubclient.Error", err, err)
			}
			if he.Status != tt.status || he.Code != hubclient.CodeInternalError || he.Retryable != tt.wantRetryable {
				t.Errorf("got Status=%d Code=%s Retryable=%v, want %d %s %v", he.Status, he.Code, he.Retryable, tt.status, hubclient.CodeInternalError, tt.wantRetryable)
			}
			if he.RequestID != tt.wantRequestID {
				t.Errorf("RequestID = %q, want %q", he.RequestID, tt.wantRequestID)
			}
			if !strings.Contains(he.Message, "without a Hub error envelope") {
				t.Errorf("Message = %q", he.Message)
			}
		})
	}
}

func TestIsRetryable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("connection reset"), false},
		{"retryable *Error", &hubclient.Error{Code: hubclient.CodeUpstreamTimeout, Retryable: true}, true},
		{"final *Error", &hubclient.Error{Code: hubclient.CodeUpstreamRejected}, false},
		{"wrapped retryable *Error", fmt.Errorf("send order: %w", &hubclient.Error{Code: hubclient.CodeUpstreamUnavailable, Retryable: true}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := hubclient.IsRetryable(tt.err); got != tt.want {
				t.Errorf("IsRetryable = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestError_Message(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  *hubclient.Error
		want string
	}{
		{"full", &hubclient.Error{Status: 503, Code: hubclient.CodeUpstreamUnavailable, Message: "Vendor circuit is open", RequestID: testRequestID}, "hubclient: UPSTREAM_UNAVAILABLE (HTTP 503): Vendor circuit is open [request " + testRequestID + "]"},
		{"from a status resource", &hubclient.Error{Code: hubclient.CodeExpired, Message: "Request passed its TTL"}, "hubclient: EXPIRED: Request passed its TTL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}
