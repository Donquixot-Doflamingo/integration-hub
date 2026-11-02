package adminapi

import (
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestChangedObjects(t *testing.T) {
	prev := map[string]string{
		"vendors/clevertap.yaml": "v", "accounts/ct-a.yaml": "a", "integrations/int_x/integration.yaml": "i",
		"integrations/int_x/versions/1.yaml": "1", "transformers/T1/7.yaml": "t", "capabilities/events.track.v1.yaml": "c",
	}
	cur := map[string]string{
		"vendors/clevertap.yaml": "v", "integrations/int_x/integration.yaml": "i",
		"integrations/int_x/versions/1.yaml": "1", "integrations/int_x/versions/2.yaml": "2", "transformers/T1/7.yaml": "t",
		"capabilities/events.track.v1.yaml": "c2", "registrar/reg_cb.yaml": "r", "README.md": "ignored",
	}
	got := fmt.Sprint(changedObjects(prev, cur))
	want := "[map[change:deleted object_id:ct-a object_type:account] map[change:updated object_id:events.track@v1 object_type:capability] " +
		"map[change:updated object_id:int_x object_type:integration] map[change:created object_id:reg_cb object_type:registrar_endpoint]]"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestConditionalHeaders(t *testing.T) {
	for _, tc := range []struct {
		inm  string
		v    int64
		cond bool
	}{{"", 0, false}, {`"412"`, 412, true}, {`W/"7"`, 7, true}, {"*", 0, true}} {
		r := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
		if tc.inm != "" {
			r.Header.Set("If-None-Match", tc.inm)
		}
		if v, c := knownVersion(r); v != tc.v || c != tc.cond {
			t.Errorf("%q: %d %v", tc.inm, v, c)
		}
	}
	for enc, want := range map[string]bool{"": false, "gzip": true, "br, gzip;q=0.5": true, "gzip;q=0": false, "identity": false} {
		r := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
		r.Header.Set("Accept-Encoding", enc)
		if acceptsGzip(r) != want {
			t.Errorf("Accept-Encoding %q: %v", enc, !want)
		}
	}
}
