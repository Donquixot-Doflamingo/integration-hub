package creds

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

const (
	regActive = "whsec-test-active-0123456789abcdef" //nolint:gosec // G101: fake test secret
	regNext   = "whsec-test-next-fedcba9876543210"   //nolint:gosec // G101: fake test secret
)

func TestSecretID(t *testing.T) {
	for _, tc := range []struct {
		ref, want string
		ok        bool
	}{
		{"localfile://reg_orders_cb", "reg_orders_cb", true},
		{"localfile://", "", false},
		{"kms://arn:aws:kms:key/1", "", false},
		{"reg_orders_cb", "", false},
		{"localfile://a/b", "", false},
	} {
		got, err := SecretID(tc.ref)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("SecretID(%q) = %q, %v; want %q ok=%v", tc.ref, got, err, tc.want, tc.ok)
		}
		if err != nil && !errors.Is(err, ErrBadSecretRef) {
			t.Errorf("SecretID(%q) error %v is not ErrBadSecretRef", tc.ref, err)
		}
	}
}

func registrarSnapshot() *registry.Snapshot {
	return &registry.Snapshot{
		Accounts: map[string]*registry.Account{
			"ct-brandA-in": {Account: "ct-brandA-in", State: registry.AccountActive},
		},
		Registrar: map[string]*registry.RegistrarEndpoint{
			"reg_orders_cb": {EndpointID: "reg_orders_cb", SecretRef: "localfile://reg_orders_cb"},
			"reg_alias":     {EndpointID: "reg_alias", SecretRef: "localfile://reg_orders_cb"},
			"reg_bad":       {EndpointID: "reg_bad", SecretRef: "vault://x"},
		},
	}
}

func TestRegistrarSecretIDs(t *testing.T) {
	snap := registrarSnapshot()
	if got := RegistrarSecretIDs(snap); !slices.Equal(got, []string{"reg_orders_cb"}) {
		t.Fatalf("RegistrarSecretIDs = %v", got)
	}
	if got := SnapshotSecretIDs(snap); !slices.Equal(got, []string{"ct-brandA-in", "reg_orders_cb"}) {
		t.Fatalf("SnapshotSecretIDs = %v", got)
	}
}

// TestRegistrarSecretsEncryptedAndRedacted is the HUB-119 "signing secrets stored encrypted and
// registered for redaction" criterion: the secret is only in the AES-GCM file, the warmed cache
// serves active then next (rotation overlap) without decrypting again, and both values are
// redacted from logs once loaded.
func TestRegistrarSecretsEncryptedAndRedacted(t *testing.T) {
	fx := newFixture(t)
	f := fx.loader.inner.(*LocalFile)
	all, err := f.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	all["reg_orders_cb"] = AccountSecrets{
		Active: Slot{Version: 1, Values: map[string]string{SigningSecretKey: regActive}},
		Next:   &Slot{Version: 2, Values: map[string]string{SigningSecretKey: regNext}},
	}
	all["reg_short"] = AccountSecrets{Active: Slot{Version: 1, Values: map[string]string{SigningSecretKey: "short-1"}}}
	all["reg_nokey"] = AccountSecrets{Active: Slot{Version: 1, Values: map[string]string{"other": "some-other-value"}}}
	if err := f.WriteAll(all); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(f.Path())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(regActive)) || bytes.Contains(raw, []byte(regNext)) {
		t.Fatal("signing secret stored in clear")
	}

	ids := append(SnapshotSecretIDs(registrarSnapshot()), "reg_short", "reg_nokey")
	if err := fx.cache.Warm(t.Context(), ids); err != nil {
		t.Fatal(err)
	}
	calls := fx.loader.calls.Load()
	rs := RegistrarSecrets{Cache: fx.cache}
	got, err := rs.SigningSecrets(t.Context(), "localfile://reg_orders_cb")
	if err != nil || len(got) != 2 || string(got[0]) != regActive || string(got[1]) != regNext {
		t.Fatalf("SigningSecrets = %q, %v; want [active next]", got, err)
	}
	if fx.loader.calls.Load() != calls {
		t.Fatal("SigningSecrets decrypted")
	}

	for _, tc := range []struct {
		ref  string
		want error
	}{
		{"localfile://reg_short", ErrMissingCredential},
		{"localfile://reg_nokey", ErrMissingCredential},
		{"localfile://reg_unknown", ErrCacheMiss},
		{"vault://x", ErrBadSecretRef},
	} {
		if _, err := rs.SigningSecrets(t.Context(), tc.ref); !errors.Is(err, tc.want) {
			t.Errorf("SigningSecrets(%q) err = %v, want %v", tc.ref, err, tc.want)
		}
		if _, err := rs.SigningSecrets(t.Context(), tc.ref); err != nil && strings.Contains(err.Error(), "short-1") {
			t.Errorf("error leaks the secret value: %v", err)
		}
	}

	var buf bytes.Buffer
	log := telemetry.NewLogger(&buf, slog.LevelInfo, fx.secrets, "test")
	log.InfoContext(t.Context(), "callback.debug", slog.String("detail", "sig with "+regActive+" and "+regNext))
	if out := buf.String(); strings.Contains(out, regActive) || strings.Contains(out, regNext) || !strings.Contains(out, telemetry.Redacted) {
		t.Fatalf("signing secrets not redacted: %s", out)
	}
}

func TestSlotsWithoutNext(t *testing.T) {
	fx := newFixture(t)
	if err := fx.cache.Warm(t.Context(), devAccounts); err != nil {
		t.Fatal(err)
	}
	a, err := fx.cache.Slots(t.Context(), "ct-brandA-in")
	if err != nil || len(a) != 1 {
		t.Fatalf("Slots(no next) = %+v, %v", a, err)
	}
	b, err := fx.cache.Slots(t.Context(), "ct-brandB-us")
	if err != nil || len(b) != 2 || b[0].Version != 3 || b[1].Version != 4 {
		t.Fatalf("Slots(with next) = %+v, %v", b, err)
	}
}

// TestSlotsDropExpiredNext: a next slot past its expires_at (the end of a registrar secret's
// rotation overlap) is no longer returned, without another decrypt.
func TestSlotsDropExpiredNext(t *testing.T) {
	fx := newFixture(t)
	f, _ := newTestFile(t)
	until := fx.clock.Now().Add(time.Minute)
	all := devSecrets()
	all["reg_rot"] = AccountSecrets{Active: Slot{Version: 2, Values: map[string]string{SigningSecretKey: "new-signing-secret-0002"}},
		Next: &Slot{Version: 1, Values: map[string]string{SigningSecretKey: "old-signing-secret-0001"}, ExpiresAt: &until}}
	if err := f.WriteAll(all); err != nil {
		t.Fatal(err)
	}
	fx.loader.inner = f
	if err := fx.cache.Warm(t.Context(), []string{"reg_rot"}); err != nil {
		t.Fatal(err)
	}
	if got, err := fx.cache.Slots(t.Context(), "reg_rot"); err != nil || len(got) != 2 || got[0].Version != 2 {
		t.Fatalf("during the overlap: %+v %v", got, err)
	}
	fx.clock.Advance(time.Minute) // still inside the cache TTL: no reload decides this
	if got, err := fx.cache.Slots(t.Context(), "reg_rot"); err != nil || len(got) != 1 || got[0].Version != 2 {
		t.Fatalf("after the overlap: %+v %v", got, err)
	}
}
