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
	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Base is the API's path prefix.
const Base = "/admin/v1"

// Deps are the admin server's collaborators. Resource handlers use what they need; nil
// collaborators make their operations answer 500 INTERNAL_ERROR, never panic.
type Deps struct {
	Pool      *pgxpool.Pool
	Redis     *redis.Client
	Verifier  Verifier // nil: every request is 401 (no issuer configured)
	Publisher *control.Publisher
	Rotator   *control.Rotator
	Archive   *control.Archive
	// Snapshot is the admin's current snapshot (reads that need no archive round trip).
	Snapshot func() *registry.Snapshot
	Log      *slog.Logger
	Now      func() time.Time
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
	s.mux.Handle(method+" "+Base+path, s.authorize(min, h))
}

// authorize authenticates the bearer token and enforces the operation's minimum role.
func (s *Server) authorize(min Role, next http.Handler) http.Handler {
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
	if !errors.As(err, &e) {
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
