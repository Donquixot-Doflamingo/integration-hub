package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient"
)

// mcpPath is where the Streamable HTTP endpoint is served.
const mcpPath = "/mcp"

// keyCheckTTL is how long a tenant key the Hub accepted is trusted without asking again.
const keyCheckTTL = 60 * time.Second

// maxCachedKeys bounds the verified-key cache; it is cleared when full.
const maxCachedKeys = 1024

// TokenInfo.Extra entries of an authenticated HTTP request: the client's hubclient and a
// redactor for its key (the raw key itself is not kept there).
const (
	tokenExtraClient  = "hubmcp.hubclient" // #nosec G101 -- a context map key naming the client, not a credential
	tokenExtraSecrets = "hubmcp.secrets"   // #nosec G101 -- a context map key, not a credential value
)

// httpAuth authenticates Streamable HTTP clients (HUB-301, SECURITY §2.5): every request carries
// `Authorization: Bearer <tenant API key>`, the key the tools then use for that client. hubmcp
// does not store keys: it asks the Hub (listIntegrations, limit 1) whether the key is valid and
// remembers only a SHA-256 fingerprint of accepted keys for keyCheckTTL.
//
// An ISFC backend client authenticates with `x-isfc-client-id` and `x-isfc-api-key` instead
// (contract 2.1.0, optionally `X-Tenant-Id`); httpHandler turns them into a bearer token of the
// form isfcToken so the same verification, cache and redaction apply. A server started with
// HUB_ISFC_CLIENT_ID and HUB_ISFC_API_KEY on a loopback listener uses that credential for a
// request that brings none.
type httpAuth struct {
	newClient   func(key string) (*hubclient.Client, error)
	newISFC     func(clientID, apiKey, tenant string) (*hubclient.Client, error)
	defaultISFC *isfcDefault
	now         func() time.Time

	mu       sync.Mutex
	verified map[[32]byte]verifiedKey
}

// isfcDefault is the environment credential of a loopback HTTP server.
type isfcDefault struct{ clientID, key, tenant string }

// Bearer tokens that stand for an ISFC credential. isfcEnvToken marks "use the server's own";
// isfcPrefix tokens carry base64url(client id) "." base64url(tenant) "." key.
const (
	isfcPrefix   = "isfc."
	isfcEnvToken = "isfc-env"
)

func isfcToken(clientID, tenant, key string) string {
	e := base64.RawURLEncoding
	return isfcPrefix + e.EncodeToString([]byte(clientID)) + "." + e.EncodeToString([]byte(tenant)) + "." + key
}

// parseISFCToken is the inverse of isfcToken.
func parseISFCToken(tok string) (clientID, tenant, key string, ok bool) {
	parts := strings.SplitN(strings.TrimPrefix(tok, isfcPrefix), ".", 3)
	if !strings.HasPrefix(tok, isfcPrefix) || len(parts) != 3 || parts[2] == "" {
		return "", "", "", false
	}
	id, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	tn, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	if err1 != nil || err2 != nil || len(id) == 0 {
		return "", "", "", false
	}
	return string(id), string(tn), parts[2], true
}

// isfcCredentials rewrites ISFC headers into a bearer token. A request that already carries an
// Authorization header next to ISFC headers is a 400, as at the Hub.
func (h *httpAuth) isfcCredentials(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, key := strings.TrimSpace(r.Header.Get("x-isfc-client-id")), strings.TrimSpace(r.Header.Get("x-isfc-api-key"))
		switch {
		case r.Header.Get("Authorization") != "" && (id != "" || key != ""):
			http.Error(w, "send one credential: a tenant API key or an ISFC client id and key", http.StatusBadRequest)
			return
		case r.Header.Get("Authorization") != "":
			// legacy tenant key (or a bad scheme, which verification refuses)
		case id != "" && key != "":
			r = r.Clone(r.Context())
			r.Header.Set("Authorization", "Bearer "+isfcToken(id, strings.TrimSpace(r.Header.Get("X-Tenant-Id")), key))
		case id == "" && key == "" && h.defaultISFC != nil:
			r = r.Clone(r.Context())
			r.Header.Set("Authorization", "Bearer "+isfcEnvToken)
		}
		r.Header.Del("x-isfc-api-key") // never reaches the MCP layer
		next.ServeHTTP(w, r)
	})
}

type verifiedKey struct {
	client  *hubclient.Client
	secrets *secretSet
	expires time.Time
}

func (h *httpAuth) verify(ctx context.Context, key string, _ *http.Request) (*auth.TokenInfo, error) {
	build := func() (*hubclient.Client, secret, error) {
		switch {
		case key == isfcEnvToken && h.defaultISFC != nil && h.newISFC != nil:
			d := h.defaultISFC
			c, err := h.newISFC(d.clientID, d.key, d.tenant)
			return c, secret(d.key), err
		case strings.HasPrefix(key, isfcPrefix) && h.newISFC != nil:
			id, tenant, k, ok := parseISFCToken(key)
			if !ok {
				return nil, "", auth.ErrInvalidToken
			}
			c, err := h.newISFC(id, k, tenant)
			return c, secret(k), err
		}
		c, err := h.newClient(key)
		return c, secret(key), err
	}
	sum := sha256.Sum256([]byte(key))
	info := func(v verifiedKey) *auth.TokenInfo {
		// UserID binds MCP sessions to the key (the SDK rejects a session id presented with
		// another key); it is a fingerprint, never the key.
		return &auth.TokenInfo{UserID: "key-" + hex.EncodeToString(sum[:8]),
			Extra: map[string]any{tokenExtraClient: v.client, tokenExtraSecrets: v.secrets}}
	}
	h.mu.Lock()
	if v, ok := h.verified[sum]; ok && h.now().Before(v.expires) {
		h.mu.Unlock()
		return info(v), nil
	}
	h.mu.Unlock()

	c, credSecret, err := build()
	if err != nil {
		return nil, auth.ErrInvalidToken
	}
	if _, err := c.ListIntegrations(ctx, hubclient.ListOptions{Limit: 1}); err != nil {
		var he *hubclient.Error
		if errors.As(err, &he) && (he.Code == hubclient.CodeUnauthorized || he.Code == hubclient.CodeForbidden) {
			return nil, auth.ErrInvalidToken
		}
		return nil, fmt.Errorf("verify key with the Hub: %w", err) // 500: the Hub is unreachable
	}
	v := verifiedKey{client: c, secrets: newSecretSet(credSecret), expires: h.now().Add(keyCheckTTL)}
	h.mu.Lock()
	if len(h.verified) >= maxCachedKeys {
		clear(h.verified)
	}
	h.verified[sum] = v
	h.mu.Unlock()
	return info(v), nil
}

// sessionAuth returns the hubclient, key redactor and key fingerprint of an authenticated HTTP
// request, or zero values for stdio.
func sessionAuth(req *mcp.CallToolRequest) (*hubclient.Client, *secretSet, string) {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return nil, nil, ""
	}
	ti := req.Extra.TokenInfo
	c, _ := ti.Extra[tokenExtraClient].(*hubclient.Client)
	s, _ := ti.Extra[tokenExtraSecrets].(*secretSet)
	return c, s, ti.UserID
}

// httpHandler serves srv over Streamable HTTP at /mcp behind cross-origin protection and bearer
// authentication; unauthenticated requests get 401 before they reach the MCP layer. The SDK's
// DNS-rebinding protection for loopback listeners stays on.
func httpHandler(srv *mcp.Server, a *httpAuth, log *slog.Logger) http.Handler {
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Logger: log})
	authed := a.isfcCredentials(auth.RequireBearerToken(a.verify, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(streamable))
	mux := http.NewServeMux()
	mux.Handle(mcpPath, http.NewCrossOriginProtection().Handler(authed))
	return mux
}

// serveHTTP serves h on ln until ctx ends, then shuts down gracefully (open streams get 5 s).
func serveHTTP(ctx context.Context, ln net.Listener, h http.Handler, log *slog.Logger) error {
	hs := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	log.InfoContext(ctx, "hubmcp listening", slog.String("addr", ln.Addr().String()), slog.String("path", mcpPath))
	select {
	case err := <-errc:
		return fmt.Errorf("serve http: %w", err)
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := hs.Shutdown(sctx); err != nil {
		_ = hs.Close()
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve http: %w", err)
	}
	return nil
}

// isLoopback reports whether addr (host:port) binds only to a loopback interface.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
