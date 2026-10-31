package mockvendor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	controlPrefix = "/__control/"
	maxBodyBytes  = 1 << 20 // recordings keep bodies verbatim up to 1 MB
	maxSpecBytes  = 1 << 20
)

var defaultBehaviour = Behaviour{Status: http.StatusOK, Body: json.RawMessage(`{}`)}

// Server is the mock vendor: the vendor surface (any path outside /__control/) and the
// control API. It is safe for concurrent use. Create it with New.
type Server struct {
	log *slog.Logger
	now func() time.Time

	mu       sync.Mutex
	routes   map[string]*routeState
	seq      uint64 // tie-break: the most recently set route wins
	def      Behaviour
	defRNG   *rand.Rand
	refused  bool
	onRefuse func(refuse bool) // set by Listen: closes or reopens the vendor listener

	rec *recorder

	stop     chan struct{} // closed by Close: releases hung requests
	stopOnce sync.Once
}

type routeState struct {
	Route
	seq   uint64
	count int        // matched requests since set (rate_limit every_nth / burst)
	rng   *rand.Rand // guarded by Server.mu
}

// New returns an empty mock: no routes, default behaviour 200 {}.
func New(log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{
		log: log, now: time.Now, routes: map[string]*routeState{}, def: defaultBehaviour, defRNG: newRNG(defaultBehaviour),
		rec: newRecorder(10_000), stop: make(chan struct{}),
	}
}

// Close releases requests held by down.mode=hang. The Server must not be used afterwards.
func (s *Server) Close() { s.stopOnce.Do(func() { close(s.stop) }) }

// Handler serves both surfaces on one port, as the contract describes.
func (s *Server) Handler() http.Handler {
	control := s.ControlHandler()
	vendor := http.HandlerFunc(s.serveVendor)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, controlPrefix) {
			control.ServeHTTP(w, r)
			return
		}
		vendor.ServeHTTP(w, r)
	})
}

// ControlHandler serves only /__control/*. Listen exposes it on its own port too, so tests can
// still reach it while the vendor listener refuses connections.
func (s *Server) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__control/routes", s.listRoutes)
	mux.HandleFunc("GET /__control/routes/{route_id}", s.getRoute)
	mux.HandleFunc("PUT /__control/routes/{route_id}", s.setRoute)
	mux.HandleFunc("DELETE /__control/routes/{route_id}", s.deleteRoute)
	mux.HandleFunc("PUT /__control/default", s.setDefault)
	mux.HandleFunc("POST /__control/reset", s.reset)
	mux.HandleFunc("GET /__control/requests", s.listRequests)
	mux.HandleFunc("DELETE /__control/requests", s.clearRequests)
	mux.HandleFunc("POST /__control/webhooks", s.triggerWebhook)
	mux.HandleFunc("GET /__control/healthz", s.health)
	mux.HandleFunc("/__control/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("no control operation %s %s", r.Method, r.URL.Path), nil)
	})
	return mux
}

// ---- control API ----

func (s *Server) listRoutes(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	items := make([]Route, 0, len(s.routes))
	for _, rs := range s.routes {
		items = append(items, rs.Route)
	}
	s.mu.Unlock()
	slices.SortFunc(items, func(a, b Route) int { return strings.Compare(a.RouteID, b.RouteID) })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getRoute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("route_id")
	s.mu.Lock()
	rs, ok := s.routes[id]
	var rt Route
	if ok {
		rt = rs.Route
	}
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("route '%s' not found", id), nil)
		return
	}
	writeJSON(w, http.StatusOK, rt)
}

func (s *Server) setRoute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("route_id")
	if !routeIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid route_id", []errorDetail{{Pointer: "route_id", Message: "must match " + routeIDPattern.String()}})
		return
	}
	var spec RouteSpec
	if !decode(w, r, &spec) {
		return
	}
	if err := spec.validate(); err != nil {
		writeValidation(w, err)
		return
	}
	s.mu.Lock()
	s.seq++
	rs := &routeState{
		Route: Route{
			RouteID: id, Match: spec.Match, Priority: spec.Priority, Behaviour: spec.Behaviour,
			Remaining: spec.Times, CreatedAt: s.now().UTC(),
		},
		seq: s.seq,
		rng: newRNG(spec.Behaviour),
	}
	s.routes[id] = rs
	rt := rs.Route
	s.updateRefusedLocked()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, rt)
}

func (s *Server) deleteRoute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("route_id")
	s.mu.Lock()
	_, ok := s.routes[id]
	delete(s.routes, id)
	s.updateRefusedLocked()
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("route '%s' not found", id), nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setDefault(w http.ResponseWriter, r *http.Request) {
	var b Behaviour
	if !decode(w, r, &b) {
		return
	}
	v := &validationError{}
	b.validate(v, "")
	if err := v.err(); err != nil {
		writeValidation(w, err)
		return
	}
	s.mu.Lock()
	s.def = b
	s.defRNG = newRNG(b)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) reset(w http.ResponseWriter, r *http.Request) {
	req := ResetRequest{}
	// The body is optional; an empty body may arrive with or without Content-Length (chunked).
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxSpecBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "read body: "+err.Error(), nil)
		return
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid JSON body: "+err.Error(), nil)
			return
		}
	}
	res := ResetResult{}
	if req.Routes == nil || *req.Routes {
		s.mu.Lock()
		res.RoutesRemoved = len(s.routes)
		s.routes = map[string]*routeState{}
		s.def = defaultBehaviour
		s.defRNG = newRNG(defaultBehaviour)
		s.updateRefusedLocked()
		s.mu.Unlock()
	}
	if req.Recordings == nil || *req.Recordings {
		res.RecordingsRemoved = s.rec.clear()
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	n := len(s.routes)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "routes": n, "recordings": s.rec.len()})
}

// updateRefusedLocked turns connection refusal on while any active route has down.mode=refuse.
func (s *Server) updateRefusedLocked() {
	refuse := false
	for _, rs := range s.routes {
		if rs.Behaviour.Down != nil && rs.Behaviour.Down.Mode == "refuse" && rs.active() {
			refuse = true
			break
		}
	}
	if refuse != s.refused {
		s.refused = refuse
		if s.onRefuse != nil {
			s.onRefuse(refuse)
		}
	}
}

func (rs *routeState) active() bool { return rs.Remaining == nil || *rs.Remaining > 0 }

// ---- vendor surface ----

// plan is everything decided for one request while holding the lock, including every random
// draw (rand.Rand is not safe for concurrent use), so responding needs no lock.
type plan struct {
	routeID     string
	beh         Behaviour
	latency     time.Duration
	rateLimited bool
	errored     bool
}

// match picks the active route with the highest priority (most recently set on a tie),
// consumes one use of it and decides its random behaviours.
func (s *Server) match(r *http.Request) plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *routeState
	for _, rs := range s.routes {
		if !rs.active() || !rs.matches(r) {
			continue
		}
		if best == nil || rs.Priority > best.Priority || (rs.Priority == best.Priority && rs.seq > best.seq) {
			best = rs
		}
	}
	if best == nil {
		return decide(plan{beh: s.def}, 0, s.defRNG)
	}
	best.Hits++
	best.count++
	if best.Remaining != nil {
		n := *best.Remaining - 1
		best.Remaining = &n
		if n == 0 {
			s.updateRefusedLocked()
		}
	}
	return decide(plan{routeID: best.RouteID, beh: best.Behaviour}, best.count, best.rng)
}

func (rs *routeState) matches(r *http.Request) bool {
	if rs.Match.Method != "*" && rs.Match.Method != r.Method {
		return false
	}
	if !matchPath(rs.Match.Path, r.URL.Path) {
		return false
	}
	for k, v := range rs.Match.Headers {
		if r.Header.Get(k) != v {
			return false
		}
	}
	return true
}

func (s *Server) serveVendor(w http.ResponseWriter, r *http.Request) {
	body, truncated := readBody(r.Body)
	p := s.match(r)
	entry := s.rec.add(s.now().UTC(), r, p.routeID, body, truncated)
	s.respond(w, r, entry, p)
}

func readBody(rc io.ReadCloser) ([]byte, bool) {
	if rc == nil {
		return nil, false
	}
	defer func() { _ = rc.Close() }()
	b, _ := io.ReadAll(io.LimitReader(rc, maxBodyBytes+1))
	if len(b) > maxBodyBytes {
		_, _ = io.Copy(io.Discard, rc)
		return b[:maxBodyBytes], true
	}
	return b, false
}

// ---- helpers ----

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxSpecBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid JSON body: "+err.Error(), nil)
		return false
	}
	return true
}

func writeValidation(w http.ResponseWriter, err error) {
	var v *validationError
	if errors.As(err, &v) {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", v.Error(), v.details)
		return
	}
	writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", err.Error(), nil)
}

func writeError(w http.ResponseWriter, status int, code, msg string, details []errorDetail) {
	if details == nil {
		details = []errorDetail{}
	}
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: msg, Retryable: false, Details: details}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func newRNG(b Behaviour) *rand.Rand {
	if b.ErrorRatio != nil && b.ErrorRatio.Seed != nil {
		seed := uint64(*b.ErrorRatio.Seed)                          //nolint:gosec // seed bits, sign irrelevant
		return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) //nolint:gosec // deterministic test double, not crypto
	}
	return rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())) //nolint:gosec // test double, not crypto
}
