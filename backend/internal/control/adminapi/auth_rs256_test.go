package adminapi

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// rsIssuer signs RS256 tokens the way Hydra does and serves the matching JWKS (HUB-472).
type rsIssuer struct {
	issuer string
	kid    string
	key    *rsa.PrivateKey
}

func newRSIssuer(t testing.TB, issuer, kid string, bits int) *rsIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	return &rsIssuer{issuer: issuer, kid: kid, key: key}
}

func (i *rsIssuer) jwk() map[string]any {
	return map[string]any{"kty": "RSA", "kid": i.kid, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(i.key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(i.key.E)).Bytes())}
}

func jwksOf(t testing.TB, keys ...map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sign signs claims (merged over sane defaults) with alg RS256, or a forged header alg.
func (i *rsIssuer) sign(t testing.TB, claims map[string]any) string {
	t.Helper()
	return i.signAlg(t, "RS256", claims)
}

func (i *rsIssuer) signAlg(t testing.TB, alg string, claims map[string]any) string {
	t.Helper()
	now := time.Now()
	c := map[string]any{"iss": i.issuer, "sub": "user-1", "exp": now.Add(time.Hour).Unix(), "nbf": now.Add(-time.Second).Unix()}
	for k, v := range claims {
		if v == nil {
			delete(c, k)
			continue
		}
		c[k] = v
	}
	hdr, _ := json.Marshal(map[string]string{"alg": alg, "typ": "JWT", "kid": i.kid})
	body, _ := json.Marshal(c)
	signing := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func keysFunc(m map[string]crypto.PublicKey) func(context.Context, bool) (map[string]crypto.PublicKey, error) {
	return func(context.Context, bool) (map[string]crypto.PublicKey, error) { return m, nil }
}

func TestJWTVerifierRS256(t *testing.T) {
	iss := newRSIssuer(t, "https://hydra.test", "rs-1", 2048)
	other := newRSIssuer(t, "https://hydra.test", "rs-1", 2048) // same kid, different key
	rotated := newRSIssuer(t, "https://hydra.test", "rs-2", 2048)
	short := newRSIssuer(t, "https://hydra.test", "rs-short", 1024)

	pub := func(i *rsIssuer) crypto.PublicKey { return &i.key.PublicKey }
	known, err := ParseJWKS(jwksOf(t, iss.jwk(), short.jwk()))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := known["rs-short"]; ok || known["rs-1"] == nil {
		t.Fatalf("ParseJWKS must keep the 2048-bit key and refuse the 1024-bit one: %v", known)
	}

	tests := []struct {
		name    string
		keys    func(context.Context, bool) (map[string]crypto.PublicKey, error)
		token   string
		wantErr string // "" = valid
	}{
		{"valid", keysFunc(known), iss.sign(t, map[string]any{"email": "a@example.com", "client_id": "console"}), ""},
		{"wrong key under a known kid", keysFunc(known), other.sign(t, nil), "bad signature"},
		{"unknown kid", keysFunc(known), rotated.sign(t, nil), "unknown signing key"},
		{"short RSA key refused", keysFunc(known), short.sign(t, nil), "unknown signing key"},
		{"short RSA key refused even if handed in directly", keysFunc(map[string]crypto.PublicKey{"rs-short": pub(short)}), short.sign(t, nil), "bad signature"},
		{"alg none", keysFunc(known), noneToken(iss.kid), "unsupported header"},
		{"alg HS256 with the public key as secret", keysFunc(known), iss.signAlg(t, "HS256", nil), "unsupported header"},
		{"ES256 header on an RSA key", keysFunc(known), iss.signAlg(t, "ES256", nil), "bad signature"},
		{"expired", keysFunc(known), iss.sign(t, map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}), "token expired"},
		{"wrong issuer", keysFunc(known), iss.sign(t, map[string]any{"iss": "https://evil.test"}), "wrong issuer"},
		{"no subject", keysFunc(known), iss.sign(t, map[string]any{"sub": nil}), "no subject"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &JWTVerifier{Issuer: "https://hydra.test", Keys: tt.keys}
			p, err := v.Verify(t.Context(), tt.token)
			if tt.wantErr != "" {
				if !errors.Is(err, ErrBadToken) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || p.Subject != "user-1" || p.Email != "a@example.com" {
				t.Fatalf("%+v %v", p, err)
			}
			c, err := v.VerifyToken(t.Context(), tt.token)
			if err != nil || c.ClientID != "console" || c.Subject != "user-1" {
				t.Fatalf("claims %+v %v", c, err)
			}
		})
	}
}

func noneToken(kid string) string {
	hdr, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT", "kid": kid})
	body, _ := json.Marshal(map[string]any{"iss": "https://hydra.test", "sub": "x", "exp": time.Now().Add(time.Hour).Unix()})
	return base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(body) + "."
}

// TestRemoteKeysRotation: an RS256 key set is fetched through discovery, and a token signed by a
// key added later (an unknown kid) triggers one re-fetch.
func TestRemoteKeysRotation(t *testing.T) {
	first := newRSIssuer(t, "", "rs-1", 2048)
	second := newRSIssuer(t, "", "rs-2", 2048)
	served := []map[string]any{first.jwk()}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"jwks_uri": srv.URL + "/jwks"})
		case "/jwks":
			_, _ = w.Write(jwksOf(t, served...))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	first.issuer, second.issuer = srv.URL, srv.URL
	now := time.Now()
	v := &JWTVerifier{Issuer: srv.URL, Keys: RemoteKeys(srv.URL, srv.Client()), Now: func() time.Time { return now }}
	if _, err := v.Verify(t.Context(), first.sign(t, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(t.Context(), second.sign(t, nil)); !errors.Is(err, ErrBadToken) {
		t.Fatalf("a key that is not published yet: %v", err)
	}
	served = []map[string]any{first.jwk(), second.jwk()}
	// The refresh is rate limited to once a minute (RemoteKeys), so a verifier built later sees it.
	v2 := &JWTVerifier{Issuer: srv.URL, Keys: RemoteKeys(srv.URL, srv.Client())}
	if _, err := v2.Verify(t.Context(), second.sign(t, nil)); err != nil {
		t.Fatalf("rotated key: %v", err)
	}
}

// TestES256StillWorksWithMixedKeySet: the dev issuer's ES256 tokens verify against a JWKS that
// also carries an RSA key, and the RSA key cannot be used for them.
func TestES256StillWorksWithMixedKeySet(t *testing.T) {
	dev, err := NewDevIssuer("http://admin.test")
	if err != nil {
		t.Fatal(err)
	}
	rs := newRSIssuer(t, "http://admin.test", "rs-1", 2048)
	devKeys, _ := dev.Verifier().Keys(t.Context(), false)
	keys := map[string]crypto.PublicKey{"rs-1": &rs.key.PublicKey}
	for k, v := range devKeys {
		keys[k] = v
	}
	tok, err := dev.Mint(Principal{Subject: "p", Roles: []Role{RoleViewer}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	v := &JWTVerifier{Issuer: "http://admin.test", Keys: keysFunc(keys)}
	if p, err := v.Verify(t.Context(), tok); err != nil || p.Top() != RoleViewer {
		t.Fatalf("%+v %v", p, err)
	}
}
