package roles

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
)

// The follow loop retries every 5 s; an unloadable snapshot is one ERROR line per version, again
// only every unloadableEvery (HUB-451).
func TestUnloadableSnapshotIsLoggedOnceInAWhile(t *testing.T) {
	var buf bytes.Buffer
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	old := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = old })
	r := &roleRegistry{log: slog.New(slog.NewTextHandler(&buf, nil))}
	ctx := context.Background()
	err := &control.SnapshotLoadError{Version: 7, Cause: errors.New("boom")}
	lines := func() int { return strings.Count(buf.String(), "level=ERROR") }

	r.logUnloadable(ctx, err)
	if lines() != 1 || !strings.Contains(buf.String(), "archive_version=7") || !strings.Contains(buf.String(), "DEV_RESET=1") {
		t.Fatalf("first report: %q", buf.String())
	}
	for range 5 {
		now = now.Add(5 * time.Second)
		r.logUnloadable(ctx, err)
	}
	if lines() != 1 {
		t.Fatalf("repeats within the window logged: %d lines", lines())
	}
	now = now.Add(unloadableEvery)
	r.logUnloadable(ctx, err)
	if lines() != 2 {
		t.Fatalf("no reminder after the window: %d lines", lines())
	}
	r.logUnloadable(ctx, &control.SnapshotLoadError{Version: 8, Cause: errors.New("boom")})
	if lines() != 3 {
		t.Fatalf("a new version must be reported at once: %d lines", lines())
	}
}
