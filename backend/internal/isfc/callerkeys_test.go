package isfc_test

import (
	"slices"
	"sync"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc"
)

func TestScopesFromPermissions(t *testing.T) {
	tests := []struct {
		name  string
		perms []string
		want  []string
	}{
		{"none", nil, nil},
		{"invoke only", []string{"HUB:INVOKE"}, []string{"invoke"}},
		{"all three in a fixed order", []string{"HUB:REQUESTS_READ", "HUB:DRY_RUN", "HUB:INVOKE"}, []string{"invoke", "dry_run", "requests_read"}},
		{"other permissions are ignored", []string{"TENANT:READ", "HUB_TENANT:ADMIN", "HUB:INVOKE"}, []string{"invoke"}},
		{"case and spaces", []string{" hub:dry_run "}, []string{"dry_run"}},
		{"duplicates collapse", []string{"HUB:INVOKE", "HUB:INVOKE"}, []string{"invoke"}},
		{"near misses grant nothing", []string{"HUB:INVOKE:ALL", "HUB_INVOKE", "INVOKE"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isfc.ScopesFromPermissions(tt.perms); !slices.Equal(got, tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestKeyMapReplaceAndLookup(t *testing.T) {
	var km isfc.KeyMap
	if _, ok := km.Lookup("sha256:x"); ok || km.Loaded() || km.Len() != 0 {
		t.Fatal("empty map found something")
	}
	km.Replace([]isfc.CallerEntry{{KeyID: "k1", Hash: "sha256:a", ClientID: "c1"}, {KeyID: "k2", Hash: "sha256:b", ClientID: "c2"}})
	if e, ok := km.Lookup("sha256:a"); !ok || e.KeyID != "k1" || km.Len() != 2 || !km.Loaded() {
		t.Fatalf("lookup %+v %v", e, ok)
	}
	km.Replace(nil) // every key revoked: loaded, empty
	if _, ok := km.Lookup("sha256:a"); ok || !km.Loaded() {
		t.Fatal("replace did not drop the old keys")
	}
}

func TestKeyMapConcurrentReplaceAndLookup(t *testing.T) {
	var km isfc.KeyMap
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 2000 {
				km.Lookup("sha256:a")
			}
		})
	}
	wg.Go(func() {
		for i := range 200 {
			km.Replace([]isfc.CallerEntry{{KeyID: "k", Hash: "sha256:a", ClientID: "c", Scopes: []string{"invoke"}}, {KeyID: "n", Hash: string(rune('a' + i%26))}})
		}
	})
	wg.Wait()
}
