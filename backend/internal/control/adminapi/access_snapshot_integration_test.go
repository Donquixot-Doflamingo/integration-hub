//go:build integration

package adminapi

import (
	"net/http"
	"testing"
)

// HUB-451: tenants / accounts / api-keys reads reuse the snapshot cached by archive checksum
// instead of rebuilding the blob on every call (which also moved BuiltAt to "now").
func TestAccessReadsReuseTheCachedSnapshot(t *testing.T) {
	a := newAdminRig(t)
	// The first publish archives a snapshot; the reads below then go through the cache.
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/integrations/int_evt_brandA/pause", `{"reason":"cache check"}`); resp.Code != 200 {
		t.Fatalf("pause: %d %s", resp.Code, resp.Body)
	}
	first, err := a.s.accessSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.s.accessSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !first.BuiltAt.Equal(second.BuiltAt) {
		t.Fatal("accessSnapshot rebuilt the snapshot between two reads of an unchanged archive")
	}
	if resp, _ := a.do(RoleViewer, http.MethodGet, "/accounts", ""); resp.Code != 200 {
		t.Fatalf("accounts: %d", resp.Code)
	}
}
