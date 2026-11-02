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
		if v := servedVersion(t, o); v < 1 {
			t.Fatalf("node %s exports snapshot_version %d", o, v)
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
