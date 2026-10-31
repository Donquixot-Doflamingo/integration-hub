// Package idempotency is the Redis-backed Idempotency-Key store of the gateway (DESIGN §4.2,
// §5.1 step 4, §6.3; feature F-10). It is layer L3 (TECH_SPEC §4.1): it imports only core and
// telemetry, and it never decides an HTTP status. The edge maps every Outcome and error to the
// caller contract.
//
// # Scope and lifetime
//
// A record is scoped to tenant + integration + key (contract parameter IdempotencyKey). The
// Redis key is Options.Prefix + hex(SHA-256(tenant NUL integration NUL key)), so raw keys
// (caller-chosen, possibly business ids) never reach Redis. Each record also holds the body
// hash of the request that claimed it (BodyHash: operation + body), so reusing a key with a
// different body or operation is a Conflict.
//
//   - Begin claims a free key for LeaseTTL (default 5 min): the in-progress marker of a crashed
//     gateway frees itself instead of pinning the key for a day.
//   - Complete stores the replayable response and sets the record to expire TTL (default 24 h)
//     after the first claim, measured on the Redis clock (TIME), not on any gateway clock.
//   - Release deletes the record so the next request with the key runs fresh.
//
// All three are single-key Lua scripts, so they are atomic and cluster-safe, and each is one
// Redis round trip. Complete and Release are guarded by the request id that won Begin: a request
// whose lease expired and whose key was claimed by a newer request gets ErrNotOwner and changes
// nothing.
//
// # What is replayable
//
// Only 200 (canonical response) and 202 (Accepted body) responses are stored; the contract's
// replay is "200/202 with X-Hub-Idempotent-Replay: true" and DESIGN §6.3 says idempotency only
// short-circuits completed requests: a retry of a failed request is a new attempt that resolves
// fresh (F-10 rule 5, US-1003). Complete refuses any other status with ErrNotReplayable. The edge
// therefore calls Release for every outcome that is not a stored 200/202: canonical error
// envelopes (4xx and 5xx, including UPSTREAM_TIMEOUT where the vendor may have acted; vendor-side
// idempotency keys, DESIGN §11.3, cover that case), panics and shutdown. An async or wait request
// that got its 202 stored and later fails terminally in the worker should also be released by
// request id so that a retry resolves fresh.
//
// Stored headers are an allowlist of the contract response headers that belong to the result:
// Content-Type, Location (202), X-Hub-Served-By and X-Hub-Session. X-Hub-Request-Id is carried
// separately as Outcome.RequestID; the debug-only X-Hub-Integration-Version, credentials, cookies
// and every other header are dropped. Bodies are stored exactly as they are replayed (the
// canonical, post-transform response, which never carries credentials). Status, headers and body
// together must fit in Options.MaxBytes (default 1 MiB); a larger response is refused with
// ErrTooLarge and nothing is written.
//
// # Redis unavailable
//
// Any failure to get an answer from Redis (connection refused, timeout, cancelled context, an
// unexpected reply) is returned as a *UnavailableError, which matches errors.Is(err,
// ErrUnavailable) and unwraps to the client error (so errors.Is(err, context.Canceled) still
// works). When Begin returns it, nothing is known about the key: the edge fails closed (500
// INTERNAL_ERROR, retryable) for integrations with moves_money_or_quota and proceeds without
// deduplication otherwise; a request that proceeds that way must not call Complete or Release.
// Each round trip is bounded by Options.Timeout (default 50 ms, TECH_SPEC §12.2) on top of the
// request context. The client must be built to fail fast (TECH_SPEC §8.1): ContextTimeoutEnabled
// so that timeout reaches the socket, and DialerRetries 1, because go-redis otherwise retries a
// refused dial five times with 100 ms backoff.
package idempotency
