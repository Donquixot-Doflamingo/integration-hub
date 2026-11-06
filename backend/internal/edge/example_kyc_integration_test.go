//go:build integration

package edge

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/idempotency"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/sessions"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

// HUB-351, HUB-352: the runnable proof behind examples/aadhaar-okyc/README.md and
// examples/pan-verify/README.md. An onboarding backend (tenant kyc-onboarding) runs Aadhaar OTP
// e-KYC and PAN verification through the real gateway, against mockvendor programmed with the
// example's scenario files (examples/aadhaar-okyc/mockvendor/*.json, the same files run.sh uses)
// to answer like the KYC provider's documented API:
//
//	step 1  POST /v1/integrations/int_aadhaar_okyc/invoke/send_otp    -> 200 otp_sent + X-Hub-Session
//	step 2  POST /v1/integrations/int_aadhaar_okyc/invoke/verify_otp  (X-Hub-Session) -> 200 verified + minimal e-KYC
//	PAN     POST /v1/integrations/int_pan_verify/invoke                -> 200 pan_status + name_match
//
// Every exchange is validated against contracts/openapi/hub-public.v1.yaml, every caller response
// is checked for the provider's name, the account and the fake credentials, and the audit trail
// must be sealed (D6). Every vendor region, the sandbox one included, points at mockvendor.

// kycCreds serves obviously fake client id / secret values per account.
type kycCreds struct{}

func (kycCreds) Load(_ context.Context, ids []string) (map[string]creds.AccountSecrets, error) {
	out := map[string]creds.AccountSecrets{}
	for _, id := range ids {
		out[id] = creds.AccountSecrets{Active: creds.Slot{Version: 1, Values: map[string]string{
			"client_id": "fake-client-id-" + id, "client_secret": "fake-client-secret-" + id}}}
	}
	return out, nil
}

type kycRig struct {
	h      http.Handler
	mv     *mockvendor.Instance
	db     *pgxpool.Pool
	sink   *recordingSink
	sealer *audit.Sealer
	spec   *contract.Spec
	run    string // unique per rig, for idempotency keys
}

func newKYCRig(t *testing.T) *kycRig {
	t.Helper()
	dsn := os.Getenv("HUB_PG_DSN")
	if dsn == "" {
		t.Fatal("HUB_PG_DSN not set; run via `make itest`")
	}
	sdb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(t.Context(), sdb); err != nil {
		t.Fatal(err)
	}
	_ = sdb.Close()
	db, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	mv := mockvendor.Start(t)
	snap := overlaySnapshot(t, kycExampleDir)
	for _, v := range snap.Vendors {
		for region := range v.Regions {
			v.Regions[region] = mv.VendorURL + "/verification" // the provider's hosts share this prefix
		}
	}
	store := &registry.Store{}
	store.Swap(snap)
	keys, err := audit.NewLocalKeys([]byte("kyc-example-seal-master-key-0123"))
	if err != nil {
		t.Fatal(err)
	}
	r := &kycRig{mv: mv, db: db, sink: &recordingSink{}, sealer: audit.NewSealer(keys), spec: contract.Load(t, "hub-public.v1.yaml"), run: core.NewULID(time.Now())}
	m := telemetry.NewMetrics("gateway", "kyc-example")
	cache, _ := creds.NewCache(kycCreds{}, telemetry.NewSecretSet(), m, creds.CacheOptions{})
	if err := cache.Warm(t.Context(), creds.AccountIDs(snap)); err != nil {
		t.Fatal(err)
	}
	client, _ := upstream.New(m, upstream.Options{})
	t.Cleanup(client.Close)
	rdb := redis.NewClient(&redis.Options{Addr: os.Getenv("HUB_REDIS_ADDR")})
	t.Cleanup(func() { _ = rdb.Close() })
	d := Deps{Snapshots: store, Creds: cache, Upstream: client, Metrics: m, Log: slog.New(slog.DiscardHandler), Audit: r.sink,
		Masker: audit.NewMasker([]byte("kyc-example-mask-key")), Sealer: r.sealer,
		Sessions: db, Idempotency: idempotency.New(rdb, m, idempotency.Options{Prefix: "itest:kyc:" + r.run + ":"})}
	r.h = New(d).Handler()
	return r
}

// scenario resets mockvendor and programs one examples/aadhaar-okyc/mockvendor/<name>.json file.
func (r *kycRig) scenario(t *testing.T, name string) {
	t.Helper()
	r.mv.Reset(t)
	for id, route := range loadKYCScenario(t, name).Routes {
		r.mv.SetRoute(t, id, route)
	}
}

// calls returns the vendor calls since the last scenario().
func (r *kycRig) calls(t *testing.T) []mockvendor.RecordedRequest {
	t.Helper()
	return r.mv.Requests(t, nil)
}

// only is the single vendor call since the last scenario(); it must have gone to path.
func (r *kycRig) only(t *testing.T, path string) (http.Header, map[string]any) {
	t.Helper()
	calls := r.calls(t)
	if len(calls) != 1 || calls[0].Path != path {
		t.Fatalf("vendor calls %d (first %v), want one to %s", len(calls), calls, path)
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("vendor body: %v", err)
	}
	return http.Header(calls[0].Headers), body
}

// kycLeaks are strings that must never reach a caller: the provider, the account, the credentials.
var kycLeaks = []string{"cashfree", "cf-sid", "fake-client", "sandbox."}

// call is the caller's HTTP request: path, Idempotency-Key (suffixed per run), X-Hub-Session, body.
func (r *kycRig) call(t *testing.T, path, key, session, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+kycKey)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key+"-"+r.run)
	}
	if session != "" {
		req.Header.Set("X-Hub-Session", session)
	}
	rec := r.spec.Serve(t, r.h, req)
	seen := strings.ToLower(rec.Body.String() + " " + strings.Join(headerValues(rec.Header()), " "))
	for _, leak := range kycLeaks {
		if strings.Contains(seen, leak) {
			t.Fatalf("%s: response leaks %q: %s", path, leak, rec.Body)
		}
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// audited returns the audit record and events of one request, waiting briefly for the batch.
func (r *kycRig) audited(t *testing.T, requestID string) (audit.Record, []audit.Event) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.sink.mu.Lock()
		var rec *audit.Record
		var events []audit.Event
		for _, b := range r.sink.batches {
			for i := range b.Records {
				if b.Records[i].RequestID == requestID {
					rec = &b.Records[i]
				}
			}
			for _, e := range b.Events {
				if e.RequestID == requestID {
					events = append(events, e)
				}
			}
		}
		r.sink.mu.Unlock()
		if rec != nil {
			return *rec, events
		}
		if time.Now().After(deadline) {
			t.Fatalf("no audit record for %s", requestID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// requireSealed checks that every payload of the request was sealed (never stored in clear), that
// the sealed copies open with the tenant key, and that none of them holds a credential. It returns
// the opened payloads by event type.
func (r *kycRig) requireSealed(t *testing.T, rec *httptest.ResponseRecorder, clear ...string) map[string]string {
	t.Helper()
	id := rec.Header().Get("X-Hub-Request-Id")
	record, events := r.audited(t, id)
	if record.PayloadPolicy != string(registry.PayloadSealed) {
		t.Fatalf("%s: payload policy %s, want sealed", id, record.PayloadPolicy)
	}
	opened, n := map[string]string{}, 0
	for _, e := range events {
		if e.SHA256 == "" {
			continue
		}
		if e.PayloadState != audit.PayloadSealed {
			t.Fatalf("%s %s: payload state %s", id, e.Type, e.PayloadState)
		}
		for _, s := range clear {
			if strings.Contains(string(e.Payload), s) {
				t.Fatalf("%s %s: %q stored in clear", id, e.Type, s)
			}
		}
		pt, _, err := r.sealer.Open(record.TenantID, e.RequestID, e.Seq, e.Payload)
		if err != nil {
			t.Fatalf("%s %s: open: %v", id, e.Type, err)
		}
		if strings.Contains(string(pt), "fake-client-secret") || strings.Contains(string(pt), kycKey) {
			t.Fatalf("%s %s: a sealed payload holds a credential", id, e.Type)
		}
		opened[e.Type] += string(pt)
		n++
	}
	if n < 2 {
		t.Fatalf("%s: %d sealed payloads", id, n)
	}
	return opened
}

const (
	aadhaarSend   = "/v1/integrations/int_aadhaar_okyc/invoke/send_otp"
	aadhaarVerify = "/v1/integrations/int_aadhaar_okyc/invoke/verify_otp"

	okycOTPPath    = "/verification/offline-aadhaar/otp"
	okycVerifyPath = "/verification/offline-aadhaar/verify"

	aadhaarNumber   = "655675523712" // the provider's documented sandbox test number
	aadhaarSendBody = `{"aadhaar_number": "655675523712", "consent": true, "purpose": "KYC for a personal loan application"}`
	aadhaarOTPBody  = `{"otp": "111000"}`
)

// sendAadhaarOTP programs scenario, runs step 1 and returns the session id.
func (r *kycRig) sendAadhaarOTP(t *testing.T, scenario, key string) string {
	t.Helper()
	r.scenario(t, scenario)
	rec, body := r.call(t, aadhaarSend, key, "", aadhaarSendBody)
	sid := rec.Header().Get("X-Hub-Session")
	if rec.Code != 200 || body["status"] != "otp_sent" || !sessions.IDPattern.MatchString(sid) {
		t.Fatalf("step 1: %d %s session %q", rec.Code, rec.Body, sid)
	}
	return sid
}

func TestExampleAadhaarOKYCFlow(t *testing.T) {
	r := newKYCRig(t)

	t.Run("happy path: send OTP, verify it in the session, minimal e-KYC data, sealed audit", func(t *testing.T) {
		r.scenario(t, "aadhaar-happy")
		rec, body := r.call(t, aadhaarSend, "okyc-send-happy", "", aadhaarSendBody)
		sid := rec.Header().Get("X-Hub-Session")
		if rec.Code != 200 || !sessions.IDPattern.MatchString(sid) || body["status"] != "otp_sent" || body["vendor_ref"] != "21637861" || len(body) != 2 {
			t.Fatalf("step 1: %d %s session %q", rec.Code, rec.Body, sid)
		}
		h, vb := r.only(t, okycOTPPath)
		// Only the Aadhaar number goes to the provider; consent and purpose stay in the Hub.
		if len(vb) != 1 || vb["aadhaar_number"] != aadhaarNumber ||
			h.Get("X-Client-Id") != "fake-client-id-cf-sid-test" || h.Get("X-Client-Secret") != "fake-client-secret-cf-sid-test" {
			t.Fatalf("step 1 vendor call %v %v", h, vb)
		}
		// The session keeps only the provider's reference: never the Aadhaar number.
		var data []byte
		var account string
		if err := r.db.QueryRow(t.Context(), `SELECT data, account_id FROM hub.vendor_sessions WHERE session_id = $1`, sid).Scan(&data, &account); err != nil {
			t.Fatal(err)
		}
		if string(data) != `{"vendor_txn_id": "21637861"}` || account != "cf-sid-test" {
			t.Fatalf("session row %s on %s", data, account)
		}
		// Step 1 is sealed: the Aadhaar number is in the sealed copy only, never in clear.
		if opened := r.requireSealed(t, rec, aadhaarNumber); !strings.Contains(opened[audit.RequestReceived], aadhaarNumber) {
			t.Fatalf("the sealed request.received does not hold the request: %q", opened[audit.RequestReceived])
		}

		// Step 2: the person types the OTP from the SMS; the app sends it with the session.
		r.scenario(t, "aadhaar-happy")
		rec, body = r.call(t, aadhaarVerify, "okyc-verify-happy", sid, aadhaarOTPBody)
		if rec.Code != 200 || rec.Header().Get("X-Hub-Session") != "" {
			t.Fatalf("step 2: %d %s", rec.Code, rec.Body)
		}
		want := `{"address":{"district":"Haveri","pincode":"581115","state":"Karnataka"},"date_of_birth":"1995-02-02","gender":"male","name":"Mallesh Fakkirappa Dollin","status":"verified","vendor_ref":"21637861","year_of_birth":1995}`
		if got, _ := json.Marshal(body); string(got) != want {
			t.Fatalf("step 2 body\n got %s\nwant %s", got, want)
		}
		// The minimal response drops the photo, street address, care-of, mobile hash, XML and share code.
		for _, dropped := range []string{"photo", "Umashankar", "S/O", "mobile_hash", "ed189eb7", "share_code", "xml", "okyc.zip"} {
			if strings.Contains(rec.Body.String(), dropped) {
				t.Fatalf("step 2 response carries %q: %s", dropped, rec.Body)
			}
		}
		_, vb = r.only(t, okycVerifyPath)
		if len(vb) != 2 || vb["otp"] != "111000" || vb["ref_id"] != "21637861" {
			t.Fatalf("step 2 vendor body %v", vb)
		}
		r.requireSealed(t, rec, "Umashankar", "111000")
	})

	t.Run("wrong OTP is a canonical rejection and the person can retry in the same session", func(t *testing.T) {
		sid := r.sendAadhaarOTP(t, "aadhaar-wrong-otp", "okyc-send-wrong")
		rec, body := r.call(t, aadhaarVerify, "okyc-verify-wrong-1", sid, `{"otp": "000111"}`)
		e := errorOf(body)
		if rec.Code != 422 || e["code"] != "UPSTREAM_REJECTED" || e["message"] != "The OTP does not match" || e["retryable"] != false {
			t.Fatalf("wrong OTP: %d %s", rec.Code, rec.Body)
		}
		r.scenario(t, "aadhaar-happy")
		rec, body = r.call(t, aadhaarVerify, "okyc-verify-wrong-2", sid, aadhaarOTPBody)
		if rec.Code != 200 || body["status"] != "verified" {
			t.Fatalf("retry with the right OTP: %d %s", rec.Code, rec.Body)
		}
		if _, vb := r.only(t, okycVerifyPath); vb["ref_id"] != "21637861" {
			t.Fatalf("retry vendor body %v", vb)
		}
	})

	t.Run("OTP expired at the provider is a rejection with a resend hint", func(t *testing.T) {
		sid := r.sendAadhaarOTP(t, "aadhaar-otp-expired", "okyc-send-expired")
		rec, body := r.call(t, aadhaarVerify, "okyc-verify-expired", sid, aadhaarOTPBody)
		if e := errorOf(body); rec.Code != 422 || e["code"] != "UPSTREAM_REJECTED" || e["message"] != "The OTP has expired; request a new one" {
			t.Fatalf("expired OTP: %d %s", rec.Code, rec.Body)
		}
	})

	t.Run("invalid Aadhaar (a 200 INVALID from the provider) is a rejection and opens no session", func(t *testing.T) {
		r.scenario(t, "aadhaar-invalid")
		rec, body := r.call(t, aadhaarSend, "okyc-send-invalid", "", `{"aadhaar_number": "655675523710", "consent": true, "purpose": "KYC"}`)
		e := errorOf(body)
		up, _ := e["upstream"].(map[string]any)
		if rec.Code != 422 || e["code"] != "UPSTREAM_REJECTED" || e["message"] != "The Aadhaar number is not valid" || up["status"] != float64(200) || rec.Header().Get("X-Hub-Session") != "" {
			t.Fatalf("invalid Aadhaar: %d %s session %q", rec.Code, rec.Body, rec.Header().Get("X-Hub-Session"))
		}
	})

	t.Run("provider rate limit is a retryable 429 with retry_after_ms", func(t *testing.T) {
		r.scenario(t, "aadhaar-rate-limited")
		rec, body := r.call(t, aadhaarSend, "okyc-send-limited", "", aadhaarSendBody)
		if e := errorOf(body); rec.Code != 429 || e["code"] != "UPSTREAM_RATE_LIMITED" || e["retryable"] != true || e["retry_after_ms"] != float64(30000) {
			t.Fatalf("rate limited: %d %s", rec.Code, rec.Body)
		}
	})

	t.Run("provider down during verify: retryable 503, the session survives, the same key retries", func(t *testing.T) {
		sid := r.sendAadhaarOTP(t, "aadhaar-provider-down", "okyc-send-down")
		rec, body := r.call(t, aadhaarVerify, "okyc-verify-down", sid, aadhaarOTPBody)
		if e := errorOf(body); rec.Code != 503 || e["code"] != "UPSTREAM_UNAVAILABLE" || e["retryable"] != true {
			t.Fatalf("provider down: %d %s", rec.Code, rec.Body)
		}
		r.scenario(t, "aadhaar-happy")
		rec, body = r.call(t, aadhaarVerify, "okyc-verify-down", sid, aadhaarOTPBody)
		if rec.Code != 200 || body["status"] != "verified" {
			t.Fatalf("retry after recovery: %d %s", rec.Code, rec.Body)
		}
	})

	t.Run("double tap on Send OTP: one provider call (one SMS), the same session", func(t *testing.T) {
		r.scenario(t, "aadhaar-happy")
		first, _ := r.call(t, aadhaarSend, "okyc-send-double", "", aadhaarSendBody)
		second, _ := r.call(t, aadhaarSend, "okyc-send-double", "", aadhaarSendBody)
		if first.Code != 200 || second.Code != 200 || second.Header().Get("X-Hub-Idempotent-Replay") != "true" {
			t.Fatalf("double tap: %d / %d replay %q", first.Code, second.Code, second.Header().Get("X-Hub-Idempotent-Replay"))
		}
		if a, b := first.Header().Get("X-Hub-Session"), second.Header().Get("X-Hub-Session"); a == "" || a != b {
			t.Fatalf("sessions %q / %q", a, b)
		}
		if n := len(r.calls(t)); n != 1 {
			t.Fatalf("%d provider calls, want 1", n)
		}
	})

	t.Run("expired Hub session is SESSION_NOT_FOUND without a provider call", func(t *testing.T) {
		sid := r.sendAadhaarOTP(t, "aadhaar-happy", "okyc-send-session-expired")
		if _, err := r.db.Exec(t.Context(), `UPDATE hub.vendor_sessions SET created_at = now() - interval '11 minutes', expires_at = now() - interval '1 minute' WHERE session_id = $1`, sid); err != nil {
			t.Fatal(err)
		}
		r.scenario(t, "aadhaar-happy")
		rec, body := r.call(t, aadhaarVerify, "okyc-verify-session-expired", sid, aadhaarOTPBody)
		if e := errorOf(body); rec.Code != 404 || e["code"] != "SESSION_NOT_FOUND" {
			t.Fatalf("expired session: %d %s", rec.Code, rec.Body)
		}
		if n := len(r.calls(t)); n != 0 {
			t.Fatalf("%d provider calls for an expired session", n)
		}
	})

	t.Run("caller mistakes are refused at the edge without a provider call", func(t *testing.T) {
		r.scenario(t, "aadhaar-happy")
		for name, c := range map[string]struct {
			path, key, body, code string
			status                int
			pointer               string
		}{
			"no Idempotency-Key":  {aadhaarSend, "", aadhaarSendBody, "IDEMPOTENCY_KEY_REQUIRED", 400, ""},
			"no consent":          {aadhaarSend, "okyc-no-consent", `{"aadhaar_number": "655675523712", "consent": false, "purpose": "KYC"}`, "VALIDATION_FAILED", 400, "/consent"},
			"malformed Aadhaar":   {aadhaarSend, "okyc-bad-aadhaar", `{"aadhaar_number": "1234 5678 9012", "consent": true, "purpose": "KYC"}`, "VALIDATION_FAILED", 400, "/aadhaar_number"},
			"5-digit OTP":         {aadhaarVerify, "okyc-short-otp", `{"otp": "11100"}`, "VALIDATION_FAILED", 400, "/otp"},
			"no operation named":  {"/v1/integrations/int_aadhaar_okyc/invoke", "okyc-no-op", aadhaarSendBody, "OPERATION_NOT_FOUND", 404, ""},
			"Aadhaar sent again":  {aadhaarVerify, "okyc-verify-extra", `{"otp": "111000", "aadhaar_number": "655675523712"}`, "VALIDATION_FAILED", 400, ""},
			"unknown integration": {"/v1/integrations/int_aadhaar_kyc/invoke/send_otp", "okyc-unknown", aadhaarSendBody, "INTEGRATION_NOT_FOUND", 404, ""},
		} {
			rec, body := r.call(t, c.path, c.key, "", c.body)
			e := errorOf(body)
			if rec.Code != c.status || e["code"] != c.code {
				t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
				continue
			}
			if c.pointer != "" {
				d, _ := e["details"].([]any)
				if len(d) == 0 || d[0].(map[string]any)["pointer"] != c.pointer {
					t.Errorf("%s: details %v", name, e["details"])
				}
			}
			if strings.Contains(rec.Body.String(), aadhaarNumber) {
				t.Errorf("%s: the error echoes the Aadhaar number: %s", name, rec.Body)
			}
		}
		if n := len(r.calls(t)); n != 0 {
			t.Fatalf("%d provider calls for refused requests", n)
		}
	})
}
