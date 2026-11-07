// Package contract validates live HTTP exchanges against the frozen OpenAPI documents in
// contracts/openapi (AGENTS §4.6). Handler tests send real requests through Serve or Check;
// any status, header or body the contract does not allow fails the test with a JSON pointer.
package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

// Spec is one loaded contract document with a router for matching requests to operations.
type Spec struct {
	name   string
	doc    *openapi3.T
	router routers.Router
}

var (
	cacheMu sync.Mutex
	cache   = map[string]*Spec{}
)

// Load returns the named document from contracts/openapi (e.g. "hub-public.v1.yaml"),
// loading it once per test binary. External $refs (../schemas/...) are resolved.
func Load(tb testing.TB, file string) *Spec {
	tb.Helper()
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if s, ok := cache[file]; ok {
		return s
	}
	dir, err := Dir()
	if err != nil {
		tb.Fatal(err)
	}
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	doc, err := loader.LoadFromFile(filepath.Join(dir, "openapi", file))
	if err != nil {
		tb.Fatalf("load %s: %v", file, err)
	}
	// Example correctness is checked by `make contracts-check` (HUB-015); kin-openapi applies
	// OpenAPI 3.0 nullability to examples and reports false positives on 3.1 documents.
	if err := doc.Validate(context.Background(), openapi3.DisableExamplesValidation()); err != nil {
		tb.Fatalf("contract %s is invalid: %v", file, err)
	}
	anyAcceptsNull(doc)
	// Match on path only: keep each server's base path, drop scheme and host.
	base := "/"
	if len(doc.Servers) > 0 {
		if u, err := url.Parse(doc.Servers[0].URL); err == nil && u.Path != "" {
			base = u.Path
		}
	}
	doc.Servers = openapi3.Servers{{URL: base}}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		tb.Fatalf("router for %s: %v", file, err)
	}
	s := &Spec{name: file, doc: doc, router: router}
	cache[file] = s
	return s
}

// anyAcceptsNull gives every type-less schema (any JSON value) every JSON type explicitly. In
// OpenAPI 3.1 / JSON Schema 2020-12 such a schema accepts null; kin-openapi only accepts null
// when the type list names it, and would refuse e.g. RequestStatus.result: null, whose schema
// (CanonicalResponse) is any JSON value since hub-public 2.0.0.
func anyAcceptsNull(doc *openapi3.T) {
	seen := map[*openapi3.Schema]bool{}
	var walk func(r *openapi3.SchemaRef)
	walk = func(r *openapi3.SchemaRef) {
		if r == nil || r.Value == nil || seen[r.Value] {
			return
		}
		s := r.Value
		seen[s] = true
		if (s.Type == nil || len(*s.Type) == 0) && len(s.AllOf) == 0 && len(s.OneOf) == 0 && len(s.AnyOf) == 0 && len(s.Properties) == 0 {
			s.Type = &openapi3.Types{"null", "boolean", "object", "array", "number", "string"}
			s.Nullable = true
		}
		for _, p := range s.Properties {
			walk(p)
		}
		walk(s.Items)
		walk(s.AdditionalProperties.Schema)
		for _, l := range [][]*openapi3.SchemaRef{s.AllOf, s.OneOf, s.AnyOf} {
			for _, x := range l {
				walk(x)
			}
		}
	}
	for _, r := range doc.Components.Schemas {
		walk(r)
	}
	for _, r := range doc.Components.Responses {
		if r.Value != nil {
			for _, mt := range r.Value.Content {
				walk(mt.Schema)
			}
		}
	}
}

// Dir finds the repository's contracts/ directory by walking up from the working directory.
func Dir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := wd; ; d = filepath.Dir(d) {
		if st, err := os.Stat(filepath.Join(d, "contracts", "openapi")); err == nil && st.IsDir() {
			return filepath.Join(d, "contracts"), nil
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("contracts/openapi not found above %s", wd)
		}
	}
}

// Exchange is one request and the response it produced.
type Exchange struct {
	Request     *http.Request
	RequestBody []byte
	Status      int
	Header      http.Header
	Body        []byte
	// InvalidRequest marks a request the test sends on purpose to break the contract (to check
	// the error response). Only the response is validated.
	InvalidRequest bool
}

// Check validates ex against the operation that matches its method and path. It returns an
// error listing every violation, each prefixed with its JSON pointer where kin-openapi gives one.
func (s *Spec) Check(ex Exchange) error {
	req := ex.Request.Clone(context.Background())
	req.Body = io.NopCloser(bytes.NewReader(ex.RequestBody))
	route, params, err := s.router.FindRoute(req)
	if err != nil {
		return fmt.Errorf("%s: %s %s is not an operation in the contract: %w", s.name, req.Method, req.URL.Path, err)
	}
	opts := &openapi3filter.Options{
		MultiError:            true,
		IncludeResponseStatus: true,
		AuthenticationFunc:    openapi3filter.NoopAuthenticationFunc,
	}
	if ex.Header.Get("X-Hub-Pass-Through") == "true" {
		route = passThroughRoute(route)
		// A non-JSON vendor body has no decoder in kin-openapi; the contract gives it the
		// schemaless */* media type (RawBody), so only its presence there is checked.
		if mt, _, _ := mime.ParseMediaType(ex.Header.Get("Content-Type")); mt != "" && !strings.Contains(mt, "json") {
			if def := route.Operation.Responses.Default(); def == nil || def.Value == nil || def.Value.Content.Get("*/*") == nil {
				return fmt.Errorf("%s: pass-through %s body of %s is not contracted (no */* default response)", s.name, mt, route.Operation.OperationID)
			}
			opts.ExcludeResponseBody = true
		}
	}
	in := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route, Options: opts}
	var errs []error
	if err := openapi3filter.ValidateRequest(context.Background(), in); err != nil && !ex.InvalidRequest {
		errs = append(errs, fmt.Errorf("request: %s", describe(err)))
	}
	out := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in, Status: ex.Status, Header: ex.Header, Options: opts,
	}
	out.SetBodyBytes(ex.Body)
	if err := openapi3filter.ValidateResponse(context.Background(), out); err != nil {
		errs = append(errs, fmt.Errorf("response %d: %s", ex.Status, describe(err)))
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s %s %s (operation %s) violates %s:\n%w",
			req.Method, req.URL.Path, http.StatusText(ex.Status), route.Operation.OperationID, s.name, errors.Join(errs...))
	}
	return nil
}

// passThroughRoute validates a pass-through response (X-Hub-Pass-Through: true, ADR-0023)
// against the operation's default response only, also when its status equals a listed one
// (contracts/README.md "Backend: contract tests"). An operation without a default response is
// left as it is, so a pass-through where none is contracted fails.
func passThroughRoute(route *routers.Route) *routers.Route {
	def := route.Operation.Responses.Default()
	if def == nil || def.Value == nil {
		return route
	}
	op := *route.Operation
	op.Responses = openapi3.NewResponses(openapi3.WithName("default", def.Value))
	r := *route
	r.Operation = &op
	return &r
}

// describe flattens kin-openapi errors into "pointer: reason" lines.
func describe(err error) string {
	var lines []string
	var walk func(error)
	walk = func(e error) {
		var multi openapi3.MultiError
		if errors.As(e, &multi) {
			for _, m := range multi {
				walk(m)
			}
			return
		}
		var se *openapi3.SchemaError
		if errors.As(e, &se) {
			lines = append(lines, "/"+strings.Join(se.JSONPointer(), "/")+": "+se.Reason)
			return
		}
		lines = append(lines, e.Error())
	}
	walk(err)
	return strings.Join(lines, "; ")
}

// Serve runs req through h, validates the exchange and fails tb on any violation. It returns
// the recorded response for further assertions.
func (s *Spec) Serve(tb testing.TB, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	tb.Helper()
	return s.serve(tb, h, req, false)
}

// ServeInvalid is Serve for a request that breaks the contract on purpose: only the response is
// validated.
func (s *Spec) ServeInvalid(tb testing.TB, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	tb.Helper()
	return s.serve(tb, h, req, true)
}

func (s *Spec) serve(tb testing.TB, h http.Handler, req *http.Request, invalid bool) *httptest.ResponseRecorder {
	tb.Helper()
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if err := s.Check(Exchange{Request: req, RequestBody: body, Status: rec.Code, Header: rec.Header(), Body: rec.Body.Bytes(), InvalidRequest: invalid}); err != nil {
		tb.Error(err)
	}
	return rec
}

// Do sends req over the network (to a running server), validates the exchange and fails tb on
// any violation. It returns the status and body.
func (s *Spec) Do(tb testing.TB, req *http.Request) (int, []byte) {
	tb.Helper()
	return s.do(tb, req, false)
}

// DoInvalid is Do for a request that breaks the contract on purpose: only the response
// (normally a 4xx error envelope) is validated.
func (s *Spec) DoInvalid(tb testing.TB, req *http.Request) (int, []byte) {
	tb.Helper()
	return s.do(tb, req, true)
}

func (s *Spec) do(tb testing.TB, req *http.Request, invalid bool) (int, []byte) {
	tb.Helper()
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	resp, err := http.DefaultClient.Do(req) // #nosec G107 G704 -- test helper: the URL is the test's own server
	if err != nil {
		tb.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	rb, _ := io.ReadAll(resp.Body)
	if err := s.Check(Exchange{Request: req, RequestBody: body, Status: resp.StatusCode, Header: resp.Header, Body: rb, InvalidRequest: invalid}); err != nil {
		tb.Error(err)
	}
	return resp.StatusCode, rb
}

// Operation is one operation of a contract: method, path template (without the server base),
// operationId and its string-valued x- extensions (x-required-role, ...).
type Operation struct {
	Method, Path, ID string
	Extensions       map[string]string
}

// Operations lists every operation of the contract, sorted by path then method.
func (s *Spec) Operations() []Operation {
	var out []Operation
	for _, path := range s.doc.Paths.InMatchingOrder() {
		item := s.doc.Paths.Value(path)
		for method, op := range item.Operations() {
			ext := map[string]string{}
			for k, v := range op.Extensions {
				if str, ok := v.(string); ok {
					ext[k] = str
				}
			}
			out = append(out, Operation{Method: method, Path: path, ID: op.OperationID, Extensions: ext})
		}
	}
	slices.SortFunc(out, func(a, b Operation) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Method, b.Method)
	})
	return out
}

// OperationID returns the operationId of the contract operation matching req's method and path.
func (s *Spec) OperationID(req *http.Request) (string, error) {
	route, _, err := s.router.FindRoute(req)
	if err != nil {
		return "", fmt.Errorf("%s: %s %s is not an operation in the contract: %w", s.name, req.Method, req.URL.Path, err)
	}
	return route.Operation.OperationID, nil
}

// Example returns, as JSON, the application/json response example of operationID for status:
// the single `example` when name is "", else the entry `name` of `examples`. Response $refs
// (components.responses) are followed. It fails tb when there is no such example.
func (s *Spec) Example(tb testing.TB, operationID string, status int, name string) []byte {
	tb.Helper()
	for _, path := range s.doc.Paths.InMatchingOrder() {
		for _, op := range s.doc.Paths.Value(path).Operations() {
			if op.OperationID != operationID {
				continue
			}
			ref := op.Responses.Status(status)
			if ref == nil || ref.Value == nil {
				tb.Fatalf("%s: %s has no %d response", s.name, operationID, status)
			}
			mt := ref.Value.Content.Get("application/json")
			if mt == nil {
				tb.Fatalf("%s: %s %d has no application/json content", s.name, operationID, status)
			}
			var v any
			switch {
			case name == "" && mt.Example != nil:
				v = mt.Example
			case name != "" && mt.Examples[name] != nil && mt.Examples[name].Value != nil:
				v = mt.Examples[name].Value.Value
			case name == "" && len(mt.Examples) > 0: // only named examples: the first by name
				first := slices.Sorted(maps.Keys(mt.Examples))[0]
				if ex := mt.Examples[first]; ex != nil && ex.Value != nil {
					v = ex.Value.Value
				}
			default:
				tb.Fatalf("%s: %s %d has no example %q", s.name, operationID, status, name)
			}
			b, err := json.Marshal(v)
			if err != nil {
				tb.Fatalf("%s: encode example: %v", s.name, err)
			}
			return b
		}
	}
	tb.Fatalf("%s: no operation %s", s.name, operationID)
	return nil
}
