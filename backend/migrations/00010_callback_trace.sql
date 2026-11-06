-- Trace context of the callback's terminal transition (HUB-231, DESIGN §16.4). The dispatcher's
-- hub.callback.deliver span links to it, so a callback is reachable from the request's trace.
-- W3C traceparent only (version-traceid-spanid-flags): ids, never a body or a header value of
-- the caller. NULL when the transition ran without a span (scheduler expiry, tracing off).
-- Owned by internal/callbacks. Schema-qualified.

-- +goose Up
ALTER TABLE hub.callback_deliveries ADD COLUMN traceparent text
    CHECK (traceparent IS NULL OR traceparent ~ '^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$');

-- +goose Down
ALTER TABLE hub.callback_deliveries DROP COLUMN IF EXISTS traceparent;
