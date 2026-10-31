package creds

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// countingLoader wraps a Loader and counts decrypting calls.
type countingLoader struct {
	inner Loader
	calls atomic.Int64
	fail  atomic.Bool
}

func (l *countingLoader) Load(ctx context.Context, ids []string) (map[string]AccountSecrets, error) {
	l.calls.Add(1)
	if l.fail.Load() {
		return nil, ErrDecrypt
	}
	return l.inner.Load(ctx, ids)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fixture struct {
	cache   *Cache
	loader  *countingLoader
	secrets *telemetry.SecretSet
	clock   *clock
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	f, _ := newTestFile(t)
	l := &countingLoader{inner: f}
	clk := &clock{t: time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)}
	secrets := telemetry.NewSecretSet()
	c, err := NewCache(l, secrets, telemetry.NewMetrics("test", "dev"), CacheOptions{Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{cache: c, loader: l, secrets: secrets, clock: clk}
}

var devAccounts = []string{"ct-brandA-in", "ct-brandB-us"}

// TestNoDecryptOnRequestPath is the HUB-084 acceptance test: after Warm, request-path Gets
// (hits and misses alike) never reach the decrypting loader.
func TestNoDecryptOnRequestPath(t *testing.T) {
	fx := newFixture(t)
	if err := fx.cache.Warm(t.Context(), devAccounts); err != nil {
		t.Fatal(err)
	}
	if got := fx.loader.calls.Load(); got != 1 {
		t.Fatalf("warm decrypted %d times, want 1", got)
	}
	for range 1000 {
		c, err := fx.cache.Get(t.Context(), "ct-brandA-in")
		if err != nil || c.Values["passcode"] != "test-passcode-A-not-real" || c.Version != 1 || c.AccountID != "ct-brandA-in" {
			t.Fatalf("Get = %+v, %v", c, err)
		}
	}
	_, err := fx.cache.Get(t.Context(), "unknown-account")
	var miss *MissError
	if !errors.As(err, &miss) || !errors.Is(err, ErrCacheMiss) || miss.AccountID != "unknown-account" {
		t.Fatalf("miss err = %v", err)
	}
	if got := fx.loader.calls.Load(); got != 1 {
		t.Fatalf("request path decrypted: loader calls = %d, want 1", got)
	}
	if got := testutil.ToFloat64(fx.cache.misses.WithLabelValues("unknown-account")); got != 1 {
		t.Fatalf("creds_cache_miss_total = %v, want 1", got)
	}
	if got := testutil.ToFloat64(fx.cache.decrypts); got != 1 {
		t.Fatalf("creds_decrypts_total = %v, want 1", got)
	}
}

func TestGetVersionServesNextSlot(t *testing.T) {
	fx := newFixture(t)
	if err := fx.cache.Warm(t.Context(), devAccounts); err != nil {
		t.Fatal(err)
	}
	act, err := fx.cache.Get(t.Context(), "ct-brandB-us")
	if err != nil || act.Version != 3 {
		t.Fatalf("active = %+v, %v", act, err)
	}
	nxt, err := fx.cache.GetVersion(t.Context(), "ct-brandB-us", 4)
	if err != nil || nxt.Values["passcode"] != "test-passcode-B2-not-real" {
		t.Fatalf("next = %+v, %v", nxt, err)
	}
	if _, err := fx.cache.GetVersion(t.Context(), "ct-brandB-us", 9); !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("unknown version err = %v", err)
	}
}

func TestEntriesExpireAfterTTL(t *testing.T) {
	fx := newFixture(t)
	if err := fx.cache.Warm(t.Context(), devAccounts); err != nil {
		t.Fatal(err)
	}
	fx.clock.Advance(4*time.Minute + 59*time.Second)
	if _, err := fx.cache.Get(t.Context(), "ct-brandA-in"); err != nil {
		t.Fatalf("before TTL: %v", err)
	}
	fx.clock.Advance(time.Second)
	if _, err := fx.cache.Get(t.Context(), "ct-brandA-in"); !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("after TTL err = %v, want a miss", err)
	}
	if err := fx.cache.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.cache.Get(t.Context(), "ct-brandA-in"); err != nil {
		t.Fatalf("after refresh: %v", err)
	}
}

func TestFailedReloadKeepsLastGoodUntilTTL(t *testing.T) {
	fx := newFixture(t)
	if err := fx.cache.Warm(t.Context(), devAccounts); err != nil {
		t.Fatal(err)
	}
	fx.loader.fail.Store(true)
	fx.clock.Advance(2 * time.Minute)
	if err := fx.cache.Refresh(t.Context()); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("refresh err = %v", err)
	}
	if _, err := fx.cache.Get(t.Context(), "ct-brandA-in"); err != nil {
		t.Fatalf("last good entry dropped: %v", err)
	}
	fx.clock.Advance(3 * time.Minute)
	if _, err := fx.cache.Get(t.Context(), "ct-brandA-in"); !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("stale entry served past TTL: %v", err)
	}
}

func TestWarmReplacesAccountSet(t *testing.T) {
	fx := newFixture(t)
	if err := fx.cache.Warm(t.Context(), devAccounts); err != nil {
		t.Fatal(err)
	}
	err := fx.cache.Warm(t.Context(), []string{"ct-brandA-in", "ct-new"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("warm with an unknown account: err = %v, want ErrNotFound", err)
	}
	if _, err := fx.cache.Get(t.Context(), "ct-brandA-in"); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.cache.Get(t.Context(), "ct-brandB-us"); !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("account dropped from the snapshot still served: %v", err)
	}
}

// TestLoadedSecretsAreRedacted: every loaded value (active and next) and the basic-auth
// encoding are registered, so a log line carrying them is redacted.
func TestLoadedSecretsAreRedacted(t *testing.T) {
	key, _ := GenerateKey()
	f, err := NewLocalFile(t.TempDir()+"/s.enc", key)
	if err != nil {
		t.Fatal(err)
	}
	all := devSecrets()
	all["tw-basic"] = AccountSecrets{Active: Slot{Version: 1, Values: map[string]string{"username": "ACtest-user-not-real", "password": "test-password-not-real"}}}
	if err := f.WriteAll(all); err != nil {
		t.Fatal(err)
	}
	secrets := telemetry.NewSecretSet()
	c, err := NewCache(f, secrets, telemetry.NewMetrics("test", "dev"), CacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Warm(t.Context(), append(devAccounts, "tw-basic")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	log := slog.New(telemetry.NewRedactHandler(slog.NewJSONHandler(&buf, nil), secrets))
	values := []string{
		"TEST-ACCT-A-not-real", "test-passcode-A-not-real", "test-passcode-B-not-real", "test-passcode-B2-not-real",
		"ACtest-user-not-real", "test-password-not-real", basicToken("ACtest-user-not-real", "test-password-not-real"),
	}
	for _, v := range values {
		log.InfoContext(t.Context(), "outbound", slog.String("header", "Basic "+v), slog.Any("error", errors.New("vendor said "+v)))
	}
	out := buf.String()
	for _, v := range values {
		if strings.Contains(out, v) {
			t.Fatalf("secret %q reached the log:\n%s", v, out)
		}
	}
	if !strings.Contains(out, telemetry.Redacted) {
		t.Fatal("expected redaction markers")
	}
}

// TestRunRefreshesInBackground: a miss triggers an early reload off the request path, and the
// periodic refresh keeps entries alive past the TTL.
func TestRunRefreshesInBackground(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, _ := newTestFile(t)
		l := &countingLoader{inner: f}
		c, err := NewCache(l, telemetry.NewSecretSet(), telemetry.NewMetrics("test", "dev"), CacheOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Warm(t.Context(), devAccounts); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); c.Run(ctx) }()

		time.Sleep(2 * time.Second) // past MinRefreshGap
		if _, err := c.Get(ctx, "ct-missing"); !errors.Is(err, ErrCacheMiss) {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := l.calls.Load(); got != 2 {
			t.Fatalf("miss-triggered reload: loader calls = %d, want 2", got)
		}
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		if _, err := c.Get(ctx, "ct-brandA-in"); err != nil {
			t.Fatalf("periodic refresh did not keep the entry fresh: %v", err)
		}
		if got := l.calls.Load(); got < 5 {
			t.Fatalf("periodic reloads = %d, want >= 5 over 10 min at 2.5 min", got)
		}
		cancel()
		<-done
	})
}

func TestAccountIDsSkipsRetired(t *testing.T) {
	snap := &registry.Snapshot{Accounts: map[string]*registry.Account{
		"b": {Account: "b", State: registry.AccountDraining},
		"a": {Account: "a", State: registry.AccountActive},
		"r": {Account: "r", State: registry.AccountRetired},
	}}
	if got := strings.Join(AccountIDs(snap), ","); got != "a,b" {
		t.Fatalf("AccountIDs = %s", got)
	}
}

func TestNewCacheRejectsDoubleRegistration(t *testing.T) {
	m := telemetry.NewMetrics("test", "dev")
	f, _ := newTestFile(t)
	if _, err := NewCache(f, telemetry.NewSecretSet(), m, CacheOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCache(f, telemetry.NewSecretSet(), m, CacheOptions{}); err == nil {
		t.Fatal("second cache on the same registry must fail")
	}
}

func TestMissingFileIsNotExist(t *testing.T) {
	key, _ := GenerateKey()
	f, _ := NewLocalFile(t.TempDir()+"/absent.enc", key)
	_, err := f.Load(t.Context(), devAccounts)
	if !IsNotExist(err) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}
