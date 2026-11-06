package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// adminRoute is one control-plane operation (contracts/openapi/hub-admin.v1.yaml): its method and
// path template below the admin base URL.
type adminRoute struct {
	method string
	path   string // e.g. /integrations/{integration_id}/versions/{version}
}

// adminRoutes holds every hub-admin operation a hubmcp tool maps to, and nothing else.
// TestAdminRoutesMatchTheContract proves each entry equals the OpenAPI document and that the key
// set equals the admin operations in the tool contract's maps_to. There is no DELETE entry, and
// allowedMethod refuses DELETE even if one were added.
var adminRoutes = map[string]adminRoute{
	"searchRequests":             {http.MethodGet, "/requests"},
	"getRequestTimeline":         {http.MethodGet, "/requests/{request_id}/timeline"},
	"listRequestAttempts":        {http.MethodGet, "/requests/{request_id}/attempts"},
	"listBreakers":               {http.MethodGet, "/breakers"},
	"listTenants":                {http.MethodGet, "/tenants"},
	"getTenant":                  {http.MethodGet, "/tenants/{tenant_id}"},
	"createTenant":               {http.MethodPost, "/tenants"},
	"updateTenant":               {http.MethodPatch, "/tenants/{tenant_id}"},
	"createApiKey":               {http.MethodPost, "/api-keys"},
	"listVendors":                {http.MethodGet, "/vendors"},
	"getVendor":                  {http.MethodGet, "/vendors/{vendor_id}"},
	"createVendor":               {http.MethodPost, "/vendors"},
	"updateVendor":               {http.MethodPut, "/vendors/{vendor_id}"},
	"listVendorEndpoints":        {http.MethodGet, "/vendors/{vendor_id}/endpoints"},
	"getVendorEndpoint":          {http.MethodGet, "/vendors/{vendor_id}/endpoints/{endpoint_ref}"},
	"createVendorEndpoint":       {http.MethodPost, "/vendors/{vendor_id}/endpoints"},
	"listAccounts":               {http.MethodGet, "/accounts"},
	"getAccount":                 {http.MethodGet, "/accounts/{account_id}"},
	"updateAccount":              {http.MethodPatch, "/accounts/{account_id}"},
	"listCapabilities":           {http.MethodGet, "/capabilities"},
	"getTransformer":             {http.MethodGet, "/transformers/{transformer_id}"},
	"createTransformerVersion":   {http.MethodPost, "/transformers/{transformer_id}/versions"},
	"runTransformerGoldens":      {http.MethodPost, "/transformers/run-goldens"},
	"dryRunTransformer":          {http.MethodPost, "/transformers/dry-run"},
	"listIntegrations":           {http.MethodGet, "/integrations"},
	"getIntegration":             {http.MethodGet, "/integrations/{integration_id}"},
	"createIntegration":          {http.MethodPost, "/integrations"},
	"getIntegrationVersion":      {http.MethodGet, "/integrations/{integration_id}/versions/{version}"},
	"diffIntegrationVersions":    {http.MethodGet, "/integrations/{integration_id}/diff"},
	"createIntegrationDraft":     {http.MethodPost, "/integrations/{integration_id}/versions"},
	"updateIntegrationDraft":     {http.MethodPut, "/integrations/{integration_id}/versions/{version}"},
	"validateIntegrationVersion": {http.MethodPost, "/integrations/{integration_id}/versions/{version}/validate"},
	"publishIntegrationVersion":  {http.MethodPost, "/integrations/{integration_id}/versions/{version}/publish"},
	"updateIntegrationWeights":   {http.MethodPost, "/integrations/{integration_id}/weights"},
	"rollbackIntegration":        {http.MethodPost, "/integrations/{integration_id}/rollback"},
	"getRegistrarEndpoint":       {http.MethodGet, "/registrar-endpoints/{endpoint_id}"},
	"createRegistrarEndpoint":    {http.MethodPost, "/registrar-endpoints"},
	"updateRegistrarEndpoint":    {http.MethodPatch, "/registrar-endpoints/{endpoint_id}"},
}

// sendableMethods are the only HTTP methods hubmcp ever sends, to the gateway or the control
// plane. DELETE is not one of them (contracts/mcp/README.md: no tool can delete anything).
var sendableMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch}

// allowedMethod reports whether hubmcp may send an HTTP request with this method.
func allowedMethod(m string) bool { return slices.Contains(sendableMethods, m) }

// pathParams lists the {name} placeholders of the route's path template, in order.
func (r adminRoute) pathParams() []string {
	var out []string
	rest := r.path
	for {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			return out
		}
		out = append(out, rest[i+1:i+j])
		rest = rest[i+j+1:]
	}
}

// adminClient calls the control plane (hub-admin.v1.yaml) with the signed-in user's own OIDC
// token. It can only reach the operations in its allowlist, which is built from the maps_to of
// the tools registered on this server: over Streamable HTTP, where write tools are not
// registered, it cannot send a single mutating request. It has no way to send a DELETE.
type adminClient struct {
	base      string // e.g. http://localhost:8081/admin/v1, no trailing slash
	token     secret
	http      *http.Client
	userAgent string
	allowed   map[string]adminRoute // operationId -> route
}

// newAdminClient builds the client for the given operations (operationId -> the method the
// contract's maps_to declares). Every operation must be in adminRoutes with exactly that method,
// and the method must be sendable; otherwise hubmcp refuses to start.
func newAdminClient(base string, token secret, hc *http.Client, userAgent string, ops map[string]string) (*adminClient, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.User != nil {
		return nil, errors.New("HUB_ADMIN_BASE_URL must be an absolute http or https URL without query or user info")
	}
	allowed := make(map[string]adminRoute, len(ops))
	for op, method := range ops {
		r, ok := adminRoutes[op]
		switch {
		case !ok:
			return nil, fmt.Errorf("admin operation %s has no route in hubmcp", op)
		case r.method != method:
			return nil, fmt.Errorf("admin operation %s: the contract maps it as %s, hubmcp's route is %s", op, method, r.method)
		case !allowedMethod(r.method):
			return nil, fmt.Errorf("admin operation %s: HTTP method %s is never sent by hubmcp", op, r.method)
		}
		allowed[op] = r
	}
	return &adminClient{base: u.String(), token: token, http: hc, userAgent: userAgent, allowed: allowed}, nil
}

// adminError is a failed admin call: the AdminError object of the envelope (passed through
// verbatim), or the transport error when the control plane could not be reached.
type adminError struct {
	Status int
	Object json.RawMessage // AdminError; nil when none was received
	Err    error           // transport failure; nil when the admin answered
}

func (e *adminError) Error() string {
	if e.Err != nil {
		return "admin API: " + e.Err.Error()
	}
	return "admin API: HTTP " + strconv.Itoa(e.Status)
}

func (e *adminError) Unwrap() error { return e.Err }

// errOperationNotAllowed is returned for an operation outside the client's allowlist. No request
// is sent.
var errOperationNotAllowed = errors.New("operation is not allowed for this hubmcp server")

const maxAdminResponse = 16 << 20

// adminCall is one control-plane request.
type adminCall struct {
	op      string            // operationId; must be in the allowlist
	path    map[string]string // path parameters (raw values, escaped here)
	query   url.Values
	body    []byte // JSON request body; nil for GET
	ifMatch string // If-Match header (update tools)
}

// adminReply is a 2xx answer.
type adminReply struct {
	status int
	body   []byte
	etag   string
}

// do sends one allowlisted operation and returns its 2xx answer. Any other status, a transport
// failure or an operation outside the allowlist is an *adminError.
func (c *adminClient) do(ctx context.Context, call adminCall) (*adminReply, error) {
	r, ok := c.allowed[call.op]
	if !ok || !allowedMethod(r.method) {
		return nil, &adminError{Err: fmt.Errorf("%s: %w", call.op, errOperationNotAllowed)}
	}
	path := r.path
	for _, name := range r.pathParams() {
		v, ok := call.path[name]
		if !ok || v == "" {
			return nil, &adminError{Err: fmt.Errorf("%s: missing path parameter %s", call.op, name)}
		}
		if v == "." || v == ".." { // PathEscape keeps dot segments; they would leave the route
			return nil, &adminError{Err: fmt.Errorf("%s: invalid path parameter %s", call.op, name)}
		}
		path = strings.Replace(path, "{"+name+"}", url.PathEscape(v), 1)
	}
	target := c.base + path
	if len(call.query) > 0 {
		target += "?" + call.query.Encode()
	}
	var body io.Reader
	if call.body != nil {
		body = bytes.NewReader(call.body)
	}
	// The host is HUB_ADMIN_BASE_URL (operator configuration); tool input only fills escaped path
	// segments of an allowlisted route and query values, so it cannot change the destination.
	req, err := http.NewRequestWithContext(ctx, r.method, target, body) // #nosec G704 -- host from HUB_ADMIN_BASE_URL; input only in escaped path segments and query values of an allowlisted route
	if err != nil {
		return nil, &adminError{Err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Authorization", "Bearer "+string(c.token))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if call.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if call.ifMatch != "" {
		req.Header.Set("If-Match", call.ifMatch)
	}
	resp, err := c.http.Do(req) // #nosec G704 -- same request as above: fixed host, allowlisted route, method-guarded client
	if err != nil {
		return nil, &adminError{Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAdminResponse+1))
	if err != nil {
		return nil, &adminError{Err: fmt.Errorf("read response: %w", err)}
	}
	if len(b) > maxAdminResponse {
		return nil, &adminError{Err: fmt.Errorf("response exceeds %d bytes", maxAdminResponse)}
	}
	if resp.StatusCode/100 != 2 {
		var env struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(b, &env) == nil && len(env.Error) > 0 && env.Error[0] == '{' {
			return nil, &adminError{Status: resp.StatusCode, Object: env.Error}
		}
		return nil, &adminError{Status: resp.StatusCode}
	}
	return &adminReply{status: resp.StatusCode, body: b, etag: resp.Header.Get("ETag")}, nil
}

// get performs one allowlisted GET and decodes the answer into out.
func (c *adminClient) get(ctx context.Context, op string, path map[string]string, query url.Values, out any) error {
	rep, err := c.do(ctx, adminCall{op: op, path: path, query: query})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(rep.body, out); err != nil {
		return &adminError{Status: rep.status, Err: fmt.Errorf("decode response: %w", err)}
	}
	return nil
}

// methodGuard is the RoundTripper of every HTTP client hubmcp uses (caller API and control
// plane). It refuses any method outside sendableMethods before anything leaves the process, so
// no code path in hubmcp (including pkg/hubclient) can issue an HTTP DELETE.
type methodGuard struct{ next http.RoundTripper }

func (g methodGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	if !allowedMethod(r.Method) {
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, fmt.Errorf("hubmcp never sends HTTP %s requests", r.Method)
	}
	return g.next.RoundTrip(r)
}

// guardedClient returns a copy of hc whose transport is wrapped by methodGuard.
func guardedClient(hc *http.Client) *http.Client {
	c := *hc
	next := c.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	if _, ok := next.(methodGuard); !ok {
		c.Transport = methodGuard{next: next}
	}
	return &c
}
