package registry

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func build(t testing.TB, m map[string]string) *Snapshot {
	t.Helper()
	s, err := Build(sources(t, m), time.Unix(0, 0))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return s
}

func TestBuildIndexesAndChecksum(t *testing.T) {
	a := build(t, validFiles)
	if a.Version != 0 || !strings.HasPrefix(a.Checksum, "sha256:") || len(a.Checksum) != len("sha256:")+64 {
		t.Fatalf("version %d checksum %q", a.Version, a.Checksum)
	}
	in := a.Integrations["int_evt_brandA"]
	if in.Active() == nil || in.Active().Targets[0].Account != "ct-brandA-in" {
		t.Fatal("active version not indexed")
	}
	k, ok := a.KeyByHash("sha256:0000000000000000000000000000000000000000000000000000000000000001")
	if !ok || k.Tenant.Tenant != "orders-service" || k.Key.KeyID != "key_01J9Z3K8X2V7Q4M6N8P0R2S4T6" {
		t.Fatalf("key index %+v %v", k, ok)
	}
	if b := build(t, validFiles); b.Checksum != a.Checksum {
		t.Fatal("same content gave a different checksum")
	}
	changed := files(map[string]string{"accounts/ct-brandA-in.yaml": strings.Replace(validFiles["accounts/ct-brandA-in.yaml"], "rps: 50", "rps: 60", 1)})
	if c := build(t, changed); c.Checksum == a.Checksum {
		t.Fatal("changed content kept the same checksum")
	}
}

func TestBuildRejectsInvalidConfig(t *testing.T) {
	_, err := Build(sources(t, files(map[string]string{"accounts/ct-brandA-in.yaml": ""})), time.Now())
	if err == nil || !strings.Contains(err.Error(), `unknown account "ct-brandA-in"`) {
		t.Fatalf("err %v", err)
	}
}

func TestBuildRejectsDuplicateKeyHash(t *testing.T) {
	_, err := Build(sources(t, files(map[string]string{
		"tenants/billing.yaml": "tenant: billing\napi_keys:\n  - { key_id: k2, hash: sha256:0000000000000000000000000000000000000000000000000000000000000001, scopes: [invoke], state: active }\nrate_limit: { rps: 1, burst: 1 }\n",
	})), time.Now())
	if err == nil || !strings.Contains(err.Error(), "same hash") {
		t.Fatalf("err %v", err)
	}
}

func TestStoreVersionsIncrease(t *testing.T) {
	var st Store
	if st.Snapshot() != nil {
		t.Fatal("empty store returned a snapshot")
	}
	base := build(t, validFiles)
	for want := uint64(1); want <= 3; want++ {
		if got := st.Swap(base).Version; got != want {
			t.Fatalf("version %d, want %d", got, want)
		}
	}
	if base.Version != 0 {
		t.Fatal("Swap modified the caller's snapshot")
	}
}

// TestConcurrentReadersDuringSwaps: readers keep a consistent view of the snapshot they loaded
// while a writer swaps continuously (run with -race).
func TestConcurrentReadersDuringSwaps(t *testing.T) {
	// Two configs that differ in one observable value: the account rps and the checksum.
	snapA := build(t, validFiles)
	snapB := build(t, files(map[string]string{"accounts/ct-brandA-in.yaml": strings.Replace(validFiles["accounts/ct-brandA-in.yaml"], "rps: 50", "rps: 60", 1)}))
	rpsFor := map[string]int{snapA.Checksum: 50, snapB.Checksum: 60}

	var st Store
	st.Swap(snapA)
	stop := make(chan struct{})
	var swaps atomic.Int64
	var writer, readers sync.WaitGroup
	writer.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			st.Swap([]*Snapshot{snapA, snapB}[i%2])
			swaps.Add(1)
		}
	})
	errs := make(chan error, 8)
	for range 8 {
		readers.Go(func() {
			last := uint64(0)
			for range 20_000 {
				s := st.Snapshot()
				if s.Version < last {
					errs <- fmt.Errorf("version went back from %d to %d", last, s.Version)
					return
				}
				last = s.Version
				want := rpsFor[s.Checksum]
				// Read the same snapshot several times: it must never change under us.
				for range 3 {
					if got := s.Accounts["ct-brandA-in"].Quota.RPS; got != want {
						errs <- fmt.Errorf("snapshot %d (%s) shows rps %d, want %d", s.Version, s.Checksum[:15], got, want)
						return
					}
				}
			}
		})
	}
	// The writer swaps for as long as the readers run.
	readers.Wait()
	close(stop)
	writer.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if swaps.Load() < 10 {
		t.Fatalf("only %d swaps happened; the test did not exercise concurrency", swaps.Load())
	}
}
