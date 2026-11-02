package adminapi

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
)

func init() { registrars = append(registrars, (*Server).registerSnapshots) }

func (s *Server) registerSnapshots() {
	s.handle("listSnapshots", RoleViewer, false, "GET /snapshots", s.listSnapshots)
	s.handle("getLatestSnapshot", RoleViewer, false, "GET /snapshots/latest", s.getLatestSnapshot)
	s.handle("getSnapshot", RoleViewer, false, "GET /snapshots/{snapshot_version}", s.getSnapshot)
	s.handle("getSnapshotBlob", RoleViewer, false, "GET /snapshots/{snapshot_version}/blob", s.getSnapshotBlob)
}

// maxBlobJSON bounds a decompressed blob (same bound as registry.Unpack).
const maxBlobJSON = 64 << 20

// longPollRecheck is how often a long poll re-reads the archive when no ping arrives (Redis
// down or a lost message): the archive is the truth, the ping only makes it fast.
const longPollRecheck = time.Second

func snapshotETag(v int64) string { return `"` + strconv.FormatInt(v, 10) + `"` }

// snapshotView is the contract's Snapshot.
func snapshotView(r control.SnapshotRow) map[string]any {
	var changeID, summary *string
	if r.ChangeID != "" {
		changeID = &r.ChangeID
		sum := r.Action + " " + r.ObjectID
		summary = &sum
	} else {
		summary = nullable(r.Reason)
	}
	return map[string]any{"version": r.Version, "checksum": r.Checksum, "author": r.Author, "change_id": changeID,
		"summary": summary, "created_at": r.CreatedAt, "blob_url": fmt.Sprintf("%s/snapshots/%d/blob", Base, r.Version)}
}

func (s *Server) archive() (*control.Archive, error) {
	if s.d.Archive != nil {
		return s.d.Archive, nil
	}
	if s.d.Publisher != nil && s.d.Publisher.Archive != nil {
		return s.d.Publisher.Archive, nil
	}
	return nil, errors.New("no snapshot archive")
}

func (s *Server) listSnapshots(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := page(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var before int64
	if len(cursor) == 1 {
		if before, err = strconv.ParseInt(cursor[0], 10, 64); err != nil || before < 1 {
			s.fail(w, r, badRequest("", "cursor is not a cursor of this list"))
			return
		}
	}
	a, err := s.archive()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := a.List(r.Context(), before, limit+1)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		next = nextCursor(strconv.FormatInt(rows[limit-1].Version, 10))
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, snapshotView(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

// knownVersion reads If-None-Match: "412" (or W/"412", or *); absent or unparsable is 0.
func knownVersion(r *http.Request) (int64, bool) {
	h := strings.TrimSpace(r.Header.Get("If-None-Match"))
	if h == "" {
		return 0, false
	}
	h = strings.Trim(strings.TrimPrefix(h, "W/"), `"`)
	v, err := strconv.ParseInt(h, 10, 64)
	if err != nil {
		return 0, true
	}
	return v, true
}

// getLatestSnapshot answers the newest snapshot; with If-None-Match and wait > 0 it holds the
// request until a newer one is archived (the hub:snapshot ping wakes it at once, the archive
// is re-read every second regardless) or wait elapses (304). An empty archive is version 0.
func (s *Server) getLatestSnapshot(w http.ResponseWriter, r *http.Request) {
	wait := 0
	if v := r.URL.Query().Get("wait"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 30000 {
			s.fail(w, r, badRequest("", "wait must be 0..30000 ms"))
			return
		}
		wait = n
	}
	a, err := s.archive()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	known, conditional := knownVersion(r)
	ctx := r.Context()
	var ping <-chan struct{}
	if conditional && wait > 0 && s.d.Redis != nil {
		sub := s.d.Redis.Subscribe(ctx, control.SnapshotChannel)
		defer func() { _ = sub.Close() }()
		if _, err := sub.Receive(ctx); err == nil { // subscribed before the first archive read: no ping is missed
			ch := make(chan struct{}, 1)
			msgs := sub.Channel()
			go func() {
				for range msgs {
					select {
					case ch <- struct{}{}:
					default:
					}
				}
			}()
			ping = ch
		}
	}
	deadline := time.NewTimer(time.Duration(wait) * time.Millisecond)
	defer deadline.Stop()
	recheck := time.NewTicker(longPollRecheck)
	defer recheck.Stop()
	for {
		row, err := a.Newest(ctx)
		switch {
		case errors.Is(err, control.ErrNoSnapshot):
			row = control.SnapshotRow{}
		case err != nil:
			s.fail(w, r, err)
			return
		}
		if row.Version > 0 && (!conditional || row.Version != known) {
			w.Header().Set("ETag", snapshotETag(row.Version))
			writeJSON(w, http.StatusOK, snapshotView(row))
			return
		}
		if wait == 0 {
			w.Header().Set("ETag", snapshotETag(row.Version))
			w.WriteHeader(http.StatusNotModified)
			return
		}
		select {
		case <-ping:
		case <-recheck.C:
		case <-deadline.C:
			wait = 0 // one last read, then 304
		case <-ctx.Done():
			return
		}
	}
}

func pathSnapshotVersion(r *http.Request) (int64, error) {
	v, err := strconv.ParseInt(r.PathValue("snapshot_version"), 10, 64)
	if err != nil || v < 1 {
		return 0, badRequest("", "snapshot_version must be a positive integer")
	}
	return v, nil
}

// blobConfig decompresses a stored blob and returns its JSON and config files.
func blobConfig(gz []byte) ([]byte, map[string]string, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot blob: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(zr, maxBlobJSON+1))
	if err != nil || len(raw) > maxBlobJSON {
		return nil, nil, fmt.Errorf("snapshot blob: unreadable or larger than %d bytes", maxBlobJSON)
	}
	var b struct {
		Config map[string]string `json:"config"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, nil, fmt.Errorf("snapshot blob: %w", err)
	}
	return raw, b.Config, nil
}

// objectOf names the registry object a config file belongs to.
func objectOf(path string) (objectType, id string, ok bool) {
	parts := strings.Split(path, "/")
	base := strings.TrimSuffix(parts[len(parts)-1], ".yaml")
	switch {
	case len(parts) == 2 && parts[0] == "vendors":
		return "vendor", base, true
	case len(parts) == 2 && parts[0] == "accounts":
		return "account", base, true
	case len(parts) == 2 && parts[0] == "capabilities":
		// events.track.v1.yaml → events.track@v1
		if i := strings.LastIndex(base, ".v"); i > 0 {
			return "capability", base[:i] + "@" + base[i+1:], true
		}
		return "capability", base, true
	case len(parts) == 2 && parts[0] == "tenants":
		return "tenant", base, true
	case len(parts) == 2 && parts[0] == "registrar":
		return "registrar_endpoint", base, true
	case len(parts) >= 2 && parts[0] == "transformers":
		return "transformer", parts[1], true
	case len(parts) >= 2 && parts[0] == "integrations":
		return "integration", parts[1], true
	}
	return "", "", false
}

// changedObjects compares two config trees object by object.
func changedObjects(prev, cur map[string]string) []map[string]any {
	type key struct{ typ, id string }
	files := func(tree map[string]string) map[key]map[string]string {
		out := map[key]map[string]string{}
		for p, b := range tree {
			if t, id, ok := objectOf(p); ok {
				k := key{t, id}
				if out[k] == nil {
					out[k] = map[string]string{}
				}
				out[k][p] = b
			}
		}
		return out
	}
	a, b := files(prev), files(cur)
	keys := slices.Collect(maps.Keys(a))
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(x, y key) int { return strings.Compare(x.typ+"/"+x.id, y.typ+"/"+y.id) })
	out := []map[string]any{}
	for _, k := range keys {
		before, inA := a[k]
		after, inB := b[k]
		change := "updated"
		switch {
		case !inA:
			change = "created"
		case !inB:
			change = "deleted"
		case maps.Equal(before, after):
			continue
		}
		out = append(out, map[string]any{"object_type": k.typ, "object_id": k.id, "change": change})
	}
	return out
}

func (s *Server) getSnapshot(w http.ResponseWriter, r *http.Request) {
	v, err := pathSnapshotVersion(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	a, err := s.archive()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	row, err := a.Get(r.Context(), v)
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	gz, err := a.Blob(r.Context(), row)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_, cur, err := blobConfig(gz)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var prevVersion *int64
	prevConfig := map[string]string{}
	if prev, err := a.Previous(r.Context(), v); err == nil {
		prevVersion = &prev.Version
		pgz, err := a.Blob(r.Context(), prev)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if _, prevConfig, err = blobConfig(pgz); err != nil {
			s.fail(w, r, err)
			return
		}
	} else if !errors.Is(err, control.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	view := snapshotView(row)
	view["size_bytes"] = len(gz)
	view["previous_version"] = prevVersion
	view["changed_objects"] = changedObjects(prevConfig, cur)
	w.Header().Set("ETag", snapshotETag(row.Version))
	writeJSON(w, http.StatusOK, view)
}

// getSnapshotBlob serves the registry.Pack JSON ({format, checksum, config, contracts}),
// decompressed, or the stored gzip bytes with Content-Encoding: gzip when the client accepts it.
func (s *Server) getSnapshotBlob(w http.ResponseWriter, r *http.Request) {
	v, err := pathSnapshotVersion(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	a, err := s.archive()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	row, err := a.Get(r.Context(), v)
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	gz, err := a.Blob(r.Context(), row)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("ETag", snapshotETag(row.Version))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("Vary", "Accept-Encoding")
	if acceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(gz) //nolint:gosec // archived registry JSON served as application/json, not HTML
		return
	}
	raw, _, err := blobConfig(gz)
	if err != nil {
		w.Header().Del("Content-Type")
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw) //nolint:gosec // archived registry JSON served as application/json, not HTML
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(enc), "gzip") && strings.ReplaceAll(strings.TrimSpace(q), " ", "") != "q=0" {
			return true
		}
	}
	return false
}
