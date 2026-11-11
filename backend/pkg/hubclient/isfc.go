package hubclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ISFCHubClientID is the ISFC client that owns the Hub's API keys: a caller asks the auth-service
// for "the key integration-hub issued to me".
const ISFCHubClientID = "integration-hub"

// isfcKeySkew refreshes a key this long before its expiry.
const isfcKeySkew = 30 * time.Second

// isfcFetchTimeout bounds one key fetch when the context has no deadline.
const isfcFetchTimeout = 15 * time.Second

// ISFCError is a failed key fetch: the auth-service answered with an error or could not be
// reached. It never carries the client secret or a key.
type ISFCError struct {
	Status  int    // HTTP status; 0 when the auth-service could not be reached
	Code    string // the auth-service's error_code, for example AS_VE_09
	Message string
	Err     error // transport failure
}

func (e *ISFCError) Error() string {
	if e.Err != nil {
		return "hubclient: ISFC key fetch: " + e.Err.Error()
	}
	s := fmt.Sprintf("hubclient: ISFC key fetch: HTTP %d", e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

func (e *ISFCError) Unwrap() error { return e.Err }

// ISFCCredential is the ISFC backend-client credential a caller presents to the Hub: its client
// id and the API key `integration-hub` issued to it. It does what the ISFC auth libraries do: it
// fetches the key from the auth-service with the client's own id and secret, caches it, and
// replaces it when the Hub rejects it. It is safe for concurrent use.
//
// The secret and the key are never part of an error, a formatted value or a slog value.
type ISFCCredential struct {
	clientID string
	secret   string // empty for a static key
	fetchURL string
	http     *http.Client
	now      func() time.Time

	mu  sync.Mutex
	key string
	exp time.Time
}

// NewISFCCredential returns a credential that fetches its key from the auth-service at
// authBaseURL (for example "https://auth.example.com") using clientID and secret. hc may be nil.
// Nothing is fetched until the first Key call.
func NewISFCCredential(clientID, secret, authBaseURL string, hc *http.Client) (*ISFCCredential, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(secret) == "" {
		return nil, errors.New("hubclient: ISFC client id and secret are required")
	}
	u, err := url.Parse(strings.TrimRight(authBaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("hubclient: parse ISFC auth base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("hubclient: ISFC auth base URL must be an absolute http or https URL without query, fragment or user info")
	}
	if hc == nil {
		hc = &http.Client{}
	}
	q := url.Values{"requested_client_id": {ISFCHubClientID}}
	return &ISFCCredential{clientID: clientID, secret: secret, http: hc, now: time.Now,
		fetchURL: u.String() + "/api/v1/client/api-key?" + q.Encode()}, nil
}

// NewISFCKeyCredential returns a credential for a key the caller already holds (for example one
// an operator configured). It is never refreshed.
func NewISFCKeyCredential(clientID, apiKey string) (*ISFCCredential, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("hubclient: ISFC client id and API key are required")
	}
	return &ISFCCredential{clientID: clientID, key: apiKey, now: time.Now}, nil
}

// ClientID is the value of the x-isfc-client-id header.
func (c *ISFCCredential) ClientID() string { return c.clientID }

// Key returns the API key to send as x-isfc-api-key. It is fetched on first use and cached.
// Pass the key the Hub just rejected as stale to get a replacement: the cache is dropped and the
// key fetched again, unless another goroutine already replaced it. A static credential returns
// its key unchanged.
func (c *ISFCCredential) Key(ctx context.Context, stale string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fresh := c.key != "" && (c.exp.IsZero() || c.now().Add(isfcKeySkew).Before(c.exp))
	if c.secret == "" || (fresh && (stale == "" || stale != c.key)) {
		return c.key, nil
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, isfcFetchTimeout)
		defer cancel()
	}
	key, exp, err := c.fetch(ctx)
	if err != nil {
		return "", err
	}
	c.key, c.exp = key, exp
	return key, nil
}

func (c *ISFCCredential) fetch(ctx context.Context) (string, time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.fetchURL, nil)
	if err != nil {
		return "", time.Time{}, &ISFCError{Err: errors.New("build request")}
	}
	req.SetBasicAuth(c.clientID, c.secret)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) { // the URL error text would repeat the request URL
			err = ue.Err
		}
		return "", time.Time{}, &ISFCError{Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", time.Time{}, &ISFCError{Err: err}
	}
	var env struct {
		Success   *bool  `json:"success"`
		Message   string `json:"message"`
		ErrorCode string `json:"error_code"`
		Error     string `json:"error"`
		Data      struct {
			Key       string    `json:"key"`
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"data"`
	}
	jsonErr := json.Unmarshal(b, &env)
	if resp.StatusCode/100 != 2 {
		e := &ISFCError{Status: resp.StatusCode}
		if jsonErr == nil {
			e.Code, e.Message = env.ErrorCode, env.Error
			if len(e.Message) > 200 {
				e.Message = e.Message[:200]
			}
		}
		return "", time.Time{}, e
	}
	if jsonErr != nil || env.Data.Key == "" || (env.Success != nil && !*env.Success) {
		return "", time.Time{}, &ISFCError{Status: resp.StatusCode, Message: "no key in the answer"}
	}
	exp := env.Data.ExpiresAt
	if exp.Year() <= 1 {
		exp = time.Time{}
	}
	return env.Data.Key, exp, nil
}

// String describes the credential without its secret or key.
func (c *ISFCCredential) String() string {
	return "hubclient.ISFCCredential{ClientID: " + c.clientID + ", Secret: [redacted]}"
}

// GoString is String, so %#v does not print the secret or the key.
func (c *ISFCCredential) GoString() string { return c.String() }

// LogValue keeps the secret and the key out of slog output.
func (c *ISFCCredential) LogValue() slog.Value {
	return slog.GroupValue(slog.String("client_id", c.clientID), slog.String("secret", "[redacted]"))
}

// WithISFC makes the client authenticate as the ISFC backend client clientID instead of with a
// tenant API key: every call carries x-isfc-client-id and x-isfc-api-key (contract scheme
// isfcS2S). The key is fetched from the auth-service at authBaseURL with clientID and secret,
// cached, and fetched again once when the Hub answers 401 or 403, then the call is retried once.
// Pass an empty apiKey to New. The auth-service is called with the client set by WithHTTPClient
// (default http.Client), never on the Hub's request path of another caller.
func WithISFC(clientID, secret, authBaseURL string) Option {
	return func(c *Client) { c.isfcCfg = &isfcConfig{clientID: clientID, secret: secret, authBaseURL: authBaseURL} }
}

// WithISFCKey is WithISFC for a key the caller already holds: it is sent as it is and never
// refreshed. Use it where the credential is supplied per request (for example hubmcp over HTTP).
func WithISFCKey(clientID, apiKey string) Option {
	return func(c *Client) { c.isfcCfg = &isfcConfig{clientID: clientID, apiKey: apiKey} }
}

// WithTenantID sends X-Tenant-Id (an ISFC tenant UUID) on every call. It is needed only when the
// ISFC credential covers more than one tenant, and is ignored with a Hub tenant API key.
func WithTenantID(id string) Option {
	return func(c *Client) { c.tenantID = strings.TrimSpace(id) }
}

type isfcConfig struct {
	clientID, secret, authBaseURL, apiKey string
}

// build turns the option into a credential, once the HTTP client is final.
func (cfg *isfcConfig) build(hc *http.Client) (*ISFCCredential, error) {
	if cfg.apiKey != "" || cfg.authBaseURL == "" && cfg.secret == "" {
		return NewISFCKeyCredential(cfg.clientID, cfg.apiKey)
	}
	return NewISFCCredential(cfg.clientID, cfg.secret, cfg.authBaseURL, hc)
}
