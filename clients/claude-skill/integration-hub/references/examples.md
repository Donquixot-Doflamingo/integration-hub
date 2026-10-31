# Example: `events.track@v1` end to end

Scenario: `orders-service` tracks an "order created" engagement event via integration
`int_evt_brandA` (dev config). Which engagement vendor and account receive it is the Hub's business.

The body below follows the dev `events.track@v1` schema; always confirm against
`GET /v1/capabilities/events.track@v1` in your environment.

## 1. Discover
```bash
curl -s -H "Authorization: Bearer $HUB_API_KEY" "$HUB_BASE_URL/v1/integrations" | jq '.'
curl -s -H "Authorization: Bearer $HUB_API_KEY" "$HUB_BASE_URL/v1/capabilities/events.track@v1" | jq '.'
```

## 2. Canonical body (`body.json`)
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

{ "status": "ok", ... }          # canonical response fields per the capability response schema
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
	case resp.StatusCode == http.StatusOK:
		return nil // done; decode the canonical response if you need fields
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
In real services prefer the SDK, which models this as typed results. With `pkg/hubclient`
(module `github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient`) the call returns
either a `Result` (200) or a `Queued` (202) and a typed error carrying the canonical code; check
exact function and option names with `go doc` before writing code. Never treat `Queued` as success.
For fire-and-forget event tracking, a queued result is usually fine to accept, but record its
request id rather than reporting "sent".
