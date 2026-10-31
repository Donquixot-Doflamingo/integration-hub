package creds

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// Fake values, clearly not real (SECURITY §3).
func devSecrets() map[string]AccountSecrets {
	return map[string]AccountSecrets{
		"ct-brandA-in": {Active: Slot{Version: 1, Values: map[string]string{"account_id": "TEST-ACCT-A-not-real", "passcode": "test-passcode-A-not-real"}}},
		"ct-brandB-us": {
			Active: Slot{Version: 3, Values: map[string]string{"account_id": "TEST-ACCT-B-not-real", "passcode": "test-passcode-B-not-real"}},
			Next:   &Slot{Version: 4, Values: map[string]string{"account_id": "TEST-ACCT-B-not-real", "passcode": "test-passcode-B2-not-real"}},
		},
	}
}

func newTestFile(t *testing.T) (*LocalFile, []byte) {
	t.Helper()
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	f, err := NewLocalFile(filepath.Join(t.TempDir(), "dev-secrets.enc"), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.WriteAll(devSecrets()); err != nil {
		t.Fatal(err)
	}
	return f, key
}

func TestLocalFileRoundTrip(t *testing.T) {
	f, _ := newTestFile(t)
	got, err := f.Load(t.Context(), []string{"ct-brandA-in", "ct-brandB-us"})
	if err != nil {
		t.Fatal(err)
	}
	if got["ct-brandA-in"].Active.Values["passcode"] != "test-passcode-A-not-real" || got["ct-brandB-us"].Next.Version != 4 {
		t.Fatalf("round trip lost data: %+v", got)
	}
	raw, err := os.ReadFile(f.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "not-real") || strings.Contains(string(raw), "passcode") {
		t.Fatal("encrypted file contains plaintext")
	}
	fi, err := os.Stat(f.Path())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestLocalFileMissingAccount(t *testing.T) {
	f, _ := newTestFile(t)
	got, err := f.Load(t.Context(), []string{"ct-brandA-in", "nope"})
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want ErrNotFound naming the account", err)
	}
	if _, ok := got["ct-brandA-in"]; !ok {
		t.Fatal("found accounts must still be returned")
	}
}

// TestTamperedFileFailsClosed: every way of altering the file fails with ErrDecrypt and
// returns no credentials.
func TestTamperedFileFailsClosed(t *testing.T) {
	f, key := newTestFile(t)
	orig, err := os.ReadFile(f.Path())
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(fn func(*envelope)) []byte {
		var env envelope
		if err := json.Unmarshal(orig, &env); err != nil {
			t.Fatal(err)
		}
		fn(&env)
		b, _ := json.Marshal(env)
		return b
	}
	otherKey, _ := GenerateKey()
	tests := []struct {
		name string
		data []byte
		key  []byte
	}{
		{"flipped ciphertext bit", mutate(func(e *envelope) { e.Ciphertext[0] ^= 1 }), key},
		{"flipped tag bit", mutate(func(e *envelope) { e.Ciphertext[len(e.Ciphertext)-1] ^= 0x80 }), key},
		{"flipped nonce bit", mutate(func(e *envelope) { e.Nonce[3] ^= 1 }), key},
		{"truncated ciphertext", mutate(func(e *envelope) { e.Ciphertext = e.Ciphertext[:len(e.Ciphertext)/2] }), key},
		{"short nonce", mutate(func(e *envelope) { e.Nonce = e.Nonce[:4] }), key},
		{"changed format", mutate(func(e *envelope) { e.Format = "hub-dev-secrets/v0" }), key},
		{"truncated file", orig[:len(orig)/2], key},
		{"not json", []byte("garbage"), key},
		{"wrong key", orig, otherKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "x.enc")
			if err := os.WriteFile(p, tt.data, 0o600); err != nil {
				t.Fatal(err)
			}
			lf, err := NewLocalFile(p, tt.key)
			if err != nil {
				t.Fatal(err)
			}
			got, err := lf.Load(t.Context(), []string{"ct-brandA-in"})
			if !errors.Is(err, ErrDecrypt) {
				t.Fatalf("err = %v, want ErrDecrypt", err)
			}
			if got != nil {
				t.Fatalf("tampered file returned data: %v", got)
			}
			if !strings.Contains(err.Error(), "tampered") || !strings.Contains(err.Error(), p) {
				t.Fatalf("error %q should say tampered and name the file", err)
			}
		})
	}
}

func TestKeyFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "dev.key")
	key, _ := GenerateKey()
	if err := WriteKeyFile(p, key, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteKeyFile(p, key, false); err == nil {
		t.Fatal("overwriting an existing key must be refused")
	}
	got, err := ReadKeyFile(p)
	if err != nil || string(got) != string(key) {
		t.Fatalf("ReadKeyFile = %v, %v", got, err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadKeyFile(p); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("world-readable key: err = %v", err)
	}
	for _, bad := range []string{"not base64!", "c2hvcnQ="} {
		if _, err := DecodeKey([]byte(bad)); err == nil {
			t.Fatalf("DecodeKey(%q) accepted", bad)
		}
	}
}

func TestValidateSecrets(t *testing.T) {
	tests := []struct {
		name string
		s    AccountSecrets
		ok   bool
	}{
		{"ok", AccountSecrets{Active: Slot{Version: 1, Values: map[string]string{"token": "abcdefgh"}}}, true},
		{"version 0", AccountSecrets{Active: Slot{Version: 0, Values: map[string]string{"token": "abcdefgh"}}}, false},
		{"no values", AccountSecrets{Active: Slot{Version: 1}}, false},
		{"empty value", AccountSecrets{Active: Slot{Version: 1, Values: map[string]string{"token": ""}}}, false},
		{"next same version", AccountSecrets{Active: Slot{Version: 1, Values: map[string]string{"a": "abcdefgh"}}, Next: &Slot{Version: 1, Values: map[string]string{"a": "bcdefghi"}}}, false},
	}
	for _, tt := range tests {
		if err := tt.s.Validate(); (err == nil) != tt.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}
