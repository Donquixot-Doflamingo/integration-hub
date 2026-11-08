package adminapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// An unloadable newest snapshot answers an actionable error (HUB-451): the version and the
// cause, no file path, a stable meta entry, instead of "Internal error".
func TestFailExplainsAnUnloadableSnapshot(t *testing.T) {
	s, _ := testServer(t, Deps{Log: slog.New(slog.DiscardHandler)})
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"missing blob", fmt.Errorf("load: %w", &control.SnapshotLoadError{Version: 7,
			Cause: fmt.Errorf("open /var/hub/blobs/snapshots/7.json.gz: %w", os.ErrNotExist)}), "blob is missing"},
		{"corrupt blob", &control.SnapshotLoadError{Version: 7, Cause: fmt.Errorf("%w: unknown format 9", registry.ErrCorruptBlob)}, "unknown format 9"},
		{"unreadable blob", &control.SnapshotLoadError{Version: 7, Cause: fmt.Errorf("read /secret/path: permission denied")}, "could not be read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.fail(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, Base+"/integrations", nil), tc.err)
			var env struct{ Error Error }
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatal(err)
			}
			e := env.Error
			if rec.Code != 500 || e.Code != CodeInternalError || e.Retryable {
				t.Fatalf("%d %+v", rec.Code, e)
			}
			if !strings.Contains(e.Message, "v7") || !strings.Contains(e.Message, tc.want) {
				t.Fatalf("message %q must name v7 and %q", e.Message, tc.want)
			}
			if strings.Contains(e.Message, "/var/hub") || strings.Contains(e.Message, "/secret") {
				t.Fatalf("message leaks a path: %q", e.Message)
			}
			if e.Meta["cause"] != "snapshot_unloadable" || e.Meta["snapshot_version"] != float64(7) {
				t.Fatalf("meta %v", e.Meta)
			}
		})
	}
}
