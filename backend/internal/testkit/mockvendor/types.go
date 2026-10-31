// Package mockvendor is the programmable fake vendor (DESIGN §19 M0) described by
// contracts/openapi/mockvendor.v1.yaml. cmd/mockvendor serves it standalone; tests start it
// in-process with Start. It must behave like an outside vendor, so it imports nothing from the
// Hub except core id helpers.
package mockvendor

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Types mirror the components of contracts/openapi/mockvendor.v1.yaml. Field names and JSON
// tags must stay in sync with the contract (the contract test in HUB-031 checks responses).

// RouteMatch selects requests: exact method (or "*"), a path glob ("*" = one segment,
// "**" = any number of segments) and optional exact header values.
type RouteMatch struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Latency delays the response.
type Latency struct {
	Mode    string `json:"mode"` // fixed | random
	FixedMS int    `json:"fixed_ms,omitempty"`
	MinMS   int    `json:"min_ms,omitempty"`
	MaxMS   int    `json:"max_ms,omitempty"`
}

// ErrorRatio answers with an error instead of the normal response, with probability Ratio.
type ErrorRatio struct {
	Ratio   float64           `json:"ratio"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
	Seed    *int64            `json:"seed,omitempty"`
}

// RateLimit answers 429 with Retry-After for every Nth request or with probability Ratio,
// after letting the first Burst requests through.
type RateLimit struct {
	EveryNth     int             `json:"every_nth,omitempty"`
	Ratio        float64         `json:"ratio,omitempty"`
	Burst        int             `json:"burst,omitempty"`
	RetryAfterMS int             `json:"retry_after_ms"`
	Body         json.RawMessage `json:"body,omitempty"`
}

// Down simulates a dead host: reset closes the TCP connection without a response, refuse
// closes the vendor listener (connection refused) until the route is removed, hang never answers.
type Down struct {
	Mode string `json:"mode"` // reset | refuse | hang
}

// SlowDrip sends headers immediately, then the body in chunks.
type SlowDrip struct {
	ChunkBytes int `json:"chunk_bytes"`
	IntervalMS int `json:"interval_ms"`
}

// Malformed corrupts the response.
type Malformed struct {
	Kind string `json:"kind"` // truncated_json | invalid_json | wrong_content_type | empty_body | html_error_page
}

// Behaviour is how a matched request is answered.
type Behaviour struct {
	Status     int               `json:"status,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       json.RawMessage   `json:"body,omitempty"`
	BodyRaw    *string           `json:"body_raw,omitempty"`
	Latency    *Latency          `json:"latency,omitempty"`
	ErrorRatio *ErrorRatio       `json:"error_ratio,omitempty"`
	RateLimit  *RateLimit        `json:"rate_limit,omitempty"`
	Down       *Down             `json:"down,omitempty"`
	SlowDrip   *SlowDrip         `json:"slow_drip,omitempty"`
	Malformed  *Malformed        `json:"malformed,omitempty"`
	Echo       bool              `json:"echo,omitempty"`
}

// RouteSpec is the body of setRouteBehaviour.
type RouteSpec struct {
	Match     RouteMatch `json:"match"`
	Priority  int        `json:"priority"`
	Times     *int       `json:"times,omitempty"`
	Behaviour Behaviour  `json:"behaviour"`
}

// Route is a stored route with its counters.
type Route struct {
	RouteID   string     `json:"route_id"`
	Match     RouteMatch `json:"match"`
	Priority  int        `json:"priority"`
	Behaviour Behaviour  `json:"behaviour"`
	Hits      int        `json:"hits"`
	Remaining *int       `json:"remaining"`
	CreatedAt time.Time  `json:"created_at"`
}

// RecordedRequest is one request received on the vendor surface.
type RecordedRequest struct {
	ID             string              `json:"id"`
	ReceivedAt     time.Time           `json:"received_at"`
	RouteID        *string             `json:"route_id"`
	Method         string              `json:"method"`
	Path           string              `json:"path"`
	Query          map[string][]string `json:"query"`
	Headers        map[string][]string `json:"headers"`
	Body           json.RawMessage     `json:"body"`
	BodyRaw        *string             `json:"body_raw"`
	BodyTruncated  bool                `json:"body_truncated"`
	ResponseStatus *int                `json:"response_status"`
	Outcome        string              `json:"outcome"` // responded | reset | refused | hung | rate_limited | errored
}

// ResetRequest is the optional body of resetMock.
type ResetRequest struct {
	Routes     *bool `json:"routes,omitempty"`
	Recordings *bool `json:"recordings,omitempty"`
}

// ResetResult reports what resetMock removed.
type ResetResult struct {
	RoutesRemoved     int `json:"routes_removed"`
	RecordingsRemoved int `json:"recordings_removed"`
}

// Signature signs an inbound webhook.
type Signature struct {
	Scheme            string `json:"scheme"` // hmac_sha256 | basic
	Secret            string `json:"secret"`
	Header            string `json:"header"`
	Format            string `json:"format,omitempty"` // default "t={ts},v1={hex}"
	TimestampOffsetMS int64  `json:"timestamp_offset_ms,omitempty"`
}

// WebhookTrigger is the body of triggerInboundWebhook.
type WebhookTrigger struct {
	TargetURL string            `json:"target_url"`
	Method    string            `json:"method,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      json.RawMessage   `json:"body,omitempty"`
	BodyRaw   *string           `json:"body_raw,omitempty"`
	Signature *Signature        `json:"signature,omitempty"`
	DelayMS   int               `json:"delay_ms,omitempty"`
}

// WebhookTriggerResult reports the Hub's answer to a triggered webhook.
type WebhookTriggerResult struct {
	SentAt        time.Time           `json:"sent_at"`
	TargetStatus  *int                `json:"target_status"`
	TargetHeaders map[string][]string `json:"target_headers,omitempty"`
	TargetBody    *string             `json:"target_body"`
	DurationMS    int64               `json:"duration_ms"`
	Error         *string             `json:"error"`
}

type errorDetail struct {
	Pointer string `json:"pointer,omitempty"`
	Message string `json:"message"`
}

type errorBody struct {
	Code      string        `json:"code"` // VALIDATION_FAILED | NOT_FOUND | INTERNAL_ERROR
	Message   string        `json:"message"`
	Retryable bool          `json:"retryable"`
	Details   []errorDetail `json:"details"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

// validationError collects field problems with JSON pointers.
type validationError struct{ details []errorDetail }

func (v *validationError) add(pointer, format string, args ...any) {
	v.details = append(v.details, errorDetail{Pointer: pointer, Message: fmt.Sprintf(format, args...)})
}

func (v *validationError) Error() string {
	msgs := make([]string, len(v.details))
	for i, d := range v.details {
		msgs[i] = d.Pointer + " " + d.Message
	}
	return strings.Join(msgs, "; ")
}

func (v *validationError) err() error {
	if len(v.details) == 0 {
		return nil
	}
	return v
}

var (
	routeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	methods        = map[string]bool{"*": true, "GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true}
)

func (s *RouteSpec) validate() error {
	v := &validationError{}
	if !methods[s.Match.Method] {
		v.add("/match/method", "must be one of *, GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS")
	}
	if !strings.HasPrefix(s.Match.Path, "/") {
		v.add("/match/path", "must start with /")
	}
	if s.Times != nil && *s.Times < 1 {
		v.add("/times", "must be >= 1 or null")
	}
	s.Behaviour.validate(v, "/behaviour")
	return v.err()
}

func (b *Behaviour) validate(v *validationError, at string) {
	if b.Status != 0 && (b.Status < 100 || b.Status > 599) {
		v.add(at+"/status", "must be 100..599")
	}
	if len(b.Body) > 0 && !json.Valid(b.Body) {
		v.add(at+"/body", "must be JSON")
	}
	if l := b.Latency; l != nil {
		switch l.Mode {
		case "fixed":
			if l.FixedMS < 0 {
				v.add(at+"/latency/fixed_ms", "must be >= 0")
			}
		case "random":
			if l.MinMS < 0 || l.MaxMS < 0 {
				v.add(at+"/latency", "min_ms and max_ms must be >= 0")
			}
			if l.MinMS > l.MaxMS {
				v.add(at+"/latency/min_ms", "must be <= max_ms")
			}
		default:
			v.add(at+"/latency/mode", "must be fixed or random")
		}
	}
	if e := b.ErrorRatio; e != nil {
		if e.Ratio < 0 || e.Ratio > 1 {
			v.add(at+"/error_ratio/ratio", "must be 0..1")
		}
		if e.Status < 400 || e.Status > 599 {
			v.add(at+"/error_ratio/status", "must be 400..599")
		}
	}
	if r := b.RateLimit; r != nil {
		if r.EveryNth < 0 || r.Ratio < 0 || r.Ratio > 1 || r.Burst < 0 || r.RetryAfterMS < 0 {
			v.add(at+"/rate_limit", "every_nth, burst and retry_after_ms must be >= 0 and ratio 0..1")
		}
		if (r.EveryNth > 0) == (r.Ratio > 0) {
			v.add(at+"/rate_limit", "set exactly one of every_nth or ratio")
		}
	}
	if d := b.Down; d != nil && d.Mode != "reset" && d.Mode != "refuse" && d.Mode != "hang" {
		v.add(at+"/down/mode", "must be reset, refuse or hang")
	}
	if d := b.SlowDrip; d != nil && (d.ChunkBytes < 1 || d.IntervalMS < 1) {
		v.add(at+"/slow_drip", "chunk_bytes and interval_ms must be >= 1")
	}
	if m := b.Malformed; m != nil {
		switch m.Kind {
		case "truncated_json", "invalid_json", "wrong_content_type", "empty_body", "html_error_page":
		default:
			v.add(at+"/malformed/kind", "unknown kind %q", m.Kind)
		}
	}
}

// matchPath reports whether path matches the glob: "*" matches one segment, "**" any number
// of segments (including none).
func matchPath(glob, path string) bool {
	return matchSegments(strings.Split(strings.Trim(glob, "/"), "/"), strings.Split(strings.Trim(path, "/"), "/"))
}

func matchSegments(glob, path []string) bool {
	for len(glob) > 0 {
		switch g := glob[0]; g {
		case "**":
			for i := 0; i <= len(path); i++ {
				if matchSegments(glob[1:], path[i:]) {
					return true
				}
			}
			return false
		default:
			if len(path) == 0 || (g != "*" && g != path[0]) {
				return false
			}
			glob, path = glob[1:], path[1:]
		}
	}
	return len(path) == 0
}
