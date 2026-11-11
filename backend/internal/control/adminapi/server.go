// Package adminapi serves the control-plane API /admin/v1 (DESIGN §16.1,
// contracts/openapi/hub-admin.v1.yaml): OIDC bearer auth, RBAC per operation
// (x-required-role), the change log on every mutation, and the admin error envelope.
package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/authz"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// Base is the API's path prefix.
const Base = "/admin/v1"

// Deps are the admin server's collaborators. Resource handlers use what they need; nil
// collaborators make their operations answer 500 INTERNAL_ERROR, never panic.
type Deps struct {
	Pool     *pgxpool.Pool
	Redis    *redis.Client
	Verifier Verifier // nil: every request is 401 (no issuer configured)
	// ISFC, when set, switches the server to HUB_ADMIN_AUTH_MODE=isfc: identity comes from the
	// ISFC gateway headers and a verified token instead of Verifier (nil: the jwt mode).
	ISFC *ISFCAuth
	// Authz decides every request in ISFC mode (an *authz.Client, or authz.Disabled in tests). nil
	// in ISFC mode refuses every request with 503 authz_unavailable (fail closed).
	Authz authz.Decider
	// Secrets is the process's redaction set; API keys of machine callers are added by value
	// (HUB-476). nil: nothing is registered.
	Secrets *telemetry.SecretSet
	// Metrics receives the auth metrics of the ISFC mode (nil: none are recorded).
	Metrics   prometheus.Registerer
	Publisher *control.Publisher
	Rotator   *control.Rotator
	Archive   *control.Archive
	// Snapshot is the admin's current snapshot (reads that need no archive round trip).
	Snapshot func() *registry.Snapshot
	// Sealer decrypts sealed payloads for unsealRequest (nil: unseal answers 500).
	Sealer *audit.Sealer
	// Upstream sends account connection tests (nil: a private client with its own metrics).
	Upstream *upstream.Client
	// CallbackHosts allowlists registrar endpoint hosts: "hooks.example.com" matches that host,
	// ".example.com" any host under it. Empty: any public host (the SSRF guard still applies).
	CallbackHosts []string
	Log           *slog.Logger
	Now           func() time.Time
}

// Route is one registered operation.
type Route struct {
	Op       string // contract operationId
	Method   string
	Path     string // ServeMux pattern path under Base, e.g. "/integrations/{integration_id}"
	Role     Role   // x-required-role
	Mutating bool   // writes a change_log row
}

// Server is the /admin/v1 HTTP surface.
type Server struct {
	d      Deps
	mux    *http.ServeMux
	routes []Route
	authn  *authnMetrics
}

// New builds the server with every implemented operation registered.
func New(d Deps) *Server {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	s := &Server{d: d, mux: http.NewServeMux()}
	if d.ISFC != nil {
		s.authn = newAuthnMetrics(d.Metrics)
	}
	s.registerMe()
	s.registerChangeLog()
	for _, reg := range registrars {
		reg(s)
	}
	s.mux.HandleFunc(Base+"/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, &Error{Status: http.StatusNotFound, Code: CodeNotFound, Message: "No such admin operation"})
	})
	return s
}

// registrars lets each resource file add its operations (func init() { registrars = append(...) }).
var registrars []func(*Server)

// Routes lists the registered operations (contract and RBAC tests).
func (s *Server) Routes() []Route { return append([]Route(nil), s.routes...) }

// Handler is the full chain: request id → recover → routes (auth and RBAC per route).
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := core.NewRequestID()
		w.Header().Set("X-Hub-Request-Id", id)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		defer func() { //nolint:contextcheck // uses the request's own context via r
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler { //nolint:errorlint // sentinel compared as panic value, per net/http
					panic(v)
				}
				s.d.Log.ErrorContext(r.Context(), "admin handler panic", slog.String("panic", fmt.Sprint(v)),
					slog.String("stack", string(debug.Stack())), slog.String("route", r.Pattern))
				writeError(w, r, internalError())
			}
		}()
		s.mux.ServeHTTP(w, r)
	})
}

// handle registers op at "METHOD /path" (path under Base) behind auth and role min.
func (s *Server) handle(op string, min Role, mutating bool, pattern string, h http.HandlerFunc) {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		panic("adminapi: pattern needs a method: " + pattern)
	}
	s.routes = append(s.routes, Route{Op: op, Method: method, Path: path, Role: min, Mutating: mutating})
	s.mux.Handle(method+" "+Base+path, s.authorize(op, min, h))
}

// authorize authenticates the caller and enforces the operation's minimum role. In ISFC mode the
// identity comes from the gateway headers (authorizeISFC); otherwise from the bearer token.
func (s *Server) authorize(op string, min Role, next http.Handler) http.Handler {
	if s.d.ISFC != nil {
		return s.authorizeISFC(op, min, next)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || tok == "" {
			writeError(w, r, &Error{Status: http.StatusUnauthorized, Code: CodeUnauthorized, Message: "Missing bearer token"})
			return
		}
		if s.d.Verifier == nil {
			writeError(w, r, &Error{Status: http.StatusUnauthorized, Code: CodeUnauthorized, Message: "No identity provider is configured"})
			return
		}
		p, err := s.d.Verifier.Verify(r.Context(), tok)
		if err != nil {
			msg := "Invalid token"
			if errors.Is(err, ErrBadToken) {
				msg = "Invalid token: " + strings.TrimPrefix(err.Error(), ErrBadToken.Error()+": ")
			} else {
				s.d.Log.WarnContext(r.Context(), "token verification failed", slog.String("error", err.Error()))
			}
			writeError(w, r, &Error{Status: http.StatusUnauthorized, Code: CodeUnauthorized, Message: msg})
			return
		}
		if !p.Allows(min) {
			writeError(w, r, &Error{Status: http.StatusForbidden, Code: CodeForbidden, Message: fmt.Sprintf("Requires role '%s'", min)})
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, p)
		ctx = control.WithChangeMeta(ctx, p.RoleNames(), RequestID(ctx))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authorizeISFC is authorize for HUB_ADMIN_AUTH_MODE=isfc: authenticate (token plus gateway
// headers), ask OPA (a deny is 403, an error or timeout 503 authz_unavailable), then keep the
// in-process role check as a second layer (AUTH_PLAN §7.1).
func (s *Server) authorizeISFC(op string, min Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, aerr := s.d.ISFC.authenticate(r)
		if aerr != nil {
			s.authn.fail(aerr.reason)
			writeError(w, r, aerr.err)
			return
		}
		if len(p.Roles) == 0 {
			s.authn.fail(authnNoRoles)
			s.logAuthz(r, p, op, authz.DecisionDeny, "no Hub role in this tenant")
			writeError(w, r, &Error{Status: http.StatusForbidden, Code: CodeForbidden, Message: "No Hub role in this tenant"})
			return
		}
		if s.d.Authz == nil {
			s.logAuthz(r, p, op, authz.DecisionError, "no policy engine configured")
			writeError(w, r, authzUnavailable())
			return
		}
		d, err := s.d.Authz.Decide(r.Context(), s.opaInput(r, p))
		if err != nil {
			s.logAuthz(r, p, op, authz.DecisionError, err.Error())
			writeError(w, r, authzUnavailable())
			return
		}
		if !d.Allow {
			s.logAuthz(r, p, op, authz.DecisionDeny, d.Reason)
			writeError(w, r, &Error{Status: http.StatusForbidden, Code: CodeForbidden, Message: "Not permitted by the authorization policy"})
			return
		}
		s.logAuthz(r, p, op, authz.DecisionAllow, d.Reason)
		if !p.Allows(min) {
			writeError(w, r, &Error{Status: http.StatusForbidden, Code: CodeForbidden, Message: fmt.Sprintf("Requires role '%s'", min)})
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, p)
		ctx = control.WithChangeMeta(ctx, p.RoleNames(), RequestID(ctx))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// opaInput is the sidecar-compatible policy input of one request: the subject with its gateway
// attributes, the path the Hub sees, the client, and the tenant of the request.
func (s *Server) opaInput(r *http.Request, p Principal) authz.Input {
	sub := make(map[string]any, len(p.SubAttrs)+2)
	for k, v := range p.SubAttrs {
		sub[k] = v
	}
	sub["id"], sub["type"] = p.Subject, p.SubType
	in := authz.Input{Sub: sub, HTTP: authz.HTTP{Method: r.Method, Path: r.URL.Path}, Client: authz.Caller{ClientID: p.ClientID}}
	if p.ISFCTenantID != "" {
		in.Tenant = &authz.Tenant{ID: p.ISFCTenantID}
	}
	return in
}

// logAuthz is the admin.authz log: who, where, which operation and the decision. It never
// carries a token, the attributes or a key.
func (s *Server) logAuthz(r *http.Request, p Principal, op, decision, reason string) {
	level := slog.LevelDebug
	switch decision {
	case authz.DecisionDeny:
		level = slog.LevelInfo
	case authz.DecisionError:
		level = slog.LevelWarn
	}
	s.d.Log.Log(r.Context(), level, "admin.authz", slog.String("subject", p.Subject), slog.String("client", p.ClientID),
		slog.String("tenant", p.ISFCTenantID), slog.String("operation", op), slog.String("decision", decision), slog.String("reason", reason))
}

type requestIDKey struct{}

// RequestID returns the admin request id of ctx.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// principal is the authenticated principal of an authorized request.
func principal(r *http.Request) Principal {
	p, _ := PrincipalFrom(r.Context())
	return p
}

// Error codes of the admin contract (AdminError.code).
const (
	CodeValidationFailed        = "VALIDATION_FAILED"
	CodeUnauthorized            = "UNAUTHORIZED"
	CodeForbidden               = "FORBIDDEN"
	CodeNotFound                = "NOT_FOUND"
	CodeConflict                = "CONFLICT"
	CodePreconditionFailed      = "PRECONDITION_FAILED"
	CodePublishValidationFailed = "PUBLISH_VALIDATION_FAILED"
	CodeRetireBlocked           = "RETIRE_BLOCKED"
	CodeRateLimited             = "RATE_LIMITED"
	CodeInternalError           = "INTERNAL_ERROR"
)

// Detail is one AdminError detail.
type Detail struct {
	Pointer string `json:"pointer,omitempty"`
	Keyword string `json:"keyword,omitempty"`
	Message string `json:"message"`
	// Sample and Field locate a sample check failure (since hub-admin 2.0.0, ADR-0023).
	Sample string `json:"sample,omitempty"`
	Field  string `json:"field,omitempty"`
}

// Error is an admin API error; handlers return it and writeError renders the envelope.
type Error struct {
	Status       int            `json:"-"`
	Code         string         `json:"code"`
	Message      string         `json:"message"`
	Retryable    bool           `json:"retryable"`
	RetryAfterMS *int64         `json:"retry_after_ms,omitempty"`
	Details      []Detail       `json:"details,omitempty"`
	Meta         map[string]any `json:"meta,omitempty"`
	RequestID    string         `json:"request_id"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func internalError() *Error {
	return &Error{Status: http.StatusInternalServerError, Code: CodeInternalError, Message: "Internal error", Retryable: true}
}

// badRequest is a 400 VALIDATION_FAILED with one detail at pointer.
func badRequest(pointer, msg string) *Error {
	return &Error{Status: http.StatusBadRequest, Code: CodeValidationFailed, Message: msg, Details: []Detail{{Pointer: pointer, Message: msg}}}
}

func notFound(what string) *Error {
	return &Error{Status: http.StatusNotFound, Code: CodeNotFound, Message: what + " not found"}
}

// fail writes err: an *Error as is, anything else as a logged 500.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *Error
	var load *control.SnapshotLoadError
	if errors.As(err, &load) && !errors.As(err, &e) {
		// Every read and write starts from the newest archived snapshot: say which one is broken
		// and why, instead of a bare internal error (HUB-451). The contract has no 503 or
		// dedicated code, so it stays INTERNAL_ERROR with the detail in message and meta.
		s.d.Log.ErrorContext(r.Context(), "newest archived snapshot cannot be loaded", slog.String("route", r.Pattern),
			slog.Int64("archive_version", load.Version), slog.String("error", err.Error()))
		e = &Error{Status: http.StatusInternalServerError, Code: CodeInternalError, Retryable: false,
			Message: fmt.Sprintf("The newest archived configuration snapshot (v%d) cannot be loaded: %s. Reads and edits are refused until it is repaired by an operator (restore the blob or remove the unloadable snapshot row; see the runbook).", load.Version, load.Reason()),
			Meta:    map[string]any{"cause": "snapshot_unloadable", "snapshot_version": load.Version}}
	} else if !errors.As(err, &e) {
		s.d.Log.ErrorContext(r.Context(), "admin operation failed", slog.String("route", r.Pattern), slog.String("error", err.Error()))
		e = internalError()
	}
	writeError(w, r, e)
}

func writeError(w http.ResponseWriter, r *http.Request, e *Error) {
	cp := *e
	cp.RequestID = RequestID(r.Context())
	if cp.Status == 0 {
		cp.Status = http.StatusInternalServerError
	}
	if cp.RetryAfterMS != nil {
		w.Header().Set("Retry-After", strconv.FormatInt((*cp.RetryAfterMS+999)/1000, 10))
	}
	writeJSON(w, cp.Status, map[string]any{"error": cp})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decode reads a JSON body (≤ 4 MB, unknown fields refused) into v.
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("", "Request body is not valid JSON for this operation: "+err.Error())
	}
	return nil
}

// reasonOf returns the mandatory change reason: the body's reason, or ?reason= for DELETE.
func reasonOf(r *http.Request, body string) (string, error) {
	reason := body
	if r.Method == http.MethodDelete {
		reason = r.URL.Query().Get("reason")
	}
	if reason = strings.TrimSpace(reason); reason == "" {
		pointer := "/reason"
		if r.Method == http.MethodDelete {
			pointer = ""
		}
		return "", badRequest(pointer, "A reason is required for every change")
	}
	if n := utf8.RuneCountInString(reason); n < 3 || n > 1024 {
		pointer := "/reason"
		if r.Method == http.MethodDelete {
			pointer = ""
		}
		return "", badRequest(pointer, "A reason must be 3 to 1024 characters")
	}
	return reason, nil
}
