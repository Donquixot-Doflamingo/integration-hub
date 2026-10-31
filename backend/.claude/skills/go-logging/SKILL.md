---
name: go-logging
description: "Structured logging with log/slog in the Integration Hub - the JSON field schema (request_id, tenant, integration_id, integration_version, trace_id, span_id, role, component, attempt, error_code), the redacting handler that masks secret values by value (DESIGN 9), level policy, never logging bodies, logging errors once at the boundary, sampling hot-path debug logs, OTel trace correlation and tests proving no secret leaks. Use whenever you add or change a log line, create a logger, touch internal/telemetry, handle an error at a boundary, or anything near credentials, headers or request bodies."
---

# Logging in the Hub

Package: `internal/telemetry` owns the handler chain. Everyone else receives a `*slog.Logger` by constructor and calls the `...Context` methods. Handler code: [references/handler.md](references/handler.md).

## Hard rules
1. **Never log secrets.** Credentials enter only via `creds.*` in header/query/auth targets (DESIGN §0, §9). The redacting handler replaces every known secret **value** with `***`, wherever it appears (message, attrs, nested groups, error strings). That is the safety net, not permission to log credential-bearing structs.
2. **Never log request or response bodies**, vendor payloads, headers maps, or canonical inputs — not even at debug. Payloads belong in the **audit trail** (`internal/audit`), which applies the integration's payload policy (full/masked/sealed/metadata_only, §13.2). Logs are for operators, audit is for the record.
3. **Log an error once, at the boundary** (HTTP handler/recover middleware, worker loop iteration, scheduler job, `main`). Inner code wraps and returns. Logging *and* returning = duplicate lines.
4. Always use the context variants (`log.InfoContext(ctx, ...)`) so request fields and trace ids attach. Lint: `sloglint` with `context: all`.
5. Typed attrs (`slog.String`, `slog.Int`, `slog.Any("error", err)`), snake_case keys from the schema below. No ad-hoc keys like `reqId`.
6. No `fmt.Print*` / `log.*` (forbidigo).

## Field schema (JSON)
| Key | Source | Notes |
| --- | --- | --- |
| `time`, `level`, `msg` | slog | RFC 3339 UTC; `msg` is a constant string, not formatted with values |
| `service` | root logger | `hub`, `hubctl`, `mockvendor`, `hubmcp` |
| `role` | root logger | `gateway`, `worker`, `scheduler`, `admin` |
| `component` | `log.With` in constructor | package/subsystem: `edge`, `delivery.worker`, `upstream.breaker` |
| `request_id` | ctx fields | `req_…` |
| `tenant` | ctx fields | tenant id, never the API key or its hash |
| `integration_id`, `integration_version` | ctx fields | version as int |
| `attempt` | ctx fields | async attempt number |
| `trace_id`, `span_id` | OTel span in ctx | hex; only when the span context is valid |
| `error_code` | at boundary | canonical code (§4.4) when known |
| `error` | at boundary | `err.Error()` (redacted) |
| `vendor`, `account`, `endpoint` | optional | internal only; fine in logs, never in caller responses |
| `snapshot_version`, `duration_ms` | optional | |

Request-scoped fields travel in the context (set once by edge middleware / the worker per envelope), and the handler adds them — callers don't repeat them:
```go
ctx = telemetry.WithRequestFields(ctx, telemetry.RequestFields{
	RequestID: reqID, Tenant: tenant, IntegrationID: integrationID, IntegrationVersion: res.Version,
})
log.InfoContext(ctx, "request parked", slog.String("account", res.Account), slog.String("reason", "circuit_open"))
```

## Handler chain
`enrich (ctx fields + trace ids) → redact (by value + key denylist) → slog.JSONHandler(os.Stdout)`, level from a `slog.LevelVar` so it can be changed at runtime through the internal port.

The secret set is refreshed whenever credentials are loaded or rotated (warm on snapshot load, §9) — **before** traffic is served — and includes `active` and `next` slots, tenant webhook signing secrets, DSN passwords, and OAuth tokens as they are minted. Values shorter than 6 characters are not registered (they would mask random text); such credentials are a config error worth flagging.

## Level policy
| Level | Use for | Examples |
| --- | --- | --- |
| Error | Hub fault or a human must act | `TRANSFORM_FAILED`, `UPSTREAM_AUTH_FAILED`, invariant violated (illegal transition), panic recovered, DB unavailable at startup |
| Warn | Degraded but handled | snapshot fetch failed (kept last good), Redis down (breaker local-only / idempotency fail-open), audit events dropped, retry budget exhausted, lost CAS race more than expected |
| Info | Lifecycle and rare state changes | start/stop, config loaded, snapshot swapped (version), leader acquired/lost, breaker transitions, drain started/finished |
| Debug | Per-request detail; off in prod by default; sampled | resolution result, stage timings, retry scheduling |

No Info line per successful request: metrics and audit already cover it. Vendor 4xx/5xx are vendor outcomes, recorded in metrics and audit, not logged per request (breaker transitions are logged).

## Hot-path debug logs
```go
var retrySample = rate.Sometimes{First: 5, Interval: time.Second} // golang.org/x/time/rate; field of the struct, not a global

if w.log.Enabled(ctx, slog.LevelDebug) {
	w.retrySample.Do(func() {
		w.log.DebugContext(ctx, "retry scheduled", slog.Int("attempt", env.Attempt), slog.Duration("delay", d))
	})
}
```
Check `Enabled` first so disabled logs cost no attribute allocation.

## Errors at the boundary
```go
func (s *Server) writeError(ctx context.Context, w http.ResponseWriter, err error) {
	ce := toCanonical(err) // edge-only mapping (go-idioms)
	if ce.Code == core.CodeTransformFailed || !isCanonical(err) {
		s.log.ErrorContext(ctx, "request failed", slog.String("error_code", string(ce.Code)), slog.Any("error", err))
	}
	writeEnvelope(w, ce)
}
```

## Trace correlation
The enrich handler reads `trace.SpanContextFromContext(ctx)`; if `IsValid()`, adds `trace_id` = `TraceID().String()` and `span_id` = `SpanID().String()`. Log lines then join to traces in the backend; dashboards link by `trace_id`.

## Tests (required)
- Redaction unit tests: secret in message, in a string attr, inside a group, inside an error, inside a `With` attr, in `[]byte`; key denylist (`authorization`, `password`, `passcode`, `api_key`, `secret`, `token`, `cookie`).
- **Canary test**: e2e flow with a canary credential `fixture-canary-<random>`; capture all log output into a buffer; `assert.NotContains(t, buf.String(), canary)`. Same canary must not appear in stored audit events (M3 acceptance).
- Schema test: decode a log line and assert required keys present with snake_case.

## Anti-patterns
- `log.Info("calling vendor", "req", vendorReq)` — logs headers with credentials and the body.
- `slog.Any("account", acct)` with a struct that has credential fields. Implement `LogValue()` on such types to emit only ids.
- `fmt.Sprintf` in `msg`. Put values in attrs.
- Logging the tenant API key, its hash, `Authorization`, `X-Hub-Signature` or webhook secrets.
- Creating loggers per request with `slog.New` (allocations); use ctx fields.
