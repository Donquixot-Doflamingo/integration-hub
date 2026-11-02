//go:build integration

package control_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
)

// TestCredentialRotation is HUB-177: add next → promote → delete old, each step audited in the
// change log with slot metadata only and announced to the nodes.
func TestCredentialRotation(t *testing.T) {
	_, pool := publisher(t)
	key, _ := creds.GenerateKey()
	store, err := creds.NewLocalFile(filepath.Join(t.TempDir(), "secrets.enc"), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteAll(map[string]creds.AccountSecrets{
		"ct-brandA-in": {Active: creds.Slot{Version: 3, Values: map[string]string{"passcode": "old-passcode-3"}}},
	}); err != nil {
		t.Fatal(err)
	}
	var pinged []string
	r := &control.Rotator{Pool: pool, Store: store, Notify: func(_ context.Context, a string) { pinged = append(pinged, a) }}
	ctx := t.Context()

	if _, err := r.Promote(ctx, "ct-brandA-in", 4, "release@example.com", "too early"); !errors.Is(err, creds.ErrRotationConflict) {
		t.Fatalf("promote without next: %v", err)
	}
	if _, err := r.AddNext(ctx, "ct-brandA-in", map[string]string{"passcode": "new-passcode-4"}, nil, "", "x"); !errors.Is(err, control.ErrMissingAuthor) {
		t.Fatalf("missing author: %v", err)
	}
	if _, err := r.AddNext(ctx, "nope", map[string]string{"passcode": "new-passcode-4"}, nil, "a", "x"); !errors.Is(err, control.ErrUnknownAccount) {
		t.Fatalf("unknown account: %v", err)
	}
	add, err := r.AddNext(ctx, "ct-brandA-in", map[string]string{"passcode": "new-passcode-4"}, nil, "release@example.com", "quarterly rotation")
	if err != nil {
		t.Fatal(err)
	}
	pro, err := r.Promote(ctx, "ct-brandA-in", 4, "release@example.com", "connection test passed")
	if err != nil {
		t.Fatal(err)
	}
	if len(pro.Slots) != 2 || pro.Slots[0].Version != 4 || pro.Slots[1].Slot != "previous" || pro.Slots[1].Version != 3 {
		t.Fatalf("after promote %+v", pro.Slots)
	}
	all, _ := store.ReadAll()
	if all["ct-brandA-in"].Active.Values["passcode"] != "new-passcode-4" {
		t.Fatal("the store does not serve the promoted credential")
	}
	del, err := r.DeleteOld(ctx, "ct-brandA-in", 3, "release@example.com", "no auth failures for 15 min")
	if err != nil {
		t.Fatal(err)
	}
	if len(pinged) != 3 || pinged[0] != "ct-brandA-in" {
		t.Fatalf("pings %v", pinged)
	}
	for _, id := range []string{add.ChangeID, pro.ChangeID, del.ChangeID} {
		var action string
		var before, after json.RawMessage
		if err := pool.QueryRow(ctx, `SELECT action, before, after FROM hub.change_log WHERE change_id = $1`, id).Scan(&action, &before, &after); err != nil {
			t.Fatalf("change %s: %v", id, err)
		}
		if strings.Contains(string(before)+string(after), "-passcode-") {
			t.Fatalf("%s change row carries secret material: %s %s", action, before, after)
		}
	}
}
