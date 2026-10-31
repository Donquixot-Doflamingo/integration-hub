package mockvendor

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type received struct {
	method string
	header http.Header
	body   string
}

func receiver(t *testing.T, status int) (*httptest.Server, func() received) {
	t.Helper()
	var mu sync.Mutex
	var got received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = received{r.Method, r.Header.Clone(), string(b)}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"received":true}`)
	}))
	t.Cleanup(srv.Close)
	return srv, func() received { mu.Lock(); defer mu.Unlock(); return got }
}

func trigger(t *testing.T, mv *Instance, trig WebhookTrigger) (int, WebhookTriggerResult) {
	t.Helper()
	var res WebhookTriggerResult
	code := mv.Control(t, http.MethodPost, "/__control/webhooks", trig, &res)
	return code, res
}

func TestWebhookDeliveryAndResult(t *testing.T) {
	mv := Start(t)
	srv, got := receiver(t, http.StatusAccepted)
	code, res := trigger(t, mv, WebhookTrigger{
		TargetURL: srv.URL + "/webhooks/in/clevertap/ct-brandA-in",
		Headers:   map[string]string{"X-Event": "delivered"},
		Body:      json.RawMessage(`{"event":"delivered","msg_id":"m_7781"}`),
	})
	if code != 200 || res.Error != nil || res.TargetStatus == nil || *res.TargetStatus != 202 ||
		res.TargetBody == nil || *res.TargetBody != `{"received":true}` || res.TargetHeaders["Content-Type"][0] != "application/json" {
		t.Fatalf("code %d result %+v", code, res)
	}
	r := got()
	if r.method != http.MethodPost || r.header.Get("X-Event") != "delivered" || r.header.Get("Content-Type") != "application/json" || r.body != `{"event":"delivered","msg_id":"m_7781"}` {
		t.Fatalf("receiver saw %+v", r)
	}
}

func TestWebhookHMACSignatureIsVerifiable(t *testing.T) {
	mv := Start(t)
	srv, got := receiver(t, http.StatusOK)
	raw := `{"event":"delivered"}`
	for _, offset := range []int64{0, -10 * 60 * 1000} { // now, and 10 minutes in the past (replay)
		before := time.Now().Add(time.Duration(offset) * time.Millisecond).Unix()
		trigger(t, mv, WebhookTrigger{
			TargetURL: srv.URL, BodyRaw: &raw,
			Signature: &Signature{Scheme: "hmac_sha256", Secret: "test-webhook-secret", Header: "X-CleverTap-Signature", TimestampOffsetMS: offset},
		})
		h := got().header.Get("X-CleverTap-Signature")
		parts := map[string]string{}
		for p := range strings.SplitSeq(h, ",") {
			k, v, _ := strings.Cut(p, "=")
			parts[k] = v
		}
		ts, err := strconv.ParseInt(parts["t"], 10, 64)
		if err != nil || ts < before || ts > before+2 {
			t.Fatalf("offset %d: timestamp %q, want ≈ %d", offset, parts["t"], before)
		}
		if want := SignHMAC("test-webhook-secret", ts, []byte(raw), ""); want != h {
			t.Fatalf("signature %q does not verify (want %q)", h, want)
		}
	}
	// Known vector so receivers in other languages can check their implementation.
	if got := SignHMAC("secret", 1700000000, []byte(`{}`), "t={ts},v1={hex}"); !strings.HasPrefix(got, "t=1700000000,v1=") || len(got) != len("t=1700000000,v1=")+64 {
		t.Fatalf("vector %q", got)
	}
}

func TestWebhookBasicAuthAndCustomFormat(t *testing.T) {
	mv := Start(t)
	srv, got := receiver(t, http.StatusOK)
	trigger(t, mv, WebhookTrigger{TargetURL: srv.URL, Body: json.RawMessage(`{}`), Method: http.MethodPut,
		Signature: &Signature{Scheme: "basic", Secret: "user:pass", Header: "Authorization"}})
	r := got()
	if r.method != http.MethodPut || r.header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("user:pass")) {
		t.Fatalf("basic: %+v", r)
	}
	trigger(t, mv, WebhookTrigger{TargetURL: srv.URL, Body: json.RawMessage(`{}`),
		Signature: &Signature{Scheme: "hmac_sha256", Secret: "s3cret-value", Header: "X-Sig", Format: "sha256={hex}"}})
	if h := got().header.Get("X-Sig"); !strings.HasPrefix(h, "sha256=") || len(h) != len("sha256=")+64 {
		t.Fatalf("custom format %q", h)
	}
}

func TestWebhookUnreachableTargetReportsError(t *testing.T) {
	mv := Start(t)
	code, res := trigger(t, mv, WebhookTrigger{TargetURL: "http://127.0.0.1:1/x", Body: json.RawMessage(`{}`)})
	if code != 200 || res.Error == nil || res.TargetStatus != nil {
		t.Fatalf("code %d result %+v", code, res)
	}
}

func TestWebhookValidation(t *testing.T) {
	mv := Start(t)
	raw := "x"
	for _, trig := range []WebhookTrigger{
		{TargetURL: "not a url", Body: json.RawMessage(`{}`)},
		{TargetURL: "http://localhost/x"},
		{TargetURL: "http://localhost/x", Body: json.RawMessage(`{}`), BodyRaw: &raw},
		{TargetURL: "http://localhost/x", Body: json.RawMessage(`{}`), Method: "GET"},
		{TargetURL: "http://localhost/x", Body: json.RawMessage(`{}`), Signature: &Signature{Scheme: "rsa", Secret: "s", Header: "h"}},
	} {
		if code, _ := trigger(t, mv, trig); code != http.StatusBadRequest {
			t.Errorf("%+v: status %d, want 400", trig, code)
		}
	}
}

func TestWebhookDelay(t *testing.T) {
	mv := Start(t)
	srv, _ := receiver(t, http.StatusOK)
	start := time.Now()
	trigger(t, mv, WebhookTrigger{TargetURL: srv.URL, Body: json.RawMessage(`{}`), DelayMS: 100})
	if d := time.Since(start); d < 100*time.Millisecond {
		t.Fatalf("returned after %s, want >= 100ms", d)
	}
}
