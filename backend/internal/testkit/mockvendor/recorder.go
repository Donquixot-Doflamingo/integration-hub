package mockvendor

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
)

// recorder keeps the most recent requests in arrival order, bounded to max entries.
type recorder struct {
	mu      sync.Mutex
	max     int
	nextSeq uint64
	entries []*entry
}

type entry struct {
	seq uint64
	mu  sync.Mutex // guards req fields filled in after the response (status, outcome)
	req RecordedRequest
}

func newRecorder(maxEntries int) *recorder { return &recorder{max: maxEntries} }

func (r *recorder) add(at time.Time, req *http.Request, routeID string, body []byte, truncated bool) *entry {
	rr := RecordedRequest{
		ID: "rec_" + core.NewULID(at), ReceivedAt: at, Method: req.Method, Path: req.URL.Path,
		Query: map[string][]string(req.URL.Query()), Headers: map[string][]string(req.Header.Clone()),
		BodyTruncated: truncated, Outcome: "responded",
	}
	if routeID != "" {
		rr.RouteID = &routeID
	}
	switch {
	case len(body) == 0:
		rr.Body = json.RawMessage("null")
	case !truncated && json.Valid(body):
		rr.Body = slices.Clone(body)
	default:
		raw := string(body)
		rr.Body = json.RawMessage("null")
		rr.BodyRaw = &raw
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextSeq++
	e := &entry{seq: r.nextSeq, req: rr}
	r.entries = append(r.entries, e)
	if len(r.entries) > r.max {
		r.entries = slices.Delete(r.entries, 0, len(r.entries)-r.max)
	}
	return e
}

// finish records how the request was answered.
func (e *entry) finish(status int, outcome string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if status > 0 {
		e.req.ResponseStatus = &status
	}
	e.req.Outcome = outcome
}

func (e *entry) snapshot() RecordedRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.req
}

type listFilter struct {
	routeID, method, path string
	since                 time.Time
	after                 uint64
	limit                 int
}

// list returns matching entries oldest first, and a cursor when more remain.
func (r *recorder) list(f listFilter) ([]RecordedRequest, *string) {
	r.mu.Lock()
	entries := slices.Clone(r.entries)
	r.mu.Unlock()
	items := make([]RecordedRequest, 0, min(f.limit, len(entries)))
	var last uint64
	for _, e := range entries {
		if e.seq <= f.after {
			continue
		}
		rr := e.snapshot()
		if f.routeID != "" && (rr.RouteID == nil || *rr.RouteID != f.routeID) {
			continue
		}
		if f.method != "" && !strings.EqualFold(rr.Method, f.method) {
			continue
		}
		if f.path != "" && !matchPath(f.path, rr.Path) {
			continue
		}
		if !f.since.IsZero() && rr.ReceivedAt.Before(f.since) {
			continue
		}
		if len(items) == f.limit {
			c := encodeCursor(last)
			return items, &c
		}
		items = append(items, rr)
		last = e.seq
	}
	return items, nil
}

func (r *recorder) clear() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.entries)
	r.entries = nil
	return n
}

func (r *recorder) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

func encodeCursor(seq uint64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatUint(seq, 10)))
}

func decodeCursor(c string) (uint64, bool) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(string(b), 10, 64)
	return n, err == nil
}

func (s *Server) listRequests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := listFilter{routeID: q.Get("route_id"), method: q.Get("method"), path: q.Get("path"), limit: 50}
	var bad []errorDetail
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			bad = append(bad, errorDetail{Pointer: "since", Message: "must be RFC 3339"})
		}
		f.since = t
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			bad = append(bad, errorDetail{Pointer: "limit", Message: "must be 1..200"})
		}
		f.limit = n
	}
	if v := q.Get("cursor"); v != "" {
		seq, ok := decodeCursor(v)
		if !ok {
			bad = append(bad, errorDetail{Pointer: "cursor", Message: "invalid cursor"})
		}
		f.after = seq
	}
	if len(bad) > 0 {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid query", bad)
		return
	}
	items, next := s.rec.list(f)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) clearRequests(w http.ResponseWriter, _ *http.Request) {
	s.rec.clear()
	w.WriteHeader(http.StatusNoContent)
}
