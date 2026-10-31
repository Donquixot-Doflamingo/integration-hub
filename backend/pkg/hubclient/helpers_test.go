package hubclient_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient"
)

// testKey is a fake tenant key. Tests assert it never shows up in errors or formatted values.
const testKey = "hk_test_canary_9f2c51e7EXAMPLEnotreal"

const testRequestID = "req_01J9Z3K8X2V7Q4M6N8P0R2S4T6"

var loadDoc = sync.OnceValues(func() (*openapi3.T, error) {
	dir, err := contract.Dir()
	if err != nil {
		return nil, err
	}
	l := openapi3.NewLoader()
	l.IsExternalRefsAllowed = true
	return l.LoadFromFile(filepath.Join(dir, "openapi", "hub-public.v1.yaml"))
})

func doc(t testing.TB) *openapi3.T {
	t.Helper()
	d, err := loadDoc()
	if err != nil {
		t.Fatalf("load hub-public.v1.yaml: %v", err)
	}
	return d
}

// namedExample is one response example of the contract, as JSON.
type namedExample struct {
	Name string
	JSON []byte
}

// mediaExamples returns the examples of a JSON media type: the single `example` (named "") or
// every entry of `examples`, sorted by name.
func mediaExamples(t testing.TB, mt *openapi3.MediaType) []namedExample {
	t.Helper()
	if mt == nil {
		t.Fatal("response has no application/json content")
	}
	var out []namedExample
	if mt.Example != nil {
		out = append(out, namedExample{"", mustJSON(t, mt.Example)})
	}
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for k := range mt.Examples {
			if !yield(k) {
				return
			}
		}
	}) {
		out = append(out, namedExample{name, mustJSON(t, mt.Examples[name].Value.Value)})
	}
	if len(out) == 0 {
		t.Fatal("response has no example")
	}
	return out
}

// componentExamples returns the examples of components.responses[name].
func componentExamples(t testing.TB, name string) []namedExample {
	t.Helper()
	r, ok := doc(t).Components.Responses[name]
	if !ok {
		t.Fatalf("no response component %q", name)
	}
	return mediaExamples(t, r.Value.Content.Get("application/json"))
}

// componentExample returns one example of components.responses[name] ("" = the only one).
func componentExample(t testing.TB, name, key string) []byte {
	t.Helper()
	for _, ex := range componentExamples(t, name) {
		if ex.Name == key {
			return ex.JSON
		}
	}
	t.Fatalf("response component %q has no example %q", name, key)
	return nil
}

// pathExample returns one example of an inline path response ("" = the only one).
func pathExample(t testing.TB, path, method string, status int, key string) []byte {
	t.Helper()
	item := doc(t).Paths.Find(path)
	if item == nil {
		t.Fatalf("no path %s", path)
	}
	op := item.GetOperation(method)
	if op == nil {
		t.Fatalf("no %s %s", method, path)
	}
	for _, ex := range mediaExamples(t, op.Responses.Status(status).Value.Content.Get("application/json")) {
		if ex.Name == key {
			return ex.JSON
		}
	}
	t.Fatalf("%s %s %d has no example %q", method, path, status, key)
	return nil
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// requestIDOf reads request_id from a body or from its error envelope.
func requestIDOf(body []byte) string {
	var v struct {
		RequestID string `json:"request_id"`
		Error     struct {
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &v)
	if v.RequestID != "" {
		return v.RequestID
	}
	if v.Error.RequestID != "" {
		return v.Error.RequestID
	}
	return testRequestID
}

// reply is a canned Hub response.
type reply struct {
	status int
	body   []byte
	header http.Header // extra headers; Content-Type and X-Hub-Request-Id are set by default
}

// seen is a request the fake Hub received.
type seen struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

// fakeHub is an httptest server that answers with canned replies and records requests.
type fakeHub struct {
	srv    *httptest.Server
	client *hubclient.Client
	mu     sync.Mutex
	seen   []seen
}

func (f *fakeHub) requests() []seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seen)
}

func (f *fakeHub) last(t testing.TB) seen {
	t.Helper()
	r := f.requests()
	if len(r) == 0 {
		t.Fatal("the fake Hub received no request")
	}
	return r[len(r)-1]
}

// newHub starts a fake Hub that answers replies in order (the last one repeats). With
// validate, every exchange is checked against hub-public.v1.yaml and violations fail t.
func newHub(t *testing.T, validate bool, replies ...reply) *fakeHub {
	t.Helper()
	f := &fakeHub{}
	var n int
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.seen = append(f.seen, seen{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body})
		rp := replies[min(n, len(replies)-1)]
		n++
		f.mu.Unlock()
		writeReply(w, rp)
	}))
	t.Cleanup(f.srv.Close)
	f.client = newClient(t, f.srv.URL, validate)
	return f
}

func writeReply(w http.ResponseWriter, rp reply) {
	for k, vs := range rp.header {
		w.Header()[k] = vs
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	if _, set := rp.header["X-Hub-Request-Id"]; !set { // a nil entry means: send none
		w.Header().Set("X-Hub-Request-Id", requestIDOf(rp.body))
	}
	w.WriteHeader(rp.status)
	_, _ = w.Write(rp.body)
}

// newClient builds a client for baseURL whose transport validates exchanges when validate is set.
func newClient(t *testing.T, baseURL string, validate bool, opts ...hubclient.Option) *hubclient.Client {
	t.Helper()
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	var rt http.RoundTripper = tr
	if validate {
		rt = &checkingTransport{t: t, base: tr, spec: contract.Load(t, "hub-public.v1.yaml")}
	}
	c, err := hubclient.New(baseURL, testKey, append([]hubclient.Option{hubclient.WithHTTPClient(&http.Client{Transport: rt})}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// checkingTransport validates each request the SDK sends and each response it receives
// against the caller contract.
type checkingTransport struct {
	t    *testing.T
	base http.RoundTripper
	spec *contract.Spec
}

func (c *checkingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	resp, err := c.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	rb, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(rb))
	ex := contract.Exchange{Request: req.Clone(context.Background()), RequestBody: body, Status: resp.StatusCode, Header: resp.Header, Body: rb}
	if err := c.spec.Check(ex); err != nil {
		c.t.Errorf("contract violation: %v", err)
	}
	return resp, nil
}

// trackEvent is a canonical events.track@v1 body (the contract's request example).
func trackEvent() map[string]any {
	return map[string]any{
		"user":  map[string]any{"id": "u_48213"},
		"event": map[string]any{"name": "order_placed", "timestamp": "2026-10-31T09:15:00Z"},
	}
}

func jsonEqual(t testing.TB, got, want []byte) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got invalid JSON %s: %v", got, err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("want invalid JSON %s: %v", want, err)
	}
	if gb, wb := mustJSON(t, g), mustJSON(t, w); !bytes.Equal(gb, wb) {
		t.Errorf("JSON mismatch\n got: %s\nwant: %s", gb, wb)
	}
}
