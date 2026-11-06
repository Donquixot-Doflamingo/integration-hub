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

// HUB-342, HUB-343: the runnable proof behind docs/use-cases/README.md (two-step flows) and
// examples/otp-account-flow/README.md. A loan onboarding backend (tenant loan-onboarding) runs
// the UX flow "send OTP -> user types OTP -> user types bank account" through the real gateway
// against mockvendor, playing the fictional vendor demo-kyc:
//
//	step 1  POST /v1/integrations/int_demo_otp/invoke/send_otp     -> 200 otp_sent + X-Hub-Session
//	step 2  POST /v1/integrations/int_demo_otp/invoke/verify_otp   (X-Hub-Session) -> 200 verified
//	step 3  POST /v1/integrations/int_demo_bank_verify/invoke      -> 200 valid + name at bank
//
// send_otp and verify_otp are two operations of one integration, each with its own transformer
// pair and so its own vendor endpoint (/v1/otp/send, /v1/otp/verify; per-operation transformers).
//
// Every exchange is validated against contracts/openapi/hub-public.v1.yaml.

// exampleCreds serves an obviously fake bearer token per account, so the vendor side shows which
// account a call was sent with.
type exampleCreds struct{}

func (exampleCreds) Load(_ context.Context, ids []string) (map[string]creds.AccountSecrets, error) {
	out := map[string]creds.AccountSecrets{}
	for _, id := range ids {
		out[id] = creds.AccountSecrets{Active: creds.Slot{Version: 1, Values: map[string]string{
			"token": "fake-token-" + id, "account_id": "fake-acct-" + id, "passcode": "fake-pass-" + id}}}
	}
	return out, nil
}

type exampleRig struct {
	h     http.Handler
	mv    *mockvendor.Instance
	db    *pgxpool.Pool
	store *registry.Store
	snaps map[int]*registry.Snapshot // int_demo_otp v1 and v2 active, built once (goldens run per build)
	spec  *contract.Spec
	run   string // unique per rig, for idempotency keys
}

func newExampleRig(t *testing.T) *exampleRig {
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
	r := &exampleRig{mv: mv, db: db, store: &registry.Store{}, snaps: map[int]*registry.Snapshot{}, spec: contract.Load(t, "hub-public.v1.yaml"),
		run: core.NewULID(time.Now())}
	for _, active := range []int{1, 2} {
		snap := exampleSnapshot(t, active)
		for _, v := range snap.Vendors {
			for region := range v.Regions {
				v.Regions[region] = mv.VendorURL
			}
		}
		r.snaps[active] = snap
	}
	r.publish(t, 1)
	m := telemetry.NewMetrics("gateway", "example")
	cache, _ := creds.NewCache(exampleCreds{}, telemetry.NewSecretSet(), m, creds.CacheOptions{})
	if err := cache.Warm(t.Context(), creds.AccountIDs(r.store.Snapshot())); err != nil {
		t.Fatal(err)
	}
	client, _ := upstream.New(m, upstream.Options{})
	t.Cleanup(client.Close)
	rdb := redis.NewClient(&redis.Options{Addr: os.Getenv("HUB_REDIS_ADDR")})
	t.Cleanup(func() { _ = rdb.Close() })
	d := Deps{Snapshots: r.store, Creds: cache, Upstream: client, Metrics: m, Log: slog.New(slog.DiscardHandler), Audit: &recordingSink{},
		Sessions: db, Idempotency: idempotency.New(rdb, m, idempotency.Options{Prefix: "itest:example:" + r.run + ":"})}
	r.h = New(d).Handler()
	return r
}

// publish swaps in the example snapshot with int_demo_otp v<active> (the publish-equivalent),
// every vendor region pointed at mockvendor.
func (r *exampleRig) publish(t *testing.T, active int) {
	t.Helper()
	r.store.Swap(r.snaps[active])
}

// vendor makes mockvendor answer the next calls to path with status and body.
func (r *exampleRig) vendor(t *testing.T, path string, status int, body string) {
	t.Helper()
	r.mv.Reset(t)
	r.mv.SetRoute(t, "demo", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: path},
		Behaviour: mockvendor.Behaviour{Status: status, Body: json.RawMessage(body)}})
}

// vendorCalls returns the requests mockvendor received since the last vendor().
func (r *exampleRig) vendorCalls(t *testing.T) []mockvendor.RecordedRequest {
	t.Helper()
	return r.mv.Requests(t, nil)
}

// otpVendor programs both OTP endpoints at once (send -> OTP_SENT, verify -> VERIFIED), so a
// call routed to the wrong endpoint gets the wrong answer and the wrong path is recorded.
func (r *exampleRig) otpVendor(t *testing.T) {
	t.Helper()
	r.mv.Reset(t)
	for name, route := range map[string][2]string{"send": {otpSendPath, vendorSent}, "verify": {otpVerifyPath, vendorVerified}} {
		r.mv.SetRoute(t, name, mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: route[0]},
			Behaviour: mockvendor.Behaviour{Status: 200, Body: json.RawMessage(route[1])}})
	}
}

// lastCall is the only vendor call since the last vendor() or otpVendor(), with its JSON body;
// it must have gone to path.
func (r *exampleRig) lastCall(t *testing.T, path string) (http.Header, map[string]any) {
	t.Helper()
	calls := r.vendorCalls(t)
	if len(calls) != 1 {
		t.Fatalf("%d vendor calls, want 1", len(calls))
	}
	if calls[0].Path != path {
		t.Fatalf("vendor call went to %s, want %s", calls[0].Path, path)
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("vendor body: %v", err)
	}
	return http.Header(calls[0].Headers), body
}

// call is the caller's HTTP request: path, Idempotency-Key, X-Hub-Session (optional), body.
func (r *exampleRig) call(t *testing.T, path, key, session, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+exampleKey)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key+"-"+r.run)
	}
	if session != "" {
		req.Header.Set("X-Hub-Session", session)
	}
	rec := r.spec.Serve(t, r.h, req)
	// The caller contract never names a vendor or account (DESIGN §0).
	for _, leak := range []string{"demo-kyc", "DEMO-CLIENT", "fake-token"} {
		if strings.Contains(rec.Body.String(), leak) || strings.Contains(strings.Join(headerValues(rec.Header()), " "), leak) {
			t.Fatalf("%s: response leaks %q: %s", path, leak, rec.Body)
		}
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func headerValues(h http.Header) []string {
	var out []string
	for _, vs := range h {
		out = append(out, vs...)
	}
	return out
}

const (
	otpSend   = "/v1/integrations/int_demo_otp/invoke/send_otp"
	otpVerify = "/v1/integrations/int_demo_otp/invoke/verify_otp"
	bankCheck = "/v1/integrations/int_demo_bank_verify/invoke"

	// The demo vendor's two OTP endpoints (vendor side only, never seen by the caller).
	otpSendPath   = "/v1/otp/send"
	otpVerifyPath = "/v1/otp/verify"
	bankPath      = "/v1/bank-accounts/verify"

	sendBody   = `{"mobile": "+919812345678"}`
	verifyBody = `{"mobile": "+919812345678", "otp": "482913"}`
	wrongBody  = `{"mobile": "+919812345678", "otp": "000000"}`
	bankBody   = `{"account_number": "50100123456789", "ifsc": "DEMO0001234", "name": "Asha Verma"}`

	vendorSent     = `{"status": "OTP_SENT", "txn_id": "dk_txn_7f3a", "expires_in": 300, "ref": "dk_ref_0001"}`
	vendorVerified = `{"status": "VERIFIED", "ref": "dk_ref_0002"}`
)

// sendOTP runs step 1 and returns the session id.
func (r *exampleRig) sendOTP(t *testing.T, key string) string {
	t.Helper()
	r.vendor(t, otpSendPath, 200, vendorSent)
	rec, body := r.call(t, otpSend, key, "", sendBody)
	sid := rec.Header().Get("X-Hub-Session")
	if rec.Code != 200 || body["status"] != "otp_sent" || !sessions.IDPattern.MatchString(sid) {
		t.Fatalf("step 1: %d %s session %q", rec.Code, rec.Body, sid)
	}
	return sid
}

func errorOf(body map[string]any) map[string]any {
	e, _ := body["error"].(map[string]any)
	return e
}

func TestExampleOTPAccountFlow(t *testing.T) {
	r := newExampleRig(t)

	t.Run("happy path: send OTP, verify it in the session, verify the bank account", func(t *testing.T) {
		r.publish(t, 1)
		// Step 1: the app asks for an OTP. Both OTP endpoints are live: the call must pick send.
		r.otpVendor(t)
		rec, body := r.call(t, otpSend, "otp-send-happy", "", sendBody)
		sid := rec.Header().Get("X-Hub-Session")
		if rec.Code != 200 || !sessions.IDPattern.MatchString(sid) {
			t.Fatalf("step 1: %d %s session %q", rec.Code, rec.Body, sid)
		}
		if body["status"] != "otp_sent" || body["expires_in_s"] != float64(300) || body["vendor_ref"] != "dk_ref_0001" {
			t.Fatalf("step 1 body %v", body)
		}
		h, vb := r.lastCall(t, otpSendPath)
		if len(vb) != 1 || vb["mobile"] != "+919812345678" || h.Get("X-Client-Id") != "DEMO-CLIENT-A" || h.Get("Authorization") != "Bearer fake-token-demo-kyc-a" {
			t.Fatalf("step 1 vendor call %v %v", h, vb)
		}
		// The session keeps only the vendor txn id: never the mobile number or the OTP.
		var data []byte
		var account string
		var version int
		if err := r.db.QueryRow(t.Context(), `SELECT data, account_id, version FROM hub.vendor_sessions WHERE session_id = $1`, sid).Scan(&data, &account, &version); err != nil {
			t.Fatal(err)
		}
		if string(data) != `{"vendor_txn_id": "dk_txn_7f3a"}` || account != "demo-kyc-a" || version != 1 {
			t.Fatalf("session row %s %s v%d", data, account, version)
		}

		// The platform team moves OTP traffic to account B (v2) while the user reads the SMS.
		r.publish(t, 2)

		// Step 2: the user types the OTP. verify_otp goes to the verify endpoint (its own
		// transformer pair), and the session pins the call to v1 and account A.
		r.otpVendor(t)
		rec, body = r.call(t, otpVerify, "otp-verify-happy", sid, verifyBody)
		if rec.Code != 200 || body["status"] != "verified" || rec.Header().Get("X-Hub-Session") != "" {
			t.Fatalf("step 2: %d %s session %q", rec.Code, rec.Body, rec.Header().Get("X-Hub-Session"))
		}
		h, vb = r.lastCall(t, otpVerifyPath)
		if len(vb) != 3 || vb["mobile"] != "+919812345678" || vb["txn_id"] != "dk_txn_7f3a" || vb["otp"] != "482913" ||
			h.Get("X-Client-Id") != "DEMO-CLIENT-A" || h.Get("Authorization") != "Bearer fake-token-demo-kyc-a" {
			t.Fatalf("step 2 vendor call %v %v", h, vb)
		}

		// Step 3: the user types the bank account. A separate integration, no session.
		r.vendor(t, bankPath, 200, `{"result": "VALID", "beneficiary_name": "ASHA VERMA", "name_match_score": 96, "ref": "dk_ref_0003"}`)
		rec, body = r.call(t, bankCheck, "bank-verify-happy", "", bankBody)
		if rec.Code != 200 || body["account_status"] != "valid" || body["name_at_bank"] != "ASHA VERMA" || body["name_match"] != true {
			t.Fatalf("step 3: %d %s", rec.Code, rec.Body)
		}
		_, vb = r.lastCall(t, bankPath)
		if vb["account_number"] != "50100123456789" || vb["ifsc"] != "DEMO0001234" || vb["reference"] != "bank-verify-happy-"+r.run {
			t.Fatalf("step 3 vendor body %v", vb)
		}

		// A new user who starts after the publish is served by v2 (account B).
		r.otpVendor(t)
		if rec, _ := r.call(t, otpSend, "otp-send-after-publish", "", sendBody); rec.Code != 200 {
			t.Fatalf("new step 1: %d %s", rec.Code, rec.Body)
		}
		if h, _ := r.lastCall(t, otpSendPath); h.Get("X-Client-Id") != "DEMO-CLIENT-B" {
			t.Fatalf("new session served by %q, want account B", h.Get("X-Client-Id"))
		}
	})

	t.Run("wrong OTP is a canonical rejection and the user can retry in the same session", func(t *testing.T) {
		r.publish(t, 1)
		sid := r.sendOTP(t, "otp-send-wrong")
		r.vendor(t, otpVerifyPath, 422, `{"error": "OTP_MISMATCH", "message": "otp mismatch, 2 attempts left"}`)
		rec, body := r.call(t, otpVerify, "otp-verify-wrong-1", sid, wrongBody)
		e := errorOf(body)
		up, _ := e["upstream"].(map[string]any)
		if rec.Code != 422 || e["code"] != "UPSTREAM_REJECTED" || e["message"] != "The OTP does not match" || e["retryable"] != false || up["status"] != float64(422) {
			t.Fatalf("wrong OTP: %d %s", rec.Code, rec.Body)
		}
		r.vendor(t, otpVerifyPath, 200, vendorVerified)
		rec, body = r.call(t, otpVerify, "otp-verify-wrong-2", sid, verifyBody)
		if rec.Code != 200 || body["status"] != "verified" {
			t.Fatalf("retry with the right OTP: %d %s", rec.Code, rec.Body)
		}
		if _, vb := r.lastCall(t, otpVerifyPath); vb["txn_id"] != "dk_txn_7f3a" {
			t.Fatalf("retry vendor body %v", vb)
		}
	})

	t.Run("OTP expired at the vendor is a rejection with a resend hint", func(t *testing.T) {
		r.publish(t, 1)
		sid := r.sendOTP(t, "otp-send-vendor-expired")
		r.vendor(t, otpVerifyPath, 422, `{"error": "OTP_EXPIRED", "message": "otp expired"}`)
		rec, body := r.call(t, otpVerify, "otp-verify-vendor-expired", sid, verifyBody)
		if e := errorOf(body); rec.Code != 422 || e["code"] != "UPSTREAM_REJECTED" || e["message"] != "The OTP has expired; request a new one" {
			t.Fatalf("expired OTP: %d %s", rec.Code, rec.Body)
		}
	})

	t.Run("expired Hub session is SESSION_NOT_FOUND without a vendor call; step 1 again opens a new one", func(t *testing.T) {
		r.publish(t, 1)
		sid := r.sendOTP(t, "otp-send-expired")
		// Ten minutes pass (DESIGN §14: the vendor txn id is kept 10 min).
		if _, err := r.db.Exec(t.Context(), `UPDATE hub.vendor_sessions SET created_at = now() - interval '11 minutes', expires_at = now() - interval '1 minute' WHERE session_id = $1`, sid); err != nil {
			t.Fatal(err)
		}
		r.vendor(t, otpVerifyPath, 200, vendorVerified)
		rec, body := r.call(t, otpVerify, "otp-verify-expired", sid, verifyBody)
		if e := errorOf(body); rec.Code != 404 || e["code"] != "SESSION_NOT_FOUND" || e["retryable"] != false {
			t.Fatalf("expired session: %d %s", rec.Code, rec.Body)
		}
		if n := len(r.vendorCalls(t)); n != 0 {
			t.Fatalf("%d vendor calls for an expired session", n)
		}
		if again := r.sendOTP(t, "otp-send-expired-again"); again == sid {
			t.Fatal("step 1 again returned the expired session")
		}
	})

	t.Run("double tap on Send OTP: one vendor call, the same session", func(t *testing.T) {
		r.publish(t, 1)
		r.vendor(t, otpSendPath, 200, vendorSent)
		first, _ := r.call(t, otpSend, "otp-send-double", "", sendBody)
		second, _ := r.call(t, otpSend, "otp-send-double", "", sendBody)
		if first.Code != 200 || second.Code != 200 || second.Header().Get("X-Hub-Idempotent-Replay") != "true" {
			t.Fatalf("double tap: %d / %d replay %q", first.Code, second.Code, second.Header().Get("X-Hub-Idempotent-Replay"))
		}
		if a, b := first.Header().Get("X-Hub-Session"), second.Header().Get("X-Hub-Session"); a == "" || a != b {
			t.Fatalf("sessions %q / %q", a, b)
		}
		if n := len(r.vendorCalls(t)); n != 1 {
			t.Fatalf("%d vendor calls, want 1 (one SMS)", n)
		}
	})

	t.Run("vendor down during verify: retryable 503, the session survives, the same key retries", func(t *testing.T) {
		r.publish(t, 1)
		sid := r.sendOTP(t, "otp-send-down")
		r.vendor(t, otpVerifyPath, 503, `{"error": "maintenance"}`)
		rec, body := r.call(t, otpVerify, "otp-verify-down", sid, verifyBody)
		if e := errorOf(body); rec.Code != 503 || e["code"] != "UPSTREAM_UNAVAILABLE" || e["retryable"] != true {
			t.Fatalf("vendor down: %d %s", rec.Code, rec.Body)
		}
		r.vendor(t, otpVerifyPath, 200, vendorVerified)
		rec, body = r.call(t, otpVerify, "otp-verify-down", sid, verifyBody)
		if rec.Code != 200 || body["status"] != "verified" || rec.Header().Get("X-Hub-Idempotent-Replay") != "" {
			t.Fatalf("retry after recovery: %d %s", rec.Code, rec.Body)
		}
	})

	t.Run("caller mistakes are refused at the edge without a vendor call", func(t *testing.T) {
		r.publish(t, 1)
		r.vendor(t, bankPath, 200, `{"result": "VALID"}`)
		for name, c := range map[string]struct {
			path, key, body, code string
			status                int
			pointer               string
		}{
			"no Idempotency-Key": {bankCheck, "", bankBody, "IDEMPOTENCY_KEY_REQUIRED", 400, ""},
			"malformed IFSC":     {bankCheck, "bank-verify-bad-ifsc", `{"account_number": "50100123456789", "ifsc": "demo123"}`, "VALIDATION_FAILED", 400, "/ifsc"},
			"malformed mobile":   {otpSend, "otp-send-bad-mobile", `{"mobile": "98123"}`, "VALIDATION_FAILED", 400, "/mobile"},
			"unknown operation":  {"/v1/integrations/int_demo_otp/invoke/verify_account", "otp-unknown-op", verifyBody, "OPERATION_NOT_FOUND", 404, ""},
			"no operation named": {"/v1/integrations/int_demo_otp/invoke", "otp-no-op", sendBody, "OPERATION_NOT_FOUND", 404, ""},
			"verify without otp": {otpVerify, "otp-verify-no-otp", sendBody, "VALIDATION_FAILED", 400, ""},
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
		}
		if n := len(r.vendorCalls(t)); n != 0 {
			t.Fatalf("%d vendor calls for refused requests", n)
		}
	})

	t.Run("invalid bank account is a canonical answer, not an error", func(t *testing.T) {
		r.publish(t, 1)
		r.vendor(t, bankPath, 200, `{"result": "INVALID", "ref": "dk_ref_0004"}`)
		rec, body := r.call(t, bankCheck, "bank-verify-invalid", "", `{"account_number": "50100000000000", "ifsc": "DEMO0001234"}`)
		if rec.Code != 200 || body["account_status"] != "invalid" || body["name_match"] != nil {
			t.Fatalf("invalid account: %d %s", rec.Code, rec.Body)
		}
	})
}
