package roles

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

func testSecrets() map[string]creds.AccountSecrets {
	return map[string]creds.AccountSecrets{"acct-a": {Active: creds.Slot{Version: 1, Values: map[string]string{"api_key": "not-a-real-key"}}}}
}

// TestReopeningLoaderPicksUpKeyAndFileCreatedLater is HUB-454: a key file missing at start no
// longer makes the loader empty for the life of the process.
func TestReopeningLoaderPicksUpKeyAndFileCreatedLater(t *testing.T) {
	dir := t.TempDir()
	keyFile, path := filepath.Join(dir, "dev.key"), filepath.Join(dir, "secrets.enc")
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	l, err := newReopeningLoader(t.Context(), path, keyFile, log)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 { // missing key: empty store, one warning
		if got, err := l.Load(t.Context(), []string{"acct-a"}); err != nil || len(got) != 0 {
			t.Fatalf("no key: %v, %v", got, err)
		}
	}
	if n := strings.Count(buf.String(), "no credential key file"); n != 1 {
		t.Fatalf("key warnings = %d, want 1:\n%s", n, buf.String())
	}

	key, _ := creds.GenerateKey() // hubctl creds init-key, then creds set
	if err := creds.WriteKeyFile(keyFile, key, false); err != nil {
		t.Fatal(err)
	}
	for range 3 { // key but no file: empty store, one warning naming the path
		if got, err := l.Load(t.Context(), []string{"acct-a"}); err != nil || len(got) != 0 {
			t.Fatalf("no file: %v, %v", got, err)
		}
	}
	if n := strings.Count(buf.String(), "no credentials file"); n != 1 || !strings.Contains(buf.String(), path) {
		t.Fatalf("file warnings = %d, want 1 naming %s:\n%s", n, path, buf.String())
	}
	lf, _ := creds.NewLocalFile(path, key)
	if err := lf.WriteAll(testSecrets()); err != nil {
		t.Fatal(err)
	}
	got, err := l.Load(t.Context(), []string{"acct-a"})
	if err != nil || got["acct-a"].Active.Version != 1 {
		t.Fatalf("after set: %v, %v", got, err)
	}

	if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil { // corrupt stays an error
		t.Fatal(err)
	}
	if _, err := l.Load(t.Context(), []string{"acct-a"}); !errors.Is(err, creds.ErrDecrypt) {
		t.Fatalf("corrupt file: err = %v, want ErrDecrypt", err)
	}
}

func TestReopeningLoaderRejectsBadKeyAtStart(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "dev.key")
	if err := os.WriteFile(keyFile, []byte("not base64!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newReopeningLoader(t.Context(), "x.enc", keyFile, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("a corrupt key file must stay a startup error")
	}
}

// TestRotationReloadLogIsTruthful: Info only when the cache serves what it was asked for.
func TestRotationReloadLogIsTruthful(t *testing.T) {
	dir := t.TempDir()
	keyFile, path := filepath.Join(dir, "dev.key"), filepath.Join(dir, "secrets.enc")
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	l, err := newReopeningLoader(t.Context(), path, keyFile, log)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := creds.NewCache(l, telemetry.NewSecretSet(), telemetry.NewMetrics("test", "dev"), creds.CacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Warm(t.Context(), []string{"acct-a"}); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	logRotationReload(t.Context(), log, cache, "acct-a", cache.Refresh(t.Context()))
	if strings.Contains(buf.String(), `"msg":"credentials reloaded after rotation"`) || !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Fatalf("empty cache must warn, not report success:\n%s", buf.String())
	}

	key, _ := creds.GenerateKey()
	if err := creds.WriteKeyFile(keyFile, key, false); err != nil {
		t.Fatal(err)
	}
	lf, _ := creds.NewLocalFile(path, key)
	if err := lf.WriteAll(testSecrets()); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	logRotationReload(t.Context(), log, cache, "acct-a", cache.Refresh(t.Context()))
	if !strings.Contains(buf.String(), `"msg":"credentials reloaded after rotation"`) {
		t.Fatalf("loaded cache must report success:\n%s", buf.String())
	}
}
