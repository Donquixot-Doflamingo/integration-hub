package isfc

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

// Secret is a credential that never prints: it logs and formats as "***".
type Secret string

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue("***") }

// String hides the value from fmt.
func (Secret) String() string { return "***" }

// GoString hides the value from %#v.
func (Secret) GoString() string { return `"***"` }

// Config configures a Client.
type Config struct {
	// BaseURL is the auth-service base URL (no trailing path), e.g. https://auth.internal.
	BaseURL string
	// ClientID and ClientSecret are the Hub backend client's credentials ("integration-hub").
	ClientID     string
	ClientSecret Secret
	// AuthClientID is the ISFC client id of the auth-service itself: the key the Hub fetches is
	// the one auth-service issued to the Hub (default "auth-service").
	AuthClientID string
	// Timeout bounds one HTTP call (default 5 s). Callers also pass a context deadline.
	Timeout time.Duration
	// HTTPClient replaces the default client (tests). Its own Timeout is left as set.
	HTTPClient *http.Client
	Now        func() time.Time
}

// Client calls the ISFC auth-service as the Hub backend client.
type Client struct {
	cfg  Config
	http *http.Client

	mu     sync.Mutex
	key    Secret
	keyExp time.Time // zero: no expiry known
}

// keySkew refreshes the cached key this long before ISFC says it expires.
const keySkew = 30 * time.Second

// maxBody bounds every response read (a configuration with 10k keys is a few MB).
const maxBody = 32 << 20

// New builds a client. It makes no call until first use.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("isfc: BaseURL must be an absolute http(s) URL")
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("isfc: ClientID and ClientSecret are required")
	}
	cfg.BaseURL = strings.TrimRight(u.String(), "/")
	if cfg.AuthClientID == "" {
		cfg.AuthClientID = "auth-service"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	return &Client{cfg: cfg, http: hc}, nil
}

// APIError is an ISFC error answer: {"error_code":"AS_..","error":"..."}. It carries no request
// data, and its text never includes a credential.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("isfc: status %d code %q: %s", e.Status, e.Code, e.Message)
}

// IsNotFound reports whether err is an ISFC 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// ListTenants returns every ISFC tenant (GET /api/v1/tenant).
func (c *Client) ListTenants(ctx context.Context) ([]Tenant, error) {
	var out []Tenant
	err := c.get(ctx, "/api/v1/tenant", &out)
	return out, err
}

// GetTenant returns one tenant (GET /api/v1/tenant/{id}); IsNotFound when it does not exist.
func (c *Client) GetTenant(ctx context.Context, id string) (Tenant, error) {
	var out Tenant
	err := c.get(ctx, "/api/v1/tenant/"+url.PathEscape(id), &out)
	return out, err
}

// ClientMetadata returns the tenancy and linked tenants of a client
// (GET /api/v1/client/internal/{client_id}).
func (c *Client) ClientMetadata(ctx context.Context, clientID string) (ClientInfo, error) {
	var w struct {
		Status     string         `json:"status"`
		ClientType string         `json:"client_type"`
		Metadata   map[string]any `json:"metadata"`
	}
	if err := c.get(ctx, "/api/v1/client/internal/"+url.PathEscape(clientID), &w); err != nil {
		return ClientInfo{}, err
	}
	info := ClientInfo{Status: w.Status, ClientType: w.ClientType}
	info.Tenancy, _ = w.Metadata["tenancy"].(string)
	if list, ok := w.Metadata["tenants"].([]any); ok {
		for _, v := range list {
			if s, ok := v.(string); ok && s != "" {
				info.Tenants = append(info.Tenants, strings.ToLower(s))
			}
		}
	}
	return info, nil
}

// ClientConfiguration returns the keys a client issued (GET /api/v1/client/configuration/{id}).
// Key values are hashed while decoding.
func (c *Client) ClientConfiguration(ctx context.Context, clientID string) (ClientConfiguration, error) {
	var out ClientConfiguration
	err := c.get(ctx, "/api/v1/client/configuration/"+url.PathEscape(clientID), &out)
	return out, err
}

type envelope struct {
	Success   bool            `json:"success"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	ErrorCode string          `json:"error_code"`
	Error     string          `json:"error"`
}

// get calls path with the S2S key; on 401 or 403 it fetches a new key once and retries.
func (c *Client) get(ctx context.Context, path string, out any) error {
	key, err := c.apiKey(ctx, false)
	if err != nil {
		return err
	}
	data, err := c.once(ctx, path, key)
	var ae *APIError
	if errors.As(err, &ae) && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden) {
		if key, err = c.apiKey(ctx, true); err != nil {
			return err
		}
		data, err = c.once(ctx, path, key)
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("isfc: decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) once(ctx context.Context, path string, key Secret) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-isfc-client-id", c.cfg.ClientID)
	req.Header.Set("x-isfc-api-key", string(key))
	req.Header.Set("Accept", "application/json")
	return c.roundTrip(req)
}

// apiKey returns the cached key, or fetches it. force drops the cache first (after a 401/403),
// but only when another goroutine has not already replaced the key that failed.
func (c *Client) apiKey(ctx context.Context, force bool) (Secret, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.key != "" && (c.keyExp.IsZero() || c.cfg.Now().Add(keySkew).Before(c.keyExp)) {
		return c.key, nil
	}
	q := url.Values{"requested_client_id": {c.cfg.AuthClientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+"/api/v1/client/api-key?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.cfg.ClientID, string(c.cfg.ClientSecret))
	req.Header.Set("Accept", "application/json")
	data, err := c.roundTrip(req)
	if err != nil {
		return "", fmt.Errorf("fetch own api key: %w", err)
	}
	var d struct {
		Key       string    `json:"key"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &d); err != nil || d.Key == "" {
		return "", errors.New("isfc: fetch own api key: no key in the answer")
	}
	c.key, c.keyExp = Secret(d.Key), d.ExpiresAt
	if !c.keyExp.IsZero() && c.keyExp.Year() <= 1 {
		c.keyExp = time.Time{}
	}
	return c.key, nil
}

// roundTrip sends req and returns the envelope's data. Transport errors are reduced to their
// cause: the URL error text would repeat the request URL.
func (c *Client) roundTrip(req *http.Request) (json.RawMessage, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("isfc: %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("isfc: read %s: %w", req.URL.Path, err)
	}
	var env envelope
	jsonErr := json.Unmarshal(body, &env)
	if resp.StatusCode/100 != 2 {
		ae := &APIError{Status: resp.StatusCode}
		if jsonErr == nil {
			ae.Code, ae.Message = env.ErrorCode, env.Error
		}
		if len(ae.Message) > 200 {
			ae.Message = ae.Message[:200]
		}
		return nil, ae
	}
	if jsonErr != nil {
		return nil, fmt.Errorf("isfc: %s: answer is not JSON", req.URL.Path)
	}
	if !env.Success {
		return nil, &APIError{Status: resp.StatusCode, Code: env.ErrorCode, Message: env.Message}
	}
	return env.Data, nil
}
