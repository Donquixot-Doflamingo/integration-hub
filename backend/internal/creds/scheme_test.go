package creds

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

func cr(values map[string]string) Credentials {
	return Credentials{AccountID: "acct-1", Version: 1, Values: values}
}

func TestApplySchemes(t *testing.T) {
	ct := cr(map[string]string{"account_id": "TEST-ACCT-not-real", "passcode": "test-passcode-not-real"})
	ctHeaders := map[string]string{"X-CleverTap-Account-Id": "TEST-ACCT-not-real", "X-CleverTap-Passcode": "test-passcode-not-real", "Content-Type": "application/json"}
	tests := []struct {
		name     string
		scheme   string
		creds    Credentials
		headers  map[string]string
		wantAuth string // "" = no Authorization header expected
		wantErr  error
	}{
		{"static headers present", SchemeStaticHeaders, ct, ctHeaders, "", nil},
		{"static headers missing passcode", SchemeStaticHeaders, ct, map[string]string{"X-CleverTap-Account-Id": "TEST-ACCT-not-real"}, "", ErrMissingCredential},
		{"static headers no creds", SchemeStaticHeaders, cr(nil), ctHeaders, "", ErrMissingCredential},
		{"bearer", SchemeBearer, cr(map[string]string{"token": "sk-test-token-not-real"}), map[string]string{"Content-Type": "application/json"}, "Bearer sk-test-token-not-real", nil},
		{"bearer replaces transformer authorization", SchemeBearer, cr(map[string]string{"token": "sk-test-token-not-real"}), map[string]string{"authorization": "Bearer from-transformer"}, "Bearer sk-test-token-not-real", nil},
		{"bearer nil headers", SchemeBearer, cr(map[string]string{"token": "sk-test-token-not-real"}), nil, "Bearer sk-test-token-not-real", nil},
		{"bearer missing token", SchemeBearer, cr(map[string]string{"api_key": "sk-test-token-not-real"}), nil, "", ErrMissingCredential},
		// RFC 7617 example: Aladdin:open sesame
		{"basic", SchemeBasic, cr(map[string]string{"username": "Aladdin", "password": "open sesame"}), nil, "Basic QWxhZGRpbjpvcGVuIHNlc2FtZQ==", nil},
		{"basic missing password", SchemeBasic, cr(map[string]string{"username": "Aladdin"}), nil, "", ErrMissingCredential},
		{"unknown scheme", "hmac", ct, ctHeaders, "", ErrUnknownScheme},
		{"empty scheme", "", ct, ctHeaders, "", ErrUnknownScheme},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := maps.Clone(tt.headers)
			req := core.VendorRequest{Method: "POST", Path: "/x", Headers: tt.headers}
			err := Apply(tt.scheme, tt.creds, &req)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if err != nil {
				for _, v := range tt.creds.Values {
					if strings.Contains(err.Error(), v) {
						t.Fatalf("error leaks a credential value: %v", err)
					}
				}
				return
			}
			if got := req.Headers["Authorization"]; got != tt.wantAuth {
				t.Fatalf("Authorization = %q, want %q", got, tt.wantAuth)
			}
			n := 0
			for k := range req.Headers {
				if strings.EqualFold(k, "Authorization") {
					n++
				}
			}
			if tt.wantAuth != "" && n != 1 {
				t.Fatalf("%d Authorization headers, want 1: %v", n, req.Headers)
			}
			if !maps.Equal(tt.headers, orig) {
				t.Fatalf("Apply mutated the transformer's header map: %v (was %v)", tt.headers, orig)
			}
		})
	}
}

// TestSchemeComesFromVendorDefinition: the dev CleverTap vendor declares static_headers, and
// ApplyVendor uses it whatever the transformer put in the request.
func TestSchemeComesFromVendorDefinition(t *testing.T) {
	b, err := os.ReadFile("../../config/vendors/clevertap.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var v registry.Vendor
	if err := yaml.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if err := ValidateScheme(v.Auth); err != nil {
		t.Fatalf("dev vendor scheme: %v", err)
	}
	c := cr(map[string]string{"account_id": "TEST-ACCT-not-real", "passcode": "test-passcode-not-real"})
	// The transformer even tries to set a bearer header; the vendor's static_headers scheme
	// injects nothing and leaves it alone.
	req := core.VendorRequest{Headers: map[string]string{"X-CleverTap-Account-Id": "TEST-ACCT-not-real", "X-CleverTap-Passcode": "test-passcode-not-real", "Authorization": "Bearer nope"}}
	if err := ApplyVendor(&v, c, &req); err != nil {
		t.Fatal(err)
	}
	if req.Headers["Authorization"] != "Bearer nope" || len(req.Headers) != 3 {
		t.Fatalf("static_headers injected something: %v", req.Headers)
	}
	bearer := registry.Vendor{Vendor: "llm", Auth: SchemeBearer}
	if err := ApplyVendor(&bearer, cr(map[string]string{"token": "sk-test-token-not-real"}), &req); err != nil {
		t.Fatal(err)
	}
	if req.Headers["Authorization"] != "Bearer sk-test-token-not-real" {
		t.Fatalf("bearer vendor: %v", req.Headers)
	}
}

// TestInjectedValuesNeverLogged: the headers Apply injects, logged whole, come out redacted
// once the credentials went through the cache (HUB-085 "never in logs"; the audit half belongs
// to the audit writer).
func TestInjectedValuesNeverLogged(t *testing.T) {
	key, _ := GenerateKey()
	f, err := NewLocalFile(t.TempDir()+"/s.enc", key)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.WriteAll(map[string]AccountSecrets{
		"llm":    {Active: Slot{Version: 1, Values: map[string]string{"token": "sk-test-token-not-real"}}},
		"twilio": {Active: Slot{Version: 1, Values: map[string]string{"username": "ACtest-user-not-real", "password": "test-password-not-real"}}},
	}); err != nil {
		t.Fatal(err)
	}
	secrets := telemetry.NewSecretSet()
	c, err := NewCache(f, secrets, telemetry.NewMetrics("test", "dev"), CacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Warm(t.Context(), []string{"llm", "twilio"}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	log := slog.New(telemetry.NewRedactHandler(slog.NewJSONHandler(&buf, nil), secrets))
	for account, scheme := range map[string]string{"llm": SchemeBearer, "twilio": SchemeBasic} {
		cred, err := c.Get(t.Context(), account)
		if err != nil {
			t.Fatal(err)
		}
		req := core.VendorRequest{Headers: map[string]string{}}
		if err := Apply(scheme, cred, &req); err != nil {
			t.Fatal(err)
		}
		// Deliberately careless logging under a key the denylist does not cover.
		log.InfoContext(t.Context(), "sending", slog.String("headers", fmt.Sprint(req.Headers)), slog.String("auth_header", req.Headers["Authorization"]))
	}
	out := buf.String()
	for _, v := range []string{"sk-test-token-not-real", "ACtest-user-not-real", "test-password-not-real", basicToken("ACtest-user-not-real", "test-password-not-real")} {
		if strings.Contains(out, v) {
			t.Fatalf("injected value %q reached the log:\n%s", v, out)
		}
	}
}

func BenchmarkInject(b *testing.B) {
	c := cr(map[string]string{"token": "sk-test-token-not-real"})
	ct := cr(map[string]string{"account_id": "TEST-ACCT-not-real", "passcode": "test-passcode-not-real"})
	hdr := map[string]string{"Content-Type": "application/json", "X-CleverTap-Account-Id": "TEST-ACCT-not-real", "X-CleverTap-Passcode": "test-passcode-not-real"}
	b.Run("bearer", func(b *testing.B) {
		for b.Loop() {
			req := core.VendorRequest{Headers: hdr}
			if err := Apply(SchemeBearer, c, &req); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("static_headers", func(b *testing.B) {
		for b.Loop() {
			req := core.VendorRequest{Headers: hdr}
			if err := Apply(SchemeStaticHeaders, ct, &req); err != nil {
				b.Fatal(err)
			}
		}
	})
}
