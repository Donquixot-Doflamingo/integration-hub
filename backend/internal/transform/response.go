package transform

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
)

// defaultMessages are caller-facing messages per code. They never name the vendor (D12).
var defaultMessages = map[core.Code]string{
	core.CodeUpstreamAuthFailed:  "The upstream rejected the Hub's credentials",
	core.CodeUpstreamRateLimited: "The upstream is rate limiting requests",
	core.CodeUpstreamUnavailable: "The upstream is unavailable",
	core.CodeUpstreamRejected:    "The upstream rejected the request",
	core.CodeUpstreamTimeout:     "The upstream did not answer in time",
}

var errNotResponse = errors.New("not a response transformer")

// MapResponse maps a vendor response to the canonical response or a canonical error (DESIGN §7.4).
// Error rules run first, in order, and the first match wins. A 4xx/5xx that no rule matches is
// classified by status, so a vendor error is never mapped as success. A mapped response must
// match the capability response schema. Any failure of the transformer itself is an *EvalError
// (TRANSFORM_FAILED). The returned CanonicalError has no RequestID; the caller sets it.
func (p *Program) MapResponse(resp core.VendorResponse, in Input) (any, *core.CanonicalError, error) {
	out, _, ce, err := p.MapResponseSession(resp, in)
	return out, ce, err
}

// MaxSessionBytes bounds the JSON size of the session data one response maps (it is stored per
// session in Postgres; DESIGN §3.1 keeps only what later steps need, such as a vendor txn id).
const MaxSessionBytes = 16 << 10

// MapResponseSession is MapResponse plus the vendor session data of a multi-step operation: the
// transformer's session: section, evaluated only on success (never for a mapped or classified
// error). session is nil when the transformer has no session: section or every field mapped to
// nil (nothing to keep). The session mapping is held to the same TimeBudget and to MaxSessionBytes.
func (p *Program) MapResponseSession(resp core.VendorResponse, in Input) (out any, session map[string]any, ce *core.CanonicalError, err error) {
	out, ce, err = p.mapResponse(resp, in)
	if err != nil || ce != nil || p.session == nil {
		return out, nil, ce, err
	}
	start := time.Now()
	v, err := p.eval(p.session, p.env(in, resEnv(resp)))
	if err != nil {
		return nil, nil, nil, err
	}
	session, _ = v.(map[string]any) // the section is an object node (parse)
	if n, over := estimateSize(session, MaxSessionBytes); over {
		return nil, nil, nil, &EvalError{Transformer: p.ref, Path: "session", Err: fmt.Errorf("session data is larger than %d bytes (at least %d)", MaxSessionBytes, n)}
	}
	if err := p.checkTime("session", start); err != nil {
		return nil, nil, nil, err
	}
	if len(session) == 0 {
		session = nil
	}
	return out, session, nil, nil
}

func (p *Program) mapResponse(resp core.VendorResponse, in Input) (any, *core.CanonicalError, error) {
	if p.kind != "response" {
		return nil, nil, &EvalError{Transformer: p.ref, Path: "response", Err: errNotResponse}
	}
	start := time.Now()
	env := p.env(in, resEnv(resp))
	status := resp.Status
	for _, r := range p.errors {
		hit, err := p.eval(r.when, env)
		if err != nil {
			return nil, nil, err
		}
		matched, ok := hit.(bool)
		if !ok {
			return nil, nil, &EvalError{Transformer: p.ref, Path: r.when.path, Expr: r.when.src, Err: fmt.Errorf("must evaluate to true or false, got %T", hit)}
		}
		if !matched {
			continue
		}
		ce := &core.CanonicalError{Code: core.Code(r.code), Retryable: r.retryable, Message: defaultMessages[core.Code(r.code)], Upstream: &core.Upstream{Status: &status}}
		if r.retryAfterMS != nil {
			v, err := p.eval(r.retryAfterMS, env)
			if err != nil {
				return nil, nil, err
			}
			ms, err := asInt("retry_after_ms", v)
			if err != nil || ms < 0 {
				return nil, nil, &EvalError{Transformer: p.ref, Path: r.retryAfterMS.path, Expr: r.retryAfterMS.src, Err: errors.New("must evaluate to a number of milliseconds >= 0")}
			}
			ce.RetryAfterMS = &ms
		}
		if r.message != nil {
			v, err := p.eval(r.message, env)
			if err != nil {
				return nil, nil, err
			}
			if msg, ok := v.(string); ok && msg != "" {
				ce.Message = truncateRunes(msg, 2048)
			}
		}
		return nil, ce, nil
	}
	if status >= 400 {
		return nil, classify(status), nil
	}
	if p.response == nil {
		return nil, nil, nil
	}
	out, err := p.eval(p.response, env)
	if err != nil {
		return nil, nil, err
	}
	if err := p.checkOutput("response", out); err != nil {
		return nil, nil, err
	}
	if err := p.checkTime("response", start); err != nil {
		return nil, nil, err
	}
	if p.validateResponse != nil {
		if err := p.validateResponse(out); err != nil {
			return nil, nil, &EvalError{Transformer: p.ref, Path: "response", Err: fmt.Errorf("mapped response does not match the capability response schema: %s", firstLine(err.Error()))}
		}
	}
	return out, nil, nil
}

// classify is the fallback for vendor errors no rule matched (DESIGN §10.2).
func classify(status int) *core.CanonicalError {
	code, retryable := core.CodeUpstreamRejected, false
	switch {
	case status == 401 || status == 403:
		code = core.CodeUpstreamAuthFailed
	case status == 429:
		code, retryable = core.CodeUpstreamRateLimited, true
	case status >= 500:
		code, retryable = core.CodeUpstreamUnavailable, true
	}
	s := status
	return &core.CanonicalError{Code: code, Retryable: retryable, Message: defaultMessages[code], Upstream: &core.Upstream{Status: &s}}
}

// resEnv is the res variable: status, lower-cased headers (first value), body and text.
// body is the parsed JSON object or array, or nil; text is a non-JSON body (an HTML error page,
// plain text). Keeping text out of body makes res.body.error nil, not an error, on such pages.
func resEnv(resp core.VendorResponse) map[string]any {
	headers := make(map[string]any, len(resp.Headers))
	for k, v := range resp.Headers {
		if len(v) > 0 {
			headers[strings.ToLower(k)] = v[0]
		}
	}
	env := map[string]any{"status": resp.Status, "headers": headers, "body": nil, "text": nil}
	switch b := resp.Body.(type) {
	case map[string]any, []any:
		env["body"] = b
	case string:
		env["text"] = b
	case nil:
	default:
		env["body"] = b // a JSON scalar
	}
	return env
}

// header(res, name) reads a response header case-insensitively; nil when absent.
func fnHeader(params ...any) (any, error) {
	if len(params) != 2 {
		return nil, argErr("header", "(res, name)", params)
	}
	res, err := asMap("header", params[0])
	if err != nil {
		return nil, err
	}
	name, err := asString("header", params[1])
	if err != nil {
		return nil, err
	}
	headers, _ := res["headers"].(map[string]any)
	return headers[strings.ToLower(name)], nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
