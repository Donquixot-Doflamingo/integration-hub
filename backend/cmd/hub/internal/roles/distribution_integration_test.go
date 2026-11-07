//go:build integration

package roles

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// snapshotVersion reads the node's snapshot_version gauge and its served integration version.
func servedVersion(t *testing.T, ops string) int {
	t.Helper()
	_, metrics := get(t, "http://"+ops+"/metrics", nil)
	for _, line := range strings.Split(metrics, "\n") {
		if strings.HasPrefix(line, "snapshot_version ") {
			v, _ := strconv.ParseFloat(strings.TrimPrefix(line, "snapshot_version "), 64)
			return int(v)
		}
	}
	return -1
}

// TestPublishReachesThreeNodes is HUB-171: a publish reaches 3 gateway nodes within 10 s, each
// exports snapshot_version, and a node keeps serving its last good snapshot while the archive
// cannot be read.
func TestPublishReachesThreeNodes(t *testing.T) {
	pool := freshArchive(t)
	blobURL := "file://" + t.TempDir()
	rdb := redis.NewClient(&redis.Options{Addr: os.Getenv("HUB_REDIS_ADDR")})
	t.Cleanup(func() { _ = rdb.Close() })

	ops := make([]string, 3)
	for i := range ops {
		cfg := composeConfig(t, Gateway)
		cfg.BlobURL = blobURL
		ops[i] = startRoleWith(t, cfg, Options{})["ops"]
	}
	for _, o := range ops {
		if v := servedVersion(t, o); v < 0 { // 0: the dev config, not archived yet (HUB-401)
			t.Fatalf("node %s does not export snapshot_version", o)
		}
	}
	before := make([]int, 3)
	for i, o := range ops {
		before[i] = servedVersion(t, o)
	}

	blobs, _ := control.OpenBlobStore(blobURL)
	pub := &control.Publisher{Pool: pool, Archive: control.NewArchive(pool, blobs),
		Bootstrap: registry.Sources{Config: os.DirFS("../../../../config"), Contracts: os.DirFS("../../../../../contracts")},
		Notify:    func(ctx context.Context, v int64) { rdb.Publish(ctx, control.SnapshotChannel, v) }}
	start := time.Now()
	res, err := pub.PublishVersion(t.Context(), "int_evt_brandA", []byte(`
targets:
  - { vendor: clevertap, account: ct-brandA-in, transformer: { request: T1@7, response: R2@1 }, weight: 100 }
delivery: { mode: wait, wait_ms: 3000, ttl: 6h, on_circuit_open: fail_fast, pending: re_resolve, polling: true }
audit: { payloads: masked, retention: 30d }
`), true, "release@example.com", "distribution test")
	if err != nil {
		t.Fatal(err)
	}
	for i, o := range ops {
		deadline := time.Now().Add(10 * time.Second)
		for servedVersion(t, o) == before[i] {
			if time.Now().After(deadline) {
				t.Fatalf("node %d still on its old snapshot 10 s after publish %d", i, res.ArchiveVersion)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	t.Logf("3 nodes on the published snapshot in %s", time.Since(start).Round(time.Millisecond))
	for i, o := range ops { // the gauge is the archive version, the same on every node (HUB-401)
		if v := servedVersion(t, o); v != int(res.ArchiveVersion) {
			t.Fatalf("node %d reports snapshot_version %d, published archive version %d", i, v, res.ArchiveVersion)
		}
	}

	// The archive becomes unreadable (blob gone): nodes keep their last good snapshot.
	served := servedVersion(t, ops[0])
	if _, err := pool.Exec(t.Context(), `INSERT INTO hub.config_snapshots (version, checksum, blob_ref, author, reason)
		VALUES ($1, 'sha256:' || repeat('0', 64), 'file:///nonexistent/broken.json.gz', 'test', 'unreadable archive')`, res.ArchiveVersion+1); err != nil {
		t.Fatal(err)
	}
	rdb.Publish(t.Context(), control.SnapshotChannel, res.ArchiveVersion+1)
	time.Sleep(time.Second)
	code, body := get(t, "http://"+ops[0]+"/readyz", nil)
	if servedVersion(t, ops[0]) != served || code != 200 {
		t.Fatalf("node lost its snapshot when the archive broke: version %d → %d, readyz %d %s", served, servedVersion(t, ops[0]), code, body)
	}
}

// TestRestartedNodeReportsTheArchiveVersion is HUB-401: snapshot_version is the archive
// version, so a node started after a publish reports what its peers report, both when it
// adopts the publish from the archive and when its config directory builds the very content
// that is archived. With the old per-process counter the late node reported 1 and
// SnapshotBehind fired for good.
func TestRestartedNodeReportsTheArchiveVersion(t *testing.T) {
	pool := freshArchive(t)
	blobURL := "file://" + t.TempDir()
	rdb := redis.NewClient(&redis.Options{Addr: os.Getenv("HUB_REDIS_ADDR")})
	t.Cleanup(func() { _ = rdb.Close() })
	waitFor := func(ops string, want int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for servedVersion(t, ops) != want {
			if time.Now().After(deadline) {
				t.Fatalf("node %s reports snapshot_version %d, want %d", ops, servedVersion(t, ops), want)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// The admin role archives the dev config as version 1 and reports 1; a gateway that builds
	// the same content from its own config directory reports 1 too.
	admin := composeConfig(t, Admin)
	admin.BlobURL = blobURL
	adminOps := startRoleWith(t, admin, Options{})["ops"]
	waitFor(adminOps, 1)
	gw := composeConfig(t, Gateway)
	gw.BlobURL = blobURL
	first := startRoleWith(t, gw, Options{})["ops"]
	waitFor(first, 1)

	// Two publishes (versions 2 and 3): every node follows. Then a gateway starts ("restarted
	// after the publishes") from the same config directory and must report the same version.
	// The old counter gave the long-running nodes 3 (three swaps) but the late node 2.
	blobs, _ := control.OpenBlobStore(blobURL)
	pub := &control.Publisher{Pool: pool, Archive: control.NewArchive(pool, blobs),
		Bootstrap: registry.Sources{Config: os.DirFS("../../../../config"), Contracts: os.DirFS("../../../../../contracts")},
		Notify:    func(ctx context.Context, v int64) { rdb.Publish(ctx, control.SnapshotChannel, v) }}
	want := 0
	for _, waitMS := range []string{"2500", "2000"} {
		res, err := pub.PublishVersion(t.Context(), "int_evt_brandA", []byte(`
targets:
  - { vendor: clevertap, account: ct-brandA-in, transformer: { request: T1@7, response: R2@1 }, weight: 100 }
delivery: { mode: wait, wait_ms: `+waitMS+`, ttl: 6h, on_circuit_open: fail_fast, pending: re_resolve, polling: true }
audit: { payloads: masked, retention: 30d }
`), true, "release@example.com", "restart test")
		if err != nil {
			t.Fatal(err)
		}
		want = int(res.ArchiveVersion)
		waitFor(first, want)
		waitFor(adminOps, want)
	}
	if want != 3 {
		t.Fatalf("archive version %d after two publishes, want 3", want)
	}
	late := startRoleWith(t, gw, Options{})["ops"]
	waitFor(late, want)
	if a, b := servedVersion(t, first), servedVersion(t, late); a != b {
		t.Fatalf("restarted node %d, peer %d", b, a)
	}
}
