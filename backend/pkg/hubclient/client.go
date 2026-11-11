package hubclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// Version is the SDK version sent in the User-Agent header (hubclient-go/<Version>).
const Version = "0.1.0"

// maxResponseBytes bounds how much of a response body the client reads. Canonical responses are
// far smaller (the Hub caps request bodies at 1 MB).
const maxResponseBytes = 16 << 20

// Client calls the Hub caller API (contracts/openapi/hub-public.v1.yaml). It is safe for
// concurrent use and immutable after New.
//
// The API key is held privately: it is sent only in the Authorization header and never appears
// in errors, in String/GoString output or in slog values.
type Client struct {
	baseURL    *url.URL
	apiKey     string
	httpClient *http.Client
	userAgent  string
	isfc       *ISFCCredential // set by WithISFC / WithISFCKey; then apiKey is empty
	isfcCfg    *isfcConfig
	tenantID   string
}

// Option configures a Client in New.
type Option func(*Client)

// WithHTTPClient sets the HTTP client used for every call (transport, TLS, proxies). Leave its
// Timeout at zero and bound calls with the context instead. A nil client is ignored.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithUserAgent appends product to the User-Agent header, after hubclient-go/<Version>, so the
// Hub's logs show which service is calling (e.g. "orders-service/2.3.1").
func WithUserAgent(product string) Option {
	return func(c *Client) {
		if p := strings.TrimSpace(product); p != "" {
			c.userAgent = "hubclient-go/" + Version + " " + p
		}
	}
}

// New returns a client for the Hub at baseURL (for example "https://hub.prod.internal") that
// authenticates with the tenant API key apiKey. A path in baseURL is kept as a prefix. With
// WithISFC or WithISFCKey the client authenticates as an ISFC backend client instead and apiKey
// must be empty (the Hub rejects a request that carries both kinds of credential).
func New(baseURL, apiKey string, opts ...Option) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("hubclient: parse base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("hubclient: base URL must be an absolute http or https URL")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("hubclient: base URL must not carry a query, fragment or user info")
	}
	c := &Client{
		baseURL:    u,
		apiKey:     apiKey,
		httpClient: &http.Client{},
		userAgent:  "hubclient-go/" + Version,
	}
	for _, o := range opts {
		o(c)
	}
	switch {
	case c.isfcCfg != nil && strings.TrimSpace(apiKey) != "":
		return nil, errors.New("hubclient: use either an API key or an ISFC credential, not both")
	case c.isfcCfg != nil:
		if c.isfc, err = c.isfcCfg.build(c.httpClient); err != nil {
			return nil, err
		}
	case strings.TrimSpace(apiKey) == "":
		return nil, errors.New("hubclient: API key is empty")
	}
	c.isfcCfg = nil // holds the secret; the credential keeps it from here
	return c, nil
}

// String describes the client without its API key.
func (c *Client) String() string {
	if c.isfc != nil {
		return "hubclient.Client{BaseURL: " + c.baseURL.String() + ", ISFCClientID: " + c.isfc.clientID + ", ISFCKey: [redacted]}"
	}
	return "hubclient.Client{BaseURL: " + c.baseURL.String() + ", APIKey: [redacted]}"
}

// GoString is String, so %#v does not print the API key either.
func (c *Client) GoString() string { return c.String() }

// LogValue keeps the API key out of slog output.
func (c *Client) LogValue() slog.Value {
	if c.isfc != nil {
		return slog.GroupValue(slog.String("base_url", c.baseURL.String()), slog.String("isfc_client_id", c.isfc.clientID), slog.String("isfc_key", "[redacted]"))
	}
	return slog.GroupValue(slog.String("base_url", c.baseURL.String()), slog.String("api_key", "[redacted]"))
}

// call is one HTTP exchange with the Hub.
type call struct {
	method string
	path   string // already escaped, starting with /v1/
	query  url.Values
	body   []byte // nil for no body
	header http.Header
}

// response is a fully read Hub response.
type response struct {
	status    int
	header    http.Header
	body      []byte
	requestID string
}

// do sends cl and reads the whole response. Transport and context errors are returned wrapped
// (errors.Is(err, context.Canceled) works); any status is returned as a response.
//
// With an ISFC credential a 401 or 403 makes the client fetch a new key and send the call once
// more, as the ISFC auth libraries do; a key that did not change is not retried.
func (c *Client) do(ctx context.Context, cl call) (*response, error) {
	if c.isfc == nil {
		return c.exchange(ctx, cl, "")
	}
	key, err := c.isfc.Key(ctx, "")
	if err != nil {
		return nil, err
	}
	resp, err := c.exchange(ctx, cl, key)
	if err != nil || (resp.status != http.StatusUnauthorized && resp.status != http.StatusForbidden) {
		return resp, err
	}
	fresh, ferr := c.isfc.Key(ctx, key)
	if ferr != nil {
		return resp, nil //nolint:nilerr // report the Hub's answer, not the refresh failure
	}
	if fresh == key {
		return resp, nil
	}
	return c.exchange(ctx, cl, fresh)
}

// exchange is one HTTP round trip; isfcKey is the x-isfc-api-key to send, empty for a tenant key.
func (c *Client) exchange(ctx context.Context, cl call, isfcKey string) (*response, error) {
	// The base URL has no query or fragment (New checks), so the escaped path appends cleanly.
	target := c.baseURL.String() + cl.path
	if len(cl.query) > 0 {
		target += "?" + cl.query.Encode()
	}
	var body io.Reader
	if cl.body != nil {
		body = bytes.NewReader(cl.body)
	}
	req, err := http.NewRequestWithContext(ctx, cl.method, target, body)
	if err != nil {
		return nil, fmt.Errorf("hubclient: build %s %s: %w", cl.method, cl.path, err)
	}
	for k, vs := range cl.header {
		req.Header[k] = vs
	}
	if c.isfc != nil {
		req.Header.Set("x-isfc-client-id", c.isfc.clientID)
		req.Header.Set("x-isfc-api-key", isfcKey)
		if c.tenantID != "" && req.Header.Get("X-Tenant-Id") == "" {
			req.Header.Set("X-Tenant-Id", c.tenantID)
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if cl.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// *url.Error carries method and URL only; the key is in a header and never in the URL.
		return nil, fmt.Errorf("hubclient: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("hubclient: read %s %s response: %w", cl.method, cl.path, err)
	}
	if len(b) > maxResponseBytes {
		return nil, fmt.Errorf("hubclient: %s %s response exceeds %d bytes", cl.method, cl.path, maxResponseBytes)
	}
	return &response{
		status:    resp.StatusCode,
		header:    resp.Header,
		body:      b,
		requestID: resp.Header.Get("X-Hub-Request-Id"),
	}, nil
}

// getJSON performs a GET and decodes a 200 body into v. Any other status becomes an *Error.
func (c *Client) getJSON(ctx context.Context, path string, query url.Values, v any) error {
	resp, err := c.do(ctx, call{method: http.MethodGet, path: path, query: query})
	if err != nil {
		return err
	}
	if resp.status != http.StatusOK {
		return resp.unexpected()
	}
	return resp.decode(v)
}

// decode unmarshals a success body, naming the request id on failure.
func (r *response) decode(v any) error {
	if err := json.Unmarshal(r.body, v); err != nil {
		return fmt.Errorf("hubclient: decode %d response (request %s): %w", r.status, r.requestID, err)
	}
	return nil
}

// unexpected turns a status the operation does not treat as success into an error: an *Error
// for 4xx/5xx, a plain error for anything else (1xx/3xx or an undeclared 2xx).
func (r *response) unexpected() error {
	if r.status >= 400 {
		return decodeError(r.status, r.header, r.body)
	}
	return fmt.Errorf("hubclient: unexpected HTTP %d response (request %s)", r.status, r.requestID)
}

// marshalBody turns a canonical body into JSON. json.RawMessage and []byte are sent as they are
// and must already be JSON.
func marshalBody(body any) ([]byte, error) {
	var b []byte
	switch v := body.(type) {
	case nil:
		return nil, errors.New("hubclient: request body is required")
	case json.RawMessage:
		b = v
	case []byte:
		b = v
	default:
		var err error
		if b, err = json.Marshal(v); err != nil {
			return nil, fmt.Errorf("hubclient: encode request body: %w", err)
		}
	}
	if !json.Valid(b) {
		return nil, errors.New("hubclient: request body is not valid JSON")
	}
	return b, nil
}

// segment escapes one path segment and rejects empty values.
func segment(name, v string) (string, error) {
	if v == "" {
		return "", fmt.Errorf("hubclient: %s is empty", name)
	}
	return url.PathEscape(v), nil
}
