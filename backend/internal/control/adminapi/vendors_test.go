package adminapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

func TestIfMatch(t *testing.T) {
	for _, c := range []struct {
		header string
		want   bool
	}{{"", true}, {"*", true}, {`"3"`, true}, {`W/"3"`, true}, {`"2", "3"`, true}, {`"2"`, false}, {`3`, false}} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/", nil)
		if c.header != "" {
			r.Header.Set("If-Match", c.header)
		}
		if got := ifMatch(r, `"3"`); got != c.want {
			t.Errorf("If-Match %q: %v", c.header, got)
		}
	}
}

func TestVendorFieldsApply(t *testing.T) {
	base := func() vendorFieldsIn {
		return vendorFieldsIn{DisplayName: "Acme", AuthScheme: "bearer", Regions: map[string]string{"in": "https://in.acme.test"}}
	}
	ms := func(v int) *int { return &v }
	cases := []struct {
		name    string
		edit    func(*vendorFieldsIn)
		pointer string // "" = accepted
	}{
		{"minimal", func(*vendorFieldsIn) {}, ""},
		{"no display name", func(in *vendorFieldsIn) { in.DisplayName = "" }, "/display_name"},
		{"unknown scheme", func(in *vendorFieldsIn) { in.AuthScheme = "kerberos" }, "/auth_scheme"},
		{"unimplemented scheme", func(in *vendorFieldsIn) { in.AuthScheme = "oauth2_client_credentials" }, "/auth_scheme"},
		{"no regions", func(in *vendorFieldsIn) { in.Regions = nil }, "/regions"},
		{"relative region URL", func(in *vendorFieldsIn) { in.Regions["in"] = "in.acme.test" }, "/regions/in"},
		{"breaker ms", func(in *vendorFieldsIn) {
			in.Defaults = &vendorDefaultsIn{Breaker: &breakerMS{WindowMS: ms(30000), OpenMaxMS: ms(600000)}}
		}, ""},
		{"breaker sub-second", func(in *vendorFieldsIn) { in.Defaults = &vendorDefaultsIn{Breaker: &breakerMS{WindowMS: ms(1500)}} }, "/defaults/breaker/window_ms"},
		{"bad webhook scheme", func(in *vendorFieldsIn) { in.WebhookVerification = json.RawMessage(`{"scheme":"rsa"}`) }, "/webhook_verification/scheme"},
		{"webhook unknown field", func(in *vendorFieldsIn) { in.WebhookVerification = json.RawMessage(`{"scheme":"none","x":1}`) }, "/webhook_verification"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base()
			c.edit(&in)
			var v registry.Vendor
			var m vendorMeta
			err := in.apply(&v, &m)
			if c.pointer == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var e *Error
			ok := errors.As(err, &e)
			if !ok || e.Status != 400 || e.Details[0].Pointer != c.pointer {
				t.Fatalf("want 400 at %s, got %v", c.pointer, err)
			}
		})
	}
	in := base()
	in.Defaults = &vendorDefaultsIn{Breaker: &breakerMS{WindowMS: ms(30000), OpenMaxMS: ms(600000)}}
	var v registry.Vendor
	var m vendorMeta
	if err := in.apply(&v, &m); err != nil || v.Defaults.Breaker.WindowS != 30 || v.Defaults.Breaker.OpenMaxS != 600 {
		t.Fatalf("ms → s: %+v %v", v.Defaults.Breaker, err)
	}
	if b := defaultsView(v.Defaults)["breaker"].(map[string]any); b["window_ms"] != 30000 || b["open_max_ms"] != 600000 {
		t.Fatalf("s → ms: %v", b)
	}
}

func TestEndpointOrderAndPages(t *testing.T) {
	refs := endpointOrder([]string{"upload@10", "profile@1", "upload@2", "upload@1"})
	if !slices.Equal(refs, []string{"profile@1", "upload@1", "upload@2", "upload@10"}) {
		t.Fatalf("order %v", refs)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/?limit=3", nil)
	pg, next, err := pageOf(r, refs)
	if err != nil || len(pg) != 3 || next == nil {
		t.Fatalf("page 1 %v %v %v", pg, next, err)
	}
	r = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/?limit=3&cursor="+*next, nil)
	if pg, next, err = pageOf(r, refs); err != nil || !slices.Equal(pg, []string{"upload@10"}) || next != nil {
		t.Fatalf("page 2 %v %v %v", pg, next, err)
	}
	r = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/?cursor="+*nextCursor("gone@1"), nil)
	if _, _, err := pageOf(r, refs); err == nil {
		t.Fatal("a foreign cursor was accepted")
	}
}
