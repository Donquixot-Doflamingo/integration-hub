// Package mockisfc is a fake ISFC auth-service for tests and local runs (AUTH_PLAN §16, §18). It
// serves the endpoints the Hub uses, with the real response shapes (envelope
// {"success","message","data"}, errors {"error_code","error"}):
//
//	GET|POST /api/v1/client/api-key          fetch a key with client credentials (basic auth or JSON)
//	GET      /api/v1/tenant[/{id}]           tenants
//	GET      /api/v1/client/internal/{id}    client metadata (tenancy, ACTIVE tenants in panel order)
//	GET      /api/v1/client/configuration/{id}  keys the client issued (plaintext, as ISFC does today)
//	POST     /oauth2/token                   client_credentials: an RS256 access token
//	GET      /.well-known/jwks.json          the RS256 public key
//
// Management calls need x-isfc-client-id and the key auth-service issued to that client. State is
// changed from Go (Server methods) or, for cmd/mockisfc, through /_mock/*.
//
// No real ISFC, Hydra or Entra is ever called: every ISFC test in the Hub runs against this.
package mockisfc

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Options configures a Server. The zero value is usable.
type Options struct {
	// AuthClientID is the ISFC client id of the auth-service (default "auth-service").
	AuthClientID string
	// Issuer is the iss claim of minted tokens (default "http://mockisfc").
	Issuer string
	// KeyTTL is how long an issued auth-service key lives; 0 means no expiry.
	KeyTTL time.Duration
	Now    func() time.Time
}

// Tenant is a mock tenant.
type Tenant struct {
	ID, Key, Name, Status string
	UpdatedAt             time.Time
}

type client struct {
	id, secret string
	status     string
	clientType string
	tenancy    string
	tenants    []string
}

// Key is an API key a client issued to a caller client.
type Key struct {
	ID          string
	Name        string
	Plaintext   string
	Owner       string // issuing client
	Caller      string // client the key was issued to
	Status      string
	Roles       []string
	Permissions []string
	Expires     time.Time
	UpdatedAt   time.Time
}

// Server is the fake auth-service.
type Server struct {
	opts Options
	mu   sync.Mutex

	tenants map[string]*Tenant
	clients map[string]*client
	keys    []*Key
	down    bool
	calls   map[string]int // "METHOD /route" -> count

	rsa *rsa.PrivateKey
	kid string
}

// New builds a server with the auth-service client registered.
func New(o Options) *Server {
	if o.AuthClientID == "" {
		o.AuthClientID = "auth-service"
	}
	if o.Issuer == "" {
		o.Issuer = "http://mockisfc"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(fmt.Sprintf("mockisfc: rsa key: %v", err))
	}
	s := &Server{opts: o, tenants: map[string]*Tenant{}, clients: map[string]*client{}, calls: map[string]int{}, rsa: pk, kid: "mockisfc-1"}
	s.AddClient(o.AuthClientID, "auth-secret", "backend_services")
	return s
}

// ---- state (Go API) ----

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func randKey() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return "isfc_" + hex.EncodeToString(b[:])
}

// AddTenant creates an ACTIVE tenant and returns its UUID.
func (s *Server) AddTenant(key, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newUUID()
	s.tenants[id] = &Tenant{ID: id, Key: key, Name: name, Status: "ACTIVE", UpdatedAt: s.opts.Now().UTC()}
	return id
}

// RenameTenant changes a tenant's name and key.
func (s *Server) RenameTenant(id, key, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.tenants[id]; t != nil {
		t.Key, t.Name, t.UpdatedAt = key, name, s.opts.Now().UTC()
	}
}

// SetTenantStatus sets ACTIVE or INACTIVE.
func (s *Server) SetTenantStatus(id, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.tenants[id]; t != nil {
		t.Status, t.UpdatedAt = status, s.opts.Now().UTC()
	}
}

// RemoveTenant deletes a tenant (not possible in real ISFC today; used to test absent tenants).
func (s *Server) RemoveTenant(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tenants, id)
}

// AddClient registers a client (backend_services, web, ...). tenancy is "none" until
// SetClientTenants.
func (s *Server) AddClient(id, secret, clientType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[id] = &client{id: id, secret: secret, status: "ACTIVE", clientType: clientType, tenancy: "none"}
}

// SetClientTenants sets the tenants linked to a client, in panel order, and makes it tenancy
// required (none when empty).
func (s *Server) SetClientTenants(clientID string, tenantIDs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.clients[clientID]; c != nil {
		c.tenants = slices.Clone(tenantIDs)
		c.tenancy = "required"
		if len(tenantIDs) == 0 {
			c.tenancy = "none"
		}
	}
}

// IssueKey makes owner issue an ACTIVE key to caller with the given permissions and returns the
// key id and its plaintext (what the caller's auth library would fetch).
func (s *Server) IssueKey(owner, caller string, permissions ...string) (id, plaintext string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := &Key{ID: newUUID(), Name: "key-" + caller, Plaintext: randKey(), Owner: owner, Caller: caller, Status: "ACTIVE",
		Permissions: slices.Clone(permissions), UpdatedAt: s.opts.Now().UTC()}
	s.keys = append(s.keys, k)
	return k.ID, k.Plaintext
}

// SetKeyStatus sets ACTIVE or INACTIVE; an INACTIVE key leaves the client configuration.
func (s *Server) SetKeyStatus(keyID, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		if k.ID == keyID {
			k.Status, k.UpdatedAt = status, s.opts.Now().UTC()
		}
	}
}

// RevokeKey is SetKeyStatus(INACTIVE).
func (s *Server) RevokeKey(keyID string) { s.SetKeyStatus(keyID, "INACTIVE") }

// RotateAuthKeys deactivates every key auth-service issued, so the next management call with a
// cached key gets 401 and the client must fetch a new one (tests the refresh-once rule).
func (s *Server) RotateAuthKeys() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		if k.Owner == s.opts.AuthClientID {
			k.Status = "INACTIVE"
		}
	}
}

// SetDown makes every endpoint answer 503 (ISFC unreachable).
func (s *Server) SetDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

// Calls returns how many times "METHOD /route" was served (route as in the doc comment, with
// {id} for path parameters).
func (s *Server) Calls(route string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[route]
}

// Seed registers the Hub's clients and the keys they need: the backend client (secret given)
// with an auth-service key so it may read tenants and configurations, and the console client.
// It returns the platform-less defaults used by tests and cmd/mockisfc.
func (s *Server) Seed(hubClientID, hubSecret, consoleClientID string) {
	s.AddClient(hubClientID, hubSecret, "backend_services")
	s.AddClient(consoleClientID, "console-secret", "web")
	s.IssueKey(s.opts.AuthClientID, hubClientID, "TENANT:READ", "CLIENT:READ")
}

// ---- HTTP ----

// Handler serves the fake auth-service.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	route := func(pattern, name string, authed bool, h http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			s.mu.Lock()
			s.calls[name]++
			down := s.down
			s.mu.Unlock()
			if down {
				writeErr(w, http.StatusServiceUnavailable, "AS_IE_01", "mock ISFC is down")
				return
			}
			if authed && !s.authorized(r) {
				writeErr(w, http.StatusUnauthorized, "AS_UA_01", "missing or invalid API key")
				return
			}
			h(w, r)
		})
	}
	route("GET /api/v1/client/api-key", "GET /api/v1/client/api-key", false, s.apiKey)
	route("POST /api/v1/client/api-key", "POST /api/v1/client/api-key", false, s.apiKey)
	route("GET /api/v1/tenant", "GET /api/v1/tenant", true, s.listTenants)
	route("GET /api/v1/tenant/{id}", "GET /api/v1/tenant/{id}", true, s.getTenant)
	route("GET /api/v1/client/internal/{id}", "GET /api/v1/client/internal/{id}", true, s.clientInternal)
	route("GET /api/v1/client/configuration/{id}", "GET /api/v1/client/configuration/{id}", true, s.configuration)
	route("POST /oauth2/token", "POST /oauth2/token", false, s.token)
	route("GET /.well-known/jwks.json", "GET /.well-known/jwks.json", false, s.jwks)
	s.control(mux)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOK(w http.ResponseWriter, msg string, data any) {
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": msg, "data": data})
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error_code": code, "error": msg})
}

// authorized checks x-isfc-client-id against an ACTIVE, unexpired key auth-service issued to it.
func (s *Server) authorized(r *http.Request) bool {
	id, key := r.Header.Get("x-isfc-client-id"), r.Header.Get("x-isfc-api-key")
	if id == "" || key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ok := false
	for _, k := range s.keys {
		match := subtle.ConstantTimeCompare([]byte(k.Plaintext), []byte(key)) == 1
		if match && k.Status == "ACTIVE" && k.Owner == s.opts.AuthClientID && k.Caller == id &&
			(k.Expires.IsZero() || s.opts.Now().Before(k.Expires)) {
			ok = true
		}
	}
	return ok
}

// apiKey is the key fetch: the caller authenticates with its client credentials and names the
// client whose key it wants. Each fetch of an auth-service key mints a fresh one when none is
// active, like a rotated key being replaced.
func (s *Server) apiKey(w http.ResponseWriter, r *http.Request) {
	var id, secret, requested string
	if u, p, ok := r.BasicAuth(); ok {
		id, secret, requested = u, p, r.URL.Query().Get("requested_client_id")
	} else {
		var b struct {
			ClientID          string `json:"client_id"`
			ClientSecret      string `json:"client_secret"`
			RequestedClientID string `json:"requested_client_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
			writeErr(w, http.StatusBadRequest, "AS_VE_01", "invalid request body")
			return
		}
		id, secret, requested = b.ClientID, b.ClientSecret, b.RequestedClientID
	}
	if id == "" || requested == "" {
		writeErr(w, http.StatusBadRequest, "AS_VE_01", "client credentials and requested_client_id are required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clients[id]
	if c == nil || subtle.ConstantTimeCompare([]byte(c.secret), []byte(secret)) != 1 {
		writeErr(w, http.StatusBadRequest, "AS_VE_09", "Client secret is invalid")
		return
	}
	if s.clients[requested] == nil {
		writeErr(w, http.StatusNotFound, "AS_NF_01", "requested client not found")
		return
	}
	var found *Key
	for _, k := range s.keys {
		if k.Owner == requested && k.Caller == id && k.Status == "ACTIVE" {
			found = k
		}
	}
	if found == nil {
		writeErr(w, http.StatusNotFound, "AS_NF_21", "no active key for this client pair")
		return
	}
	if s.opts.KeyTTL > 0 && requested == s.opts.AuthClientID && found.Expires.IsZero() {
		found.Expires = s.opts.Now().Add(s.opts.KeyTTL)
	}
	data := map[string]any{"key": found.Plaintext}
	if !found.Expires.IsZero() {
		data["expires_at"] = found.Expires.UTC()
	}
	writeOK(w, "Client API Key fetched successfully", data)
}

func (s *Server) listTenants(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Tenant, 0, len(s.tenants))
	for _, t := range s.tenants {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b *Tenant) int { return strings.Compare(a.Key+a.ID, b.Key+b.ID) })
	writeOK(w, "Tenants retrieved successfully", tenantsJSON(out))
}

func tenantsJSON(ts []*Tenant) []map[string]any {
	out := make([]map[string]any, 0, len(ts))
	for _, t := range ts {
		out = append(out, tenantJSON(t))
	}
	return out
}

func tenantJSON(t *Tenant) map[string]any {
	return map[string]any{"id": t.ID, "key": t.Key, "name": t.Name, "status": t.Status, "updated_at": t.UpdatedAt}
}

func (s *Server) getTenant(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tenants[strings.ToLower(r.PathValue("id"))]
	if t == nil {
		writeErr(w, http.StatusNotFound, "AS_NF_30", "tenant not found")
		return
	}
	writeOK(w, "Tenant retrieved successfully", tenantJSON(t))
}

// clientInternal answers like ISFC: only ACTIVE tenants, in panel order.
func (s *Server) clientInternal(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clients[r.PathValue("id")]
	if c == nil {
		writeErr(w, http.StatusNotFound, "AS_NF_01", "client not found")
		return
	}
	tenants := []string{}
	for _, id := range c.tenants {
		if t := s.tenants[id]; t != nil && t.Status == "ACTIVE" {
			tenants = append(tenants, id)
		}
	}
	writeOK(w, "OAuth client retrieved successfully", map[string]any{
		"status": c.status, "client_type": c.clientType,
		"metadata": map[string]any{"tenancy": c.tenancy, "tenants": tenants},
	})
}

// configuration lists ACTIVE keys with plaintext (ISFC TODO(isfc-security) 4).
func (s *Server) configuration(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	c := s.clients[id]
	if c == nil {
		writeErr(w, http.StatusNotFound, "AS_NF_01", "client not found")
		return
	}
	keys := []map[string]any{}
	for _, k := range s.keys {
		if k.Owner == id && k.Status == "ACTIVE" {
			keys = append(keys, map[string]any{"id": k.ID, "name": k.Name, "key": k.Plaintext, "allowed_client_id": k.Caller,
				"status": k.Status, "roles": orEmpty(k.Roles), "permissions": orEmpty(k.Permissions), "updated_at": k.UpdatedAt})
		}
	}
	writeOK(w, "Client configuration get successfully", map[string]any{
		"client":           map[string]any{"name": c.id, "status": c.status, "client_type": c.clientType},
		"allowed_api_keys": keys,
	})
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// token is POST /oauth2/token (client_credentials) and mints an RS256 JWT.
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		GrantType    string `json:"grant_type"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.GrantType != "client_credentials" {
		writeErr(w, http.StatusBadRequest, "AS_VE_01", "invalid token request")
		return
	}
	s.mu.Lock()
	c := s.clients[req.ClientID]
	ok := c != nil && subtle.ConstantTimeCompare([]byte(c.secret), []byte(req.ClientSecret)) == 1
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusUnauthorized, "AS_UA_02", "invalid client credentials")
		return
	}
	tok, err := s.MintToken(req.ClientID, time.Hour)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "AS_IE_01", "token signing failed")
		return
	}
	writeOK(w, "Token generated", map[string]any{"access_token": tok, "token_type": "Bearer"})
}

// MintToken signs an RS256 access token for clientID (sub and client_id claims).
func (s *Server) MintToken(clientID string, ttl time.Duration) (string, error) {
	now := s.opts.Now()
	enc := base64.RawURLEncoding
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": s.kid})
	claims, _ := json.Marshal(map[string]any{"iss": s.opts.Issuer, "sub": clientID, "client_id": clientID,
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(ttl).Unix()})
	signing := enc.EncodeToString(hdr) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.rsa, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

func (s *Server) jwks(w http.ResponseWriter, _ *http.Request) {
	enc := base64.RawURLEncoding
	pub := s.rsa.PublicKey
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "alg": "RS256", "use": "sig", "kid": s.kid,
		"n": enc.EncodeToString(pub.N.Bytes()), "e": enc.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}
