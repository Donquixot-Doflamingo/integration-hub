package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
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
type httpAuth struct {
	newClient func(key string) (*hubclient.Client, error)
	now       func() time.Time

	mu       sync.Mutex
	verified map[[32]byte]verifiedKey
}

type verifiedKey struct {
	client  *hubclient.Client
	secrets *secretSet
	expires time.Time
}

func (h *httpAuth) verify(ctx context.Context, key string, _ *http.Request) (*auth.TokenInfo, error) {
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

	c, err := h.newClient(key)
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
	v := verifiedKey{client: c, secrets: newSecretSet(secret(key)), expires: h.now().Add(keyCheckTTL)}
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
	authed := auth.RequireBearerToken(a.verify, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(streamable)
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
