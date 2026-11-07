//go:build integration

package adminapi

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSnapshotsListAndGet is HUB-204: snapshots newest first with their change, a snapshot's
// changed objects versus the previous one, and the blob as JSON (or gzip on request).
func TestSnapshotsListAndGet(t *testing.T) {
	a := newDraftRig(t)
	publishUpTo(t, a, 4) // archive v1 = v3 published from the dev tree, v2 = v4
	resp, body := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/pause", `{"reason":"snapshot test"}`)
	if resp.Code != 200 {
		t.Fatalf("pause: %d %v", resp.Code, body)
	}
	resp, body = a.do(RoleViewer, http.MethodGet, "/snapshots?limit=2", "")
	items, _ := body["items"].([]any)
	if resp.Code != 200 || len(items) != 2 || body["next_cursor"] == nil {
		t.Fatalf("list: %d %v", resp.Code, body)
	}
	top := items[0].(map[string]any)
	if top["version"] != 3.0 || top["summary"] != "pauseIntegration int_evt_brandA" || top["change_id"] == nil ||
		top["blob_url"] != "/admin/v1/snapshots/3/blob" {
		t.Fatalf("newest %v", top)
	}
	_, body = a.do(RoleViewer, http.MethodGet, "/snapshots?cursor="+body["next_cursor"].(string), "")
	if n := len(body["items"].([]any)); n != 1 {
		t.Fatalf("second page %d items", n)
	}

	resp, body = a.do(RoleViewer, http.MethodGet, "/snapshots/3", "")
	if resp.Code != 200 || body["previous_version"] != 2.0 || resp.Header().Get("ETag") != `"3"` || body["size_bytes"].(float64) <= 0 {
		t.Fatalf("get: %d %v", resp.Code, body)
	}
	changed := fmt.Sprint(body["changed_objects"])
	if changed != "[map[change:updated object_id:int_evt_brandA object_type:integration]]" {
		t.Fatalf("changed objects %s", changed)
	}
	if _, body = a.do(RoleViewer, http.MethodGet, "/snapshots/1", ""); body["previous_version"] != nil || len(body["changed_objects"].([]any)) < 5 {
		t.Fatalf("first snapshot: %v", body)
	}
	if resp, _ := a.do(RoleViewer, http.MethodGet, "/snapshots/99", ""); resp.Code != 404 {
		t.Fatalf("missing snapshot: %d", resp.Code)
	}

	resp, _ = a.do(RoleViewer, http.MethodGet, "/snapshots/3/blob", "")
	var blob struct {
		Format   int               `json:"format"`
		Checksum string            `json:"checksum"`
		Config   map[string]string `json:"config"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &blob); err != nil || resp.Header().Get("Content-Type") != "application/json" ||
		blob.Format != 2 || blob.Checksum != top["checksum"] || !strings.Contains(blob.Config["integrations/int_evt_brandA/integration.yaml"], "paused: true") {
		t.Fatalf("blob: %v %s", err, resp.Body.String()[:min(200, resp.Body.Len())])
	}
	req := a.request(RoleViewer, http.MethodGet, "/snapshots/3/blob", "")
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder() // not contract-validated: a gzip body is not JSON to the validator
	a.h.ServeHTTP(rec, req)
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip blob: %v %v", err, rec.Header())
	}
	raw, _ := io.ReadAll(zr)
	if !bytes.Equal(bytes.TrimSpace(raw), bytes.TrimSpace(resp.Body.Bytes())) {
		t.Fatal("gzip and plain blobs differ")
	}
}

// TestLatestSnapshotLongPoll is HUB-204's acceptance: a long poll on the current version
// answers within 1 s of the next publish; without a publish it answers 304 after wait.
func TestLatestSnapshotLongPoll(t *testing.T) {
	a := newDraftRig(t)
	publishUpTo(t, a, 3)
	resp, body := a.do(RoleViewer, http.MethodGet, "/snapshots/latest", "")
	if resp.Code != 200 || body["version"] != 1.0 || resp.Header().Get("ETag") != `"1"` || body["blob_url"] != "/admin/v1/snapshots/1/blob" {
		t.Fatalf("latest: %d %v %v", resp.Code, body, resp.Header())
	}
	cond := func(wait int) *http.Request {
		req := a.request(RoleViewer, http.MethodGet, "/snapshots/latest?wait="+strconv.Itoa(wait), "")
		req.Header.Set("If-None-Match", `"1"`)
		return req
	}
	start := time.Now()
	rec := a.spec.Serve(t, a.h, cond(300))
	if rec.Code != 304 || rec.Header().Get("ETag") != `"1"` || time.Since(start) < 300*time.Millisecond {
		t.Fatalf("no publish: %d after %s", rec.Code, time.Since(start))
	}
	if rec := a.spec.Serve(t, a.h, cond(0)); rec.Code != 304 {
		t.Fatalf("wait=0 on the current version: %d", rec.Code)
	}

	type answer struct {
		code    int
		version float64
		at      time.Time
	}
	done := make(chan answer, 1)
	go func() {
		rec := httptest.NewRecorder()
		a.h.ServeHTTP(rec, cond(20000))
		var b map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &b)
		v, _ := b["version"].(float64)
		done <- answer{rec.Code, v, time.Now()}
	}()
	time.Sleep(500 * time.Millisecond) // the poll is parked
	if _, err := a.pub.PublishVersion(t.Context(), "int_evt_brandA", []byte(brandASpec(100)), true, "priya@example.com", "long-poll"); err != nil {
		t.Fatal(err)
	}
	published := time.Now()
	select {
	case got := <-done:
		if got.code != 200 || got.version != 2 {
			t.Fatalf("long poll: %d v%v", got.code, got.version)
		}
		if lag := got.at.Sub(published); lag > time.Second {
			t.Fatalf("long poll answered %s after the publish, want ≤ 1 s", lag)
		} else {
			t.Logf("long poll answered %s after the publish", lag.Round(time.Millisecond))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("long poll did not answer within 5 s of the publish")
	}
	req := a.request(RoleViewer, http.MethodGet, "/snapshots/latest?wait=40000", "")
	if rec := a.spec.ServeInvalid(t, a.h, req); rec.Code != 400 {
		t.Fatalf("wait out of range: %d", rec.Code)
	}
}
