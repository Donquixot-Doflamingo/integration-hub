package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
)

// fakeHub is a contract-validated stand-in for the gateway (/v1) and the control plane
// (/admin/v1) on one httptest server. Every request is matched to its operationId, recorded,
// answered from the replies queued for that operation, and the whole exchange (request and
// response) is validated against the OpenAPI document; any violation fails the test.
type fakeHub struct {
	t      testing.TB
	srv    *httptest.Server
	public *contract.Spec
	admin  *contract.Spec
	keys   map[string]bool   // accepted tenant API keys
	roles  map[string]string // hub-admin operationId -> x-required-role

	mu      sync.Mutex
	calls   []hubCall
	replies map[string][]reply // per operationId; the last reply repeats
}

// hubCall is one request the fake Hub received.
type hubCall struct {
	OperationID string
	Method      string
	Path        string
	Query       string
	Header      http.Header
	Body        []byte
}

// reply is a canned response. Bodies normally come from contract examples (see example).
type reply struct {
	status int
	body   []byte
	header map[string]string
}

const fakeRequestID = "req_01J9Z3K8X2V7Q4M6N8P0R2S4T6"

func newFakeHub(t testing.TB) *fakeHub {
	t.Helper()
	h := &fakeHub{
		t:       t,
		public:  contract.Load(t, "hub-public.v1.yaml"),
		admin:   contract.Load(t, "hub-admin.v1.yaml"),
		keys:    map[string]bool{testKey: true},
		replies: map[string][]reply{},
	}
	h.roles = map[string]string{}
	for _, op := range h.admin.Operations() {
		h.roles[op.ID] = op.Extensions["x-required-role"]
	}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.srv.Close)
	return h
}

// on queues replies for an operation (in order; the last one repeats).
func (h *fakeHub) on(operationID string, rs ...reply) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.replies[operationID] = append(h.replies[operationID], rs...)
}

// callsTo returns the recorded calls of one operation ("" = all calls).
func (h *fakeHub) callsTo(operationID string) []hubCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []hubCall
	for _, c := range h.calls {
		if operationID == "" || c.OperationID == operationID {
			out = append(out, c)
		}
	}
	return out
}

func (h *fakeHub) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	spec, isAdmin := h.public, false
	if strings.HasPrefix(r.URL.Path, "/admin/v1/") {
		spec, isAdmin = h.admin, true
	}
	probe := r.Clone(r.Context())
	probe.Body = io.NopCloser(bytes.NewReader(body))
	opID, err := spec.OperationID(probe)
	if err != nil {
		h.t.Errorf("fake hub: %v", err)
		http.Error(w, "not in contract", http.StatusNotFound)
		return
	}
	h.mu.Lock()
	h.calls = append(h.calls, hubCall{OperationID: opID, Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body})
	h.mu.Unlock()

	rep := h.next(opID, isAdmin, r.Header.Get("Authorization"))
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	rec.Header().Set("X-Hub-Request-Id", fakeRequestID)
	for k, v := range rep.header {
		rec.Header().Set(k, v)
	}
	rec.WriteHeader(rep.status)
	_, _ = rec.Write(rep.body)

	ex := contract.Exchange{Request: r, RequestBody: body, Status: rec.Code, Header: rec.Header(), Body: rec.Body.Bytes()}
	if err := spec.Check(ex); err != nil { //nolint:contextcheck // Check validates offline, no I/O
		h.t.Errorf("fake hub exchange violates the contract: %v", err)
	}
	for k, vs := range rec.Header() {
		w.Header()[k] = vs
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}

// next picks the reply: 401 for a bad credential, 403 for the viewer token on an operation that
// needs editor or admin (the Hub's RBAC), else the queued reply for the operation.
func (h *fakeHub) next(opID string, isAdmin bool, authz string) reply {
	key := strings.TrimPrefix(authz, "Bearer ")
	switch {
	case isAdmin && key == testViewerToken && (h.roles[opID] == "editor" || h.roles[opID] == "admin"):
		return reply{status: http.StatusForbidden, body: h.admin.Example(h.t, opID, http.StatusForbidden, "")}
	case isAdmin && key != testAdminToken && key != testViewerToken:
		return reply{status: http.StatusUnauthorized, body: h.admin.Example(h.t, opID, http.StatusUnauthorized, "")}
	case !isAdmin && !h.keys[key]:
		return reply{status: http.StatusUnauthorized, body: h.public.Example(h.t, opID, http.StatusUnauthorized, "")}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	q := h.replies[opID]
	if len(q) == 0 {
		h.t.Errorf("fake hub: unexpected call to %s", opID)
		return reply{status: http.StatusInternalServerError, body: h.public.Example(h.t, "listIntegrations", http.StatusInternalServerError, "INTERNAL_ERROR")}
	}
	r := q[0]
	if len(q) > 1 {
		h.replies[opID] = q[1:]
	}
	return r
}

// pub returns a reply with a hub-public example body.
func (h *fakeHub) pub(opID string, status int, name string, mutate ...func(map[string]any)) reply {
	return reply{status: status, body: mutated(h.t, h.public.Example(h.t, opID, status, name), mutate...)}
}

func mutated(t testing.TB, b []byte, mutate ...func(map[string]any)) []byte {
	t.Helper()
	if len(mutate) == 0 {
		return b
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, f := range mutate {
		f(m)
	}
	return mustJSON(t, m)
}

// adm returns a reply with a hub-admin example body.
func (h *fakeHub) adm(opID string, status int, name string, mutate ...func(map[string]any)) reply {
	return reply{status: status, body: mutated(h.t, h.admin.Example(h.t, opID, status, name), mutate...)}
}
