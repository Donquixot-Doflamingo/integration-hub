package adminapi

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Role is an admin RBAC role (DESIGN §16.1). admin ⊃ editor ⊃ viewer ⊃ tenant_viewer and
// admin ⊃ tenant_admin ⊃ tenant_viewer. tenant_viewer and tenant_admin are restricted to the
// tenants of the principal; editor and viewer do not satisfy tenant_admin (hub-admin 2.1.0).
type Role string

const (
	RoleAdmin        Role = "admin"
	RoleEditor       Role = "editor"
	RoleViewer       Role = "viewer"
	RoleTenantAdmin  Role = "tenant_admin"
	RoleTenantViewer Role = "tenant_viewer"
)

// rank orders roles for Top (-1: not a role). It is not the hierarchy: see satisfies.
func (r Role) rank() int {
	switch r {
	case RoleAdmin:
		return 4
	case RoleEditor:
		return 3
	case RoleViewer:
		return 2
	case RoleTenantAdmin:
		return 1
	case RoleTenantViewer:
		return 0
	}
	return -1
}

// satisfies reports whether holding r meets an operation's minimum role need.
func (r Role) satisfies(need Role) bool {
	switch {
	case r.rank() < 0 || need.rank() < 0:
		return false
	case r == need || r == RoleAdmin || need == RoleTenantViewer:
		return true
	case r == RoleEditor:
		return need == RoleViewer
	}
	return false // viewer and tenant_viewer hold nothing more; tenant_admin holds only tenant_viewer
}

// Principal is the authenticated caller of one admin request.
type Principal struct {
	Subject   string
	Email     string
	Name      string
	Roles     []Role
	Tenants   []string // tenant_viewer and tenant_admin scope; empty for the platform roles
	ExpiresAt time.Time

	// The fields below are set by the ISFC mode only (HUB_ADMIN_AUTH_MODE=isfc).

	// ISFCTenantID is the ISFC tenant of the request (x-isfc-tenant-id).
	ISFCTenantID string
	// Machine marks an automation client that sent an ISFC API key (Subject is "client:<id>").
	Machine bool
	// ClientID is the ISFC client of the request (x-isfc-client-id).
	ClientID string
	// SubType and SubAttrs are the gateway's x-isfc-sub-type and decoded x-isfc-sub-attrs, kept
	// for the policy input.
	SubType  string
	SubAttrs map[string]any
}

// Top is the highest role the principal holds (-1 rank when none is known).
func (p Principal) Top() Role {
	top := Role("")
	for _, r := range p.Roles {
		if r.rank() > top.rank() {
			top = r
		}
	}
	return top
}

// Allows reports whether the principal meets an operation's x-required-role.
func (p Principal) Allows(min Role) bool {
	return slices.ContainsFunc(p.Roles, func(r Role) bool { return r.satisfies(min) })
}

// Scoped reports whether results must be filtered to Tenants (a pure tenant_viewer or
// tenant_admin: no platform role).
func (p Principal) Scoped() bool {
	if p.Machine { // keys carry no tenant (AUTH_PLAN G1): a machine acts on the platform
		return false
	}
	top := p.Top()
	return top == RoleTenantViewer || top == RoleTenantAdmin
}

// SeesTenant reports whether a scoped principal may see tenant's objects.
func (p Principal) SeesTenant(tenant string) bool {
	return !p.Scoped() || slices.Contains(p.Tenants, tenant)
}

// Actor is how the change log names the principal: email when present, else the subject.
func (p Principal) Actor() string {
	if p.Email != "" {
		return p.Email
	}
	return p.Subject
}

// RoleNames lists the principal's roles as strings.
func (p Principal) RoleNames() []string {
	out := make([]string, len(p.Roles))
	for i, r := range p.Roles {
		out[i] = string(r)
	}
	return out
}

type principalKey struct{}

// PrincipalFrom returns the request's authenticated principal.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Verifier turns a bearer token into a principal.
type Verifier interface {
	Verify(ctx context.Context, token string) (Principal, error)
}

// ErrBadToken is any token problem; the message says which, never echoing the token.
var ErrBadToken = errors.New("invalid token")

// claims are the OIDC access-token claims the Hub reads.
type claims struct {
	Iss string `json:"iss"`
	Sub string `json:"sub"`
	// ClientID is the OAuth client the token was issued to (Hydra: client_id); the ISFC mode
	// checks it against the console client.
	ClientID string   `json:"client_id,omitempty"`
	Aud      any      `json:"aud"`
	Exp      int64    `json:"exp"`
	Nbf      int64    `json:"nbf"`
	Email    string   `json:"email"`
	Name     string   `json:"name"`
	Roles    []string `json:"hub_roles"`
	Tenants  []string `json:"hub_tenants"`
}

// Signing algorithms the verifier accepts: an explicit allow-list (never "none", never HS*).
// ES256 keys are P-256; RS256 keys are RSA of at least minRSABits bits.
const (
	algES256   = "ES256"
	algRS256   = "RS256"
	minRSABits = 2048
)

// TokenClaims are the verified claims of a token that the ISFC mode cross-checks against the
// gateway headers.
type TokenClaims struct {
	Subject   string
	ClientID  string
	Email     string
	ExpiresAt time.Time
}

// TokenVerifier checks a bearer token's signature, issuer and times and returns its claims.
type TokenVerifier interface {
	VerifyToken(ctx context.Context, token string) (TokenClaims, error)
}

// JWTVerifier checks ES256 and RS256 tokens from one issuer against its JSON Web Key Set.
type JWTVerifier struct {
	Issuer string
	// Audience, when set, must appear in the token's aud claim.
	Audience string
	// Keys returns the issuer's signing keys by kid (*ecdsa.PublicKey or *rsa.PublicKey);
	// refresh asks for a re-fetch (unknown kid).
	Keys func(ctx context.Context, refresh bool) (map[string]crypto.PublicKey, error)
	Now  func() time.Time
	// Leeway tolerates clock skew on exp and nbf (default 30 s).
	Leeway time.Duration
}

func (v *JWTVerifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// Verify implements Verifier.
func (v *JWTVerifier) Verify(ctx context.Context, token string) (Principal, error) {
	c, err := v.verify(ctx, token)
	if err != nil {
		return Principal{}, err
	}
	p := Principal{Subject: c.Sub, Email: c.Email, Name: c.Name, Tenants: c.Tenants, ExpiresAt: time.Unix(c.Exp, 0).UTC()}
	for _, r := range c.Roles {
		if role := Role(r); role.rank() >= 0 {
			p.Roles = append(p.Roles, role)
		}
	}
	if p.Tenants == nil {
		p.Tenants = []string{}
	}
	return p, nil
}

// VerifyToken implements TokenVerifier: the same checks as Verify, returning the claims the ISFC
// mode needs instead of a principal.
func (v *JWTVerifier) VerifyToken(ctx context.Context, token string) (TokenClaims, error) {
	c, err := v.verify(ctx, token)
	if err != nil {
		return TokenClaims{}, err
	}
	return TokenClaims{Subject: c.Sub, ClientID: c.ClientID, Email: c.Email, ExpiresAt: time.Unix(c.Exp, 0).UTC()}, nil
}

// verify checks signature, issuer, subject, times and audience of a token.
func (v *JWTVerifier) verify(ctx context.Context, token string) (*claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("%w: malformed", ErrBadToken)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &hdr); err != nil || (hdr.Alg != algES256 && hdr.Alg != algRS256) {
		return nil, fmt.Errorf("%w: unsupported header", ErrBadToken)
	}
	keys, err := v.Keys(ctx, false)
	if err != nil {
		return nil, fmt.Errorf("signing keys: %w", err)
	}
	key, ok := keys[hdr.Kid]
	if !ok {
		if keys, err = v.Keys(ctx, true); err != nil {
			return nil, fmt.Errorf("signing keys: %w", err)
		}
		if key, ok = keys[hdr.Kid]; !ok {
			return nil, fmt.Errorf("%w: unknown signing key", ErrBadToken)
		}
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("%w: bad signature", ErrBadToken)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !verifySignature(hdr.Alg, key, digest[:], sig) {
		return nil, fmt.Errorf("%w: bad signature", ErrBadToken)
	}
	var c claims
	if err := decodeSegment(parts[1], &c); err != nil {
		return nil, fmt.Errorf("%w: bad claims", ErrBadToken)
	}
	leeway := v.Leeway
	if leeway == 0 {
		leeway = 30 * time.Second
	}
	now := v.now()
	switch {
	case c.Iss != v.Issuer:
		return nil, fmt.Errorf("%w: wrong issuer", ErrBadToken)
	case c.Sub == "":
		return nil, fmt.Errorf("%w: no subject", ErrBadToken)
	case c.Exp == 0 || now.After(time.Unix(c.Exp, 0).Add(leeway)):
		return nil, fmt.Errorf("%w: token expired", ErrBadToken)
	case c.Nbf != 0 && now.Add(leeway).Before(time.Unix(c.Nbf, 0)):
		return nil, fmt.Errorf("%w: token not yet valid", ErrBadToken)
	case v.Audience != "" && !audienceHas(c.Aud, v.Audience):
		return nil, fmt.Errorf("%w: wrong audience", ErrBadToken)
	}
	return &c, nil
}

// verifySignature checks sig over digest with key for one allow-listed alg. The key type must
// match the alg, so an RSA key can never validate an ES256 token or the reverse.
func verifySignature(alg string, key crypto.PublicKey, digest, sig []byte) bool {
	switch k := key.(type) {
	case *ecdsa.PublicKey:
		if alg != algES256 || len(sig) != 64 {
			return false
		}
		return ecdsa.Verify(k, digest, new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
	case *rsa.PublicKey:
		if alg != algRS256 || k.N.BitLen() < minRSABits {
			return false
		}
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, digest, sig) == nil
	}
	return false
}

func audienceHas(aud any, want string) bool {
	switch a := aud.(type) {
	case string:
		return a == want
	case []any:
		for _, x := range a {
			if s, ok := x.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

func decodeSegment(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// jwk is one key of a JSON Web Key Set: P-256 (x, y) or RSA (n, e).
type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv,omitempty"`
	Kid string `json:"kid"`
	Use string `json:"use,omitempty"`
	Alg string `json:"alg,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
	N   string `json:"n,omitempty"`
	E   string `json:"e,omitempty"`
}

// ParseJWKS reads the P-256 and RSA signing keys of a JWKS document. Other key types, encryption
// keys (use "enc") and RSA keys under 2048 bits are skipped, so a weak or foreign key in the set
// can never verify a token.
func ParseJWKS(b []byte) (map[string]crypto.PublicKey, error) {
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(b, &set); err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	out := map[string]crypto.PublicKey{}
	for _, k := range set.Keys {
		if k.Use == "enc" {
			continue
		}
		if k.Kty == "RSA" {
			pub, err := parseRSAKey(k)
			if err != nil {
				return nil, err
			}
			if pub != nil {
				out[k.Kid] = pub
			}
			continue
		}
		if k.Kty != "EC" || k.Crv != "P-256" {
			continue
		}
		x, errX := base64.RawURLEncoding.DecodeString(k.X)
		y, errY := base64.RawURLEncoding.DecodeString(k.Y)
		if errX != nil || errY != nil {
			return nil, fmt.Errorf("jwks: key %q: bad coordinates", k.Kid)
		}
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, pad32(x)...), pad32(y)...))
		if err != nil {
			return nil, fmt.Errorf("jwks: key %q: %w", k.Kid, err)
		}
		out[k.Kid] = pub
	}
	return out, nil
}

// parseRSAKey reads an RSA JWK. A key under minRSABits (or with an unusable exponent) is
// skipped (nil, nil); undecodable parameters are an error like the EC ones.
func parseRSAKey(k jwk) (*rsa.PublicKey, error) {
	n, errN := base64.RawURLEncoding.DecodeString(k.N)
	e, errE := base64.RawURLEncoding.DecodeString(k.E)
	if errN != nil || errE != nil || len(n) == 0 || len(e) == 0 || len(e) > 4 {
		return nil, fmt.Errorf("jwks: key %q: bad RSA parameters", k.Kid)
	}
	mod := new(big.Int).SetBytes(n)
	exp := int(new(big.Int).SetBytes(e).Int64())
	if mod.BitLen() < minRSABits || exp < 3 || exp%2 == 0 {
		return nil, nil
	}
	return &rsa.PublicKey{N: mod, E: exp}, nil
}

func pad32(b []byte) []byte {
	if len(b) >= 32 {
		return b
	}
	return append(make([]byte, 32-len(b)), b...)
}

// RemoteKeys fetches the issuer's JWKS (P-256 and RSA keys) through OIDC discovery, caches it and re-fetches on an
// unknown kid at most once a minute.
func RemoteKeys(issuer string, client *http.Client) func(context.Context, bool) (map[string]crypto.PublicKey, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	var (
		mu      sync.Mutex
		keys    map[string]crypto.PublicKey
		fetched time.Time
	)
	get := func(ctx context.Context, url string, v any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET %s: %s", url, resp.Status)
		}
		return json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(v)
	}
	return func(ctx context.Context, refresh bool) (map[string]crypto.PublicKey, error) {
		mu.Lock()
		defer mu.Unlock()
		if keys != nil && (!refresh || time.Since(fetched) < time.Minute) {
			return keys, nil
		}
		var disc struct {
			JWKSURI string `json:"jwks_uri"`
		}
		if err := get(ctx, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", &disc); err != nil {
			return keys, fmt.Errorf("oidc discovery: %w", err)
		}
		var raw json.RawMessage
		if err := get(ctx, disc.JWKSURI, &raw); err != nil {
			return keys, fmt.Errorf("jwks: %w", err)
		}
		k, err := ParseJWKS(raw)
		if err != nil {
			return keys, err
		}
		keys, fetched = k, time.Now()
		return keys, nil
	}
}

// DevIssuer is the dev OIDC stub of `hub admin` (DESIGN §19 M7): a P-256 key made at start, a
// discovery document, its JWKS and a token endpoint that mints a token for any subject and roles.
// It must never run in production: it hands out admin tokens to anyone who asks.
type DevIssuer struct {
	Issuer string
	kid    string
	key    *ecdsa.PrivateKey
	Now    func() time.Time
}

// NewDevIssuer makes a stub issuer whose iss is issuer (the admin's public base URL).
func NewDevIssuer(issuer string) (*DevIssuer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	pub, _ := key.PublicKey.Bytes()
	sum := sha256.Sum256(pub)
	return &DevIssuer{Issuer: strings.TrimSuffix(issuer, "/"), kid: "dev-" + base64.RawURLEncoding.EncodeToString(sum[:6]), key: key}, nil
}

func (d *DevIssuer) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Verifier verifies this stub's tokens.
func (d *DevIssuer) Verifier() *JWTVerifier {
	keys := map[string]crypto.PublicKey{d.kid: &d.key.PublicKey}
	return &JWTVerifier{Issuer: d.Issuer, Now: d.Now, Keys: func(context.Context, bool) (map[string]crypto.PublicKey, error) { return keys, nil }}
}

// Mint signs a token for p, valid for ttl.
func (d *DevIssuer) Mint(p Principal, ttl time.Duration) (string, error) {
	now := d.now()
	hdr, _ := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT", "kid": d.kid})
	body, _ := json.Marshal(claims{Iss: d.Issuer, Sub: p.Subject, Aud: "hub-admin", Exp: now.Add(ttl).Unix(), Nbf: now.Add(-time.Second).Unix(),
		Email: p.Email, Name: p.Name, Roles: p.RoleNames(), Tenants: p.Tenants})
	signing := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, d.key, digest[:])
	if err != nil {
		return "", err
	}
	sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Handler serves the stub: GET /.well-known/openid-configuration, GET /oidc/jwks and
// POST /oidc/dev-token {"sub","email","name","roles","tenants","ttl_s"} → {"access_token",...}.
func (d *DevIssuer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"issuer": d.Issuer, "jwks_uri": d.Issuer + "/oidc/jwks",
			"token_endpoint": d.Issuer + "/oidc/dev-token", "id_token_signing_alg_values_supported": []string{"ES256"}})
	})
	mux.HandleFunc("GET /oidc/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub, _ := d.key.PublicKey.Bytes() // 0x04 || X || Y
		writeJSON(w, http.StatusOK, map[string]any{"keys": []jwk{{Kty: "EC", Crv: "P-256", Kid: d.kid, Use: "sig", Alg: "ES256",
			X: base64.RawURLEncoding.EncodeToString(pub[1:33]), Y: base64.RawURLEncoding.EncodeToString(pub[33:])}}})
	})
	mux.HandleFunc("POST /oidc/dev-token", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Sub     string   `json:"sub"`
			Email   string   `json:"email"`
			Name    string   `json:"name"`
			Roles   []string `json:"roles"`
			Tenants []string `json:"tenants"`
			TTLS    int      `json:"ttl_s"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil || in.Sub == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "want JSON {sub, roles[], tenants[], email, name, ttl_s}"})
			return
		}
		p := Principal{Subject: in.Sub, Email: in.Email, Name: in.Name, Tenants: in.Tenants}
		for _, role := range in.Roles {
			if Role(role).rank() < 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown role " + role})
				return
			}
			p.Roles = append(p.Roles, Role(role))
		}
		ttl := time.Duration(in.TTLS) * time.Second
		if ttl <= 0 || ttl > 24*time.Hour {
			ttl = time.Hour
		}
		tok, err := d.Mint(p, ttl)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mint failed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": int(ttl.Seconds())})
	})
	return mux
}
