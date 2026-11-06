//go:build integration

package roles

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
)

func decrypts(t *testing.T, ops string) int {
	t.Helper()
	_, metrics := get(t, "http://"+ops+"/metrics", nil)
	for _, line := range strings.Split(metrics, "\n") {
		if v, ok := strings.CutPrefix(line, "creds_decrypts_total "); ok {
			n, _ := strconv.ParseFloat(v, 64)
			return int(n)
		}
	}
	return -1
}

// TestPromotionReachesThreeNodes is HUB-177: a promoted credential reloads the credential cache
// of every node within 10 s of the rotator's hub:creds ping.
func TestPromotionReachesThreeNodes(t *testing.T) {
	pool := freshArchive(t)
	dir := t.TempDir()
	keyFile, secretsFile := filepath.Join(dir, "dev.key"), filepath.Join(dir, "secrets.enc")
	key, _ := creds.GenerateKey()
	if err := creds.WriteKeyFile(keyFile, key, false); err != nil {
		t.Fatal(err)
	}
	store, _ := creds.NewLocalFile(secretsFile, key)
	if err := store.WriteAll(map[string]creds.AccountSecrets{
		"ct-brandA-in": {Active: creds.Slot{Version: 1, Values: map[string]string{"account_id": "fake-acct-ROT1", "passcode": "fake-pass-ROT1"}}},
	}); err != nil {
		t.Fatal(err)
	}
	forbid("vendor credential", "fake-acct-ROT1", "fake-pass-ROT1", "fake-acct-ROT2", "fake-pass-ROT2") // secret grep (logscan_test.go)
	rdb := redis.NewClient(&redis.Options{Addr: os.Getenv("HUB_REDIS_ADDR")})
	t.Cleanup(func() { _ = rdb.Close() })

	ops := make([]string, 3)
	before := make([]int, 3)
	for i := range ops {
		cfg := composeConfig(t, Gateway)
		cfg.CredSource, cfg.DevKeyFile = "localfile://"+secretsFile, keyFile
		ops[i] = startRoleWith(t, cfg, Options{})["ops"]
		before[i] = decrypts(t, ops[i])
	}
	time.Sleep(200 * time.Millisecond) // let the subscriptions register

	r := &control.Rotator{Pool: pool, Store: store, Notify: func(ctx context.Context, a string) { rdb.Publish(ctx, control.CredentialsChannel, a) }}
	// waitReload waits until every node has decrypted again since before, then moves before on.
	waitReload := func(step string) {
		for i, o := range ops {
			deadline := time.Now().Add(10 * time.Second)
			for decrypts(t, o) <= before[i] {
				if time.Now().After(deadline) {
					t.Fatalf("node %d did not reload credentials within 10 s of %s", i, step)
				}
				time.Sleep(50 * time.Millisecond)
			}
			before[i] = decrypts(t, o)
		}
	}
	if _, err := r.AddNext(t.Context(), "ct-brandA-in", map[string]string{"account_id": "fake-acct-ROT2", "passcode": "fake-pass-ROT2"}, nil, "release@example.com", "rotation test"); err != nil {
		t.Fatal(err)
	}
	waitReload("add next")
	if _, err := r.Promote(t.Context(), "ct-brandA-in", 2, "release@example.com", "rotation test"); err != nil {
		t.Fatal(err)
	}
	waitReload("promote")
}
