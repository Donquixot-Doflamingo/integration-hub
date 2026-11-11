package mockisfc_test

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc/mockisfc"
)

// TestMintedTokenVerifiesAgainstJWKS checks the RS256 token and the JWKS belong together, the
// way a Hydra-style verifier would use them.
func TestMintedTokenVerifiesAgainstJWKS(t *testing.T) {
	m := mockisfc.New(mockisfc.Options{Issuer: "http://mockisfc.test"})
	ts := httptest.NewServer(m.Handler())
	defer ts.Close()
	tok, err := m.MintToken("integration-hub-console", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(ts.URL + "/.well-known/jwks.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var set struct {
		Keys []struct{ Kid, Alg, N, E string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil || len(set.Keys) != 1 {
		t.Fatalf("jwks: %v %+v", err, set)
	}
	enc := base64.RawURLEncoding
	n, _ := enc.DecodeString(set.Keys[0].N)
	e, _ := enc.DecodeString(set.Keys[0].E)
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	parts := strings.Split(tok, ".")
	sig, _ := enc.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature: %v", err)
	}
	var hdr map[string]string
	raw, _ := enc.DecodeString(parts[0])
	_ = json.Unmarshal(raw, &hdr)
	if hdr["alg"] != "RS256" || hdr["kid"] != set.Keys[0].Kid {
		t.Fatalf("header %v", hdr)
	}
	var claims map[string]any
	raw, _ = enc.DecodeString(parts[1])
	_ = json.Unmarshal(raw, &claims)
	if claims["iss"] != "http://mockisfc.test" || claims["client_id"] != "integration-hub-console" {
		t.Fatalf("claims %v", claims)
	}
}

func TestManagementRoutesNeedAnIssuedKey(t *testing.T) {
	m := mockisfc.New(mockisfc.Options{})
	m.Seed("integration-hub", "s", "console")
	ts := httptest.NewServer(m.Handler())
	defer ts.Close()
	for name, hdr := range map[string]map[string]string{
		"no headers": {}, "wrong key": {"x-isfc-client-id": "integration-hub", "x-isfc-api-key": "nope"},
	} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/v1/tenant", nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, resp.StatusCode)
		}
	}
}
