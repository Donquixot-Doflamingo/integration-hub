# Example: event tracking end to end

Scenario: `orders-service` tracks an "order created" engagement event via integration
`int_evt_brandA` (dev config). Which engagement vendor and account receive it is the Hub's business.

The body below follows the dev integration's request schema; always confirm against
`GET /v1/integrations/int_evt_brandA` (`operations[0].request_schema.body`) in your environment.

## 1. Discover
```bash
curl -s -H "Authorization: Bearer $HUB_API_KEY" "$HUB_BASE_URL/v1/integrations" | jq '.items[].integration_id'
curl -s -H "Authorization: Bearer $HUB_API_KEY" "$HUB_BASE_URL/v1/integrations/int_evt_brandA" \
  | jq '.operations[] | {name, idempotent, required: .request_schema.body.required}'
# {"name": "default", "idempotent": true, "required": ["user", "event"]}
```

## 2. Body (`body.json`): `user.id` and `event.name` required, the rest optional
```json
{
  "user":    { "id": "u_10293" },
  "event":   {
    "name": "order_created",
    "timestamp": "2026-10-31T10:15:00Z",
    "properties": { "order_id": "8821", "amount_minor": 125000, "currency": "INR" }
  },
  "context": { "campaign": "diwali" }
}
```

## 3. Dry-run
```bash
curl -s -X POST "$HUB_BASE_URL/v1/integrations/int_evt_brandA/dry-run" \
  -H "Authorization: Bearer $HUB_API_KEY" -H "Content-Type: application/json" \
  -d @body.json | jq '.'
```
A `VALIDATION_FAILED` here lists JSON pointers (e.g. `/event/name`); fix and repeat.

## 4. Invoke with an idempotency key
```bash
curl -s -i -X POST "$HUB_BASE_URL/v1/integrations/int_evt_brandA/invoke" \
  -H "Authorization: Bearer $HUB_API_KEY" -H "Content-Type: application/json" \
  -H "Idempotency-Key: order-8821-created" \
  -d @body.json
```
Possible outcomes:
```http
HTTP/1.1 200 OK
X-Hub-Request-Id: req_01J9ABCD

{ "status": "ok", ... }          # the integration's output (its response transformer decides the fields)
```
```http
HTTP/1.1 202 Accepted
X-Hub-Request-Id: req_01J9ABCE

{ "request_id": "req_01J9ABCE", "status": "queued", "reason": "circuit_open",
  "poll": "/v1/requests/req_01J9ABCE" }
```
```http
HTTP/1.1 400 Bad Request

{ "error": { "code": "VALIDATION_FAILED", "message": "...", "retryable": false,
             "request_id": "req_01J9ABCF" } }
```
Sending the same request again with the same `Idempotency-Key` within 24 h returns the stored result
with `X-Hub-Idempotent-Replay: true`; no duplicate event.

A pass-through integration (no response transformer) answers with the third party's own body:
```http
HTTP/1.1 200 OK
X-Hub-Request-Id: req_01J9ABCG
X-Hub-Pass-Through: true
Content-Type: application/json

{ ...the third party's own JSON, unchanged... }
```
A 4xx/5xx with `X-Hub-Pass-Through: true` is the third party's refusal, not the Hub's envelope. Check
the header before you parse `error`.

## 5. Poll a 202
```bash
curl -s -H "Authorization: Bearer $HUB_API_KEY" "$HUB_BASE_URL/v1/requests/req_01J9ABCE" | jq '.status'
# accepted | in_flight | retry_scheduled | parked -> keep polling with backoff
# succeeded -> read the result; failed | dead_lettered | expired -> handle as failure
```

## 6. Go (plain `net/http`)
```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

type hubError struct {
	Error struct {
		Code         string `json:"code"`
		Message      string `json:"message"`
		Retryable    bool   `json:"retryable"`
		RetryAfterMs int    `json:"retry_after_ms"`
		RequestID    string `json:"request_id"`
	} `json:"error"`
}

type accepted struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	Poll      string `json:"poll"`
}

func trackOrderCreated(ctx context.Context, orderID, userID string) error {
	body, _ := json.Marshal(map[string]any{
		"user":  map[string]any{"id": userID},
		"event": map[string]any{
			"name":       "order_created",
			"timestamp":  time.Now().UTC().Format(time.RFC3339),
			"properties": map[string]any{"order_id": orderID},
		},
	})
	url := os.Getenv("HUB_BASE_URL") + "/v1/integrations/int_evt_brandA/invoke"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+os.Getenv("HUB_API_KEY"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "order-"+orderID+"-created") // stable per business event

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err // network error: safe to retry with the same key
	}
	defer resp.Body.Close()
	reqID := resp.Header.Get("X-Hub-Request-Id")

	switch {
	case resp.Header.Get("X-Hub-Pass-Through") == "true":
		// the third party's own status and body (no response transformer); 2xx means done
		if resp.StatusCode < 300 {
			return nil
		}
		return fmt.Errorf("upstream refused with HTTP %d (request_id=%s)", resp.StatusCode, reqID)
	case resp.StatusCode == http.StatusOK:
		return nil // done; decode the integration's output if you need fields
	case resp.StatusCode == http.StatusAccepted:
		var a accepted
		if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
			return err
		}
		// NOT done: persist a.RequestID and poll a.Poll, or rely on the callback.
		return fmt.Errorf("queued: %s (%s)", a.RequestID, a.Status)
	default:
		var e hubError
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("hub %s (retryable=%v, retry_after_ms=%d, request_id=%s)",
			e.Error.Code, e.Error.Retryable, e.Error.RetryAfterMs, reqID)
	}
}
```
In real services prefer the SDK (`pkg/hubclient`), which returns `*hubclient.Result` (200, `PassThrough`
set for a third party's own 2xx) or `*hubclient.Queued` (202), and a typed `*hubclient.Error` or
`*hubclient.PassThroughError`: see the SKILL.md "Go SDK" section. Never
treat `Queued` as success. For fire-and-forget event tracking, a queued result is usually fine to
accept, but record its request id rather than reporting "sent".

# Example: two-step OTP (send, then verify)

Scenario: a loan onboarding backend verifies a mobile number. One integration, `int_demo_otp`
(from the runnable example `examples/otp-account-flow/` in the Hub repo), with operations `send_otp`
(body `mobile`) and `verify_otp` (body `mobile`, `otp`). Aadhaar OTP e-KYC has the same
shape (`examples/aadhaar-okyc/`: `int_aadhaar_okyc`, `send_otp` with the Aadhaar number and consent,
then `verify_otp` with the OTP). Always read the real operation names and schemas from discovery.
Use fake numbers: every `send_otp` sends a real SMS in a real environment, so ask the user first.

## 1. Discover the operations
```bash
curl -s -H "Authorization: Bearer $HUB_API_KEY" "$HUB_BASE_URL/v1/integrations/int_demo_otp" \
  | jq '{interactive, moves_money_or_quota, ops: [.operations[] | {name, required: .request_schema.body.required}]}'
```

## 2. Step 1: send the OTP and keep the session id
```bash
curl -s -i -X POST "$HUB_BASE_URL/v1/integrations/int_demo_otp/invoke/send_otp" \
  -H "Authorization: Bearer $HUB_API_KEY" -H "Content-Type: application/json" \
  -H "Idempotency-Key: otp-send-attempt-77" \
  -d '{"mobile": "+919812345678"}'
```
```http
HTTP/1.1 200 OK
X-Hub-Request-Id: req_01M6S0SYJ7KK3N8BBET4GQDSFD
X-Hub-Session: ses_01M6S0SYJAPG6N5XSV02NPW0ED

{"expires_in_s": 300, "status": "otp_sent", "vendor_ref": "dk_ref_0001"}
```

## 3. Step 2: verify inside the session
```bash
curl -s -i -X POST "$HUB_BASE_URL/v1/integrations/int_demo_otp/invoke/verify_otp" \
  -H "Authorization: Bearer $HUB_API_KEY" -H "Content-Type: application/json" \
  -H "Idempotency-Key: otp-verify-attempt-77-1" \
  -H "X-Hub-Session: ses_01M6S0SYJAPG6N5XSV02NPW0ED" \
  -d '{"mobile": "+919812345678", "otp": "482913"}'
```
```http
HTTP/1.1 200 OK

{"status": "verified", "vendor_ref": "dk_ref_0002"}
```
Wrong OTP: `422 {"error": {"code": "UPSTREAM_REJECTED", "message": "The OTP does not match", "retryable": false, ...}}`
(retry with the same session and a new key). Expired session: `404 SESSION_NOT_FOUND` (start again
at step 1). The SKILL.md table "Two-step flows" lists every case.

To check a mapping first, a developer key with scope `dry_run` can dry-run one operation:
`POST /v1/integrations/int_demo_otp/dry-run?operation=verify_otp` with the same `X-Hub-Session` header.

## 4. Go with `hubclient`
```go
// sendOTP is step 1. attemptID is your own id for this onboarding attempt.
func sendOTP(ctx context.Context, hc *hubclient.Client, attemptID, mobile string) (session string, err error) {
	out, err := hc.Invoke(ctx, "int_demo_otp", map[string]any{"mobile": mobile},
		hubclient.WithOperation("send_otp"),
		hubclient.WithIdempotencyKey("otp-send-"+attemptID)) // same attempt, same key: one SMS
	if err != nil {
		return "", err // *hubclient.Error: check Code / Retryable
	}
	res, ok := out.(*hubclient.Result)
	if !ok {
		return "", errors.New("unexpected queued OTP send") // OTP integrations are sync
	}
	return res.Session, nil // persist with the attempt, server side
}

// verifyOTP is step 2. try counts the user's attempts so each gets its own key.
func verifyOTP(ctx context.Context, hc *hubclient.Client, attemptID string, try int, session, mobile, otp string) (bool, error) {
	_, err := hc.Invoke(ctx, "int_demo_otp", map[string]any{"mobile": mobile, "otp": otp},
		hubclient.WithOperation("verify_otp"),
		hubclient.WithSession(session),
		hubclient.WithIdempotencyKey(fmt.Sprintf("otp-verify-%s-%d", attemptID, try)))
	var hubErr *hubclient.Error
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &hubErr) && hubErr.Code == hubclient.CodeUpstreamRejected:
		return false, nil // wrong or expired OTP: show hubErr.Message, let the user retry or resend
	case errors.As(err, &hubErr) && hubErr.Code == hubclient.CodeSessionNotFound:
		return false, errRestartFromSend
	default:
		return false, err // retryable errors: retry with the same session and the same key
	}
}
```
