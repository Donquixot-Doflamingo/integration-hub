// Package hubclient is the Go SDK for Hub callers, written against the caller contract
// contracts/openapi/hub-public.v1.yaml (DESIGN §4).
//
// A caller sends an integration ID and a canonical body and gets a canonical response back. The
// vendor, account and version behind an integration are never visible through this package.
//
// # Outcomes
//
// Invoke returns a sealed Outcome: *Result for a completed request (HTTP 200) or *Queued for a
// request accepted for later delivery (HTTP 202). A 202 is never a success (DESIGN §4.5): the
// caller must type-switch and poll GetRequest or wait for a callback.
//
//	c, err := hubclient.New("https://hub.prod.internal", os.Getenv("HUB_API_KEY"))
//	if err != nil { ... }
//	out, err := c.Invoke(ctx, "int_evt_brandA", body, hubclient.WithIdempotencyKey("order-8821-created"))
//	var he *hubclient.Error
//	switch {
//	case errors.As(err, &he):
//		// he.Code, he.Retryable, he.RetryAfter, he.RequestID
//	case err != nil:
//		// transport failure or cancelled context
//	}
//	switch o := out.(type) {
//	case *hubclient.Result:
//		_ = o.Decode(&resp) // done
//	case *hubclient.Queued:
//		// not done: remember o.RequestID and poll c.GetRequest
//	}
//
// # Errors
//
// Every Hub error response becomes an *Error decoded from the canonical envelope, with a Code
// from the contract's enum, Retryable and RetryAfter. Retry only when IsRetryable(err) is true,
// with the same Idempotency-Key, after RetryAfter. This package does not retry by itself.
//
// # Security
//
// The API key is sent only in the Authorization header. It is never part of an error, a
// formatted Client or a slog value. The debug-only X-Hub-Integration-Version header is ignored.
//
// The package imports nothing from internal/: it is public and must build on its own.
package hubclient
