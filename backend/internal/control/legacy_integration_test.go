//go:build integration

package control_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// legacyArchiveBlob packs registry/testdata/legacy (backend/config and the capability schemas as
// they were before ADR-0023) the way the old Pack wrote a format-1 blob.
func legacyArchiveBlob(t *testing.T) ([]byte, string) {
	t.Helper()
	cfg, contracts := os.DirFS("../registry/testdata/legacy/config"), os.DirFS("../registry/testdata/legacy/contracts")
	read := func(root fs.FS) map[string]string {
		out := map[string]string{}
		if err := fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := fs.ReadFile(root, p)
			out[p] = string(b)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	_, lg, err := registry.UpgradeLegacy(cfg, contracts)
	if err != nil || lg == nil {
		t.Fatalf("legacy testdata: %v %+v", err, lg)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(map[string]any{"format": 1, "checksum": lg.Checksum, "config": read(cfg), "contracts": read(contracts)}); err != nil {
		t.Fatal(err)
	}
	_ = zw.Close()
	return buf.Bytes(), lg.Checksum
}

// TestUpgradeLegacyArchive (HUB-434): a database written before ADR-0023 (format-1 snapshot
// blob, drafts without operations, a pending integration naming its capability) comes up on
// the current code: the admin's boot step re-archives the upgraded snapshot and rewrites the
// drafts and the pending header once; a second boot changes nothing.
func TestUpgradeLegacyArchive(t *testing.T) {
	p, pool := publisher(t)
	ctx := t.Context()
	blobs := control.DirBlobStore{Root: t.TempDir()}
	archive := control.NewArchive(pool, blobs)
	data, sum := legacyArchiveBlob(t)
	ref, err := blobs.Put(ctx, "snapshots/1.json.gz", data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO hub.config_snapshots (version, checksum, blob_ref, author, reason) VALUES (1, $1, $2, 'config:dev', 'legacy')`, sum, ref); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM hub.integration_drafts WHERE integration_id IN ('int_evt_brandA', 'int_legacy_pending')`)
		_, _ = pool.Exec(ctx, `DELETE FROM hub.integration_pending WHERE integration_id = 'int_legacy_pending'`)
	}
	cleanup()
	t.Cleanup(cleanup)
	// Shapes the pre-ADR-0023 admin wrote (git 01eb0c5).
	legacyDraft := "integration: \"\"\nversion: 0\ntargets:\n    - vendor: clevertap\n      account: ct-brandA-in\n      transformer:\n        request: T1@7\n        response: R1@2\n      weight: 100\n" +
		"delivery:\n    mode: wait\n    wait_ms: 3000\n    ttl: 6h0m0s\n    on_circuit_open: fail_fast\n    pending: re_resolve\n    polling: true\naudit:\n    payloads: masked\n    retention: 30d\n"
	if _, err := pool.Exec(ctx, `INSERT INTO hub.integration_drafts (integration_id, spec, based_on, author, reason) VALUES
		('int_evt_brandA', $1, 2, 'old@example.com', 'old draft'), ('int_legacy_pending', $1, NULL, 'old@example.com', 'first draft')`, legacyDraft); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO hub.integration_pending (integration_id, header, author) VALUES ('int_legacy_pending',
		$1, 'old@example.com')`, "integration: int_legacy_pending\nowner_tenant: orders-service\ncapability: events.track@v1\ngrants: []\nactive_version: 0\npaused: false\ndescription: old pending\nmoves_money_or_quota: false\n"); err != nil {
		t.Fatal(err)
	}

	v, skipped, err := control.UpgradeLegacyArchive(ctx, pool, archive, time.Now())
	if err != nil || v != 2 || len(skipped) != 0 {
		t.Fatalf("upgrade: version %d, skipped %v, %v", v, skipped, err)
	}
	snap, _, version, err := archive.Latest(ctx, time.Now())
	if err != nil || version != 2 || snap.Legacy != nil {
		t.Fatalf("latest after the upgrade: v%d legacy=%v %v", version, snap != nil && snap.Legacy != nil, err)
	}
	if op := snap.Integrations["int_evt_brandA"].Active().Op("default"); op == nil || op.RequestSchema.Body.Compiled() == nil {
		t.Fatal("the re-archived snapshot has no operations")
	}
	drafts, err := control.ListDrafts(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"int_evt_brandA", "int_legacy_pending"} {
		spec := string(drafts[id].Spec)
		if !strings.Contains(spec, "operations:") || !strings.Contains(spec, "request_schema:") || strings.Contains(spec, "capability") {
			t.Errorf("draft of %s not upgraded:\n%s", id, spec)
		}
	}
	pending, err := control.ListPending(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if h := string(pending["int_legacy_pending"].Header); strings.Contains(h, "capability") || !strings.Contains(h, "owner_tenant: orders-service") {
		t.Errorf("pending header not upgraded:\n%s", h)
	}

	// The upgraded drafts pass the publish validation of the current registry.
	p.Archive = archive
	if err := p.Check(ctx, control.VersionRequest{Integration: "int_evt_brandA", Spec: drafts["int_evt_brandA"].Spec, Activate: true}); err != nil {
		t.Errorf("upgraded draft does not validate: %v", err)
	}
	if err := p.Check(ctx, control.VersionRequest{Integration: "int_legacy_pending", Spec: drafts["int_legacy_pending"].Spec,
		Header: pending["int_legacy_pending"].Header, Activate: true}); err != nil {
		t.Errorf("upgraded pending integration does not validate: %v", err)
	}

	// Second boot: nothing to do.
	if v, _, err := control.UpgradeLegacyArchive(ctx, pool, archive, time.Now()); err != nil || v != 0 {
		t.Fatalf("second upgrade: version %d, %v", v, err)
	}
}
