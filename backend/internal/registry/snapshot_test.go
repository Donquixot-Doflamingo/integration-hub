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

// TestStoreCarriesTheArchiveVersion is HUB-401: the store never counts versions itself, so two
// processes serving the same publish report the same version however often each swapped.
func TestStoreCarriesTheArchiveVersion(t *testing.T) {
	var st Store
	if st.Snapshot() != nil {
		t.Fatal("empty store returned a snapshot")
	}
	base := build(t, validFiles)
	var restarted Store // a node started later: one swap only
	for range 3 {
		if got := st.Swap(base).Version; got != 0 {
			t.Fatalf("unarchived content has version %d, want 0", got)
		}
	}
	steps := []struct {
		name     string
		apply    func() (*Snapshot, bool)
		want     uint64
		changed  bool
		checksum string
	}{
		{"archived as 7", func() (*Snapshot, bool) { return st.Label(base.Checksum, 7) }, 7, true, base.Checksum},
		{"same label again", func() (*Snapshot, bool) { return st.Label(base.Checksum, 7) }, 7, false, base.Checksum},
		{"other content's label ignored", func() (*Snapshot, bool) { return st.Label("sha256:other", 9) }, 7, false, base.Checksum},
		{"republished as 8 (rollback to the same content)", func() (*Snapshot, bool) { return st.Label(base.Checksum, 8) }, 8, true, base.Checksum},
	}
	for _, s := range steps {
		got, changed := s.apply()
		if got.Version != s.want || changed != s.changed || st.Snapshot().Version != s.want {
			t.Fatalf("%s: version %d changed %v, want %d %v", s.name, got.Version, changed, s.want, s.changed)
		}
	}
	if v := restarted.SwapVersion(base, 8).Version; v != st.Snapshot().Version {
		t.Fatalf("restarted node reports %d, peer %d", v, st.Snapshot().Version)
	}
	if base.Version != 0 {
		t.Fatal("Swap or SwapVersion modified the caller's snapshot")
	}
	var empty Store
	if got, changed := empty.Label(base.Checksum, 1); got != nil || changed {
		t.Fatal("Label on an empty store published something")
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

func TestBuildCompilesTransformersOnceAndShares(t *testing.T) {
	s := build(t, validFiles)
	p1 := s.Programs[TransformerRef{"T1", 7}]
	if p1 == nil || s.Programs[TransformerRef{"R2", 1}] == nil {
		t.Fatalf("programs %v", s.Programs)
	}
	if p1.Ref() != "T1@7" || p1.Kind() != "request" {
		t.Fatalf("program %s %s", p1.Ref(), p1.Kind())
	}
}

func TestBuildRefusesFailingGoldensAndTypeErrors(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]string
		want      string
	}{
		{"failing golden", map[string]string{"transformers/T1/fixtures/basic.expected_request.json": `{"method": "PUT", "path": "/1/upload"}`},
			`transformers/T1/fixtures/basic: T1@7: golden mismatch: /method: expected "PUT", got "POST"`},
		{"missing goldens", map[string]string{"transformers/R2/fixtures/ok.vendor_response.json": "", "transformers/R2/fixtures/ok.expected_response.json": ""},
			"transformers/R2/fixtures: R2@1: no golden fixtures"},
		{"type error", map[string]string{"transformers/T1/7.yaml": validFiles["transformers/T1/7.yaml"] + "  body:\n    id: 'req.user.id +'\n"},
			`transformers/T1/7.yaml: request.body.id: syntax`},
		// ADR-0023: the fields a transformer reads are checked against each operation using it.
		{"undeclared field", map[string]string{
			"transformers/T1/7.yaml":                               validFiles["transformers/T1/7.yaml"] + "  body:\n    id: req.user.idd\n",
			"transformers/T1/fixtures/basic.expected_request.json": `{"method": "POST", "path": "/1/upload", "body": {}}`},
			`integrations/int_evt_brandA/versions/1.yaml: targets[0].transformer.request: T1@7 (request.body.id) reads req.user.idd, which operation default's request_schema.body does not declare`},
		{"undeclared header", map[string]string{
			"transformers/T1/7.yaml":                               validFiles["transformers/T1/7.yaml"] + "  headers:\n    X-Source: hdr[\"X-Source\"] ?? \"none\"\n",
			"transformers/T1/fixtures/basic.expected_request.json": `{"method": "POST", "path": "/1/upload", "headers": {"X-Source": "none"}}`},
			`reads hdr.X-Source, which operation default's request_schema.headers does not declare`},
		{"sample breaks the endpoint schema", map[string]string{
			"vendors/clevertap.yaml": strings.Replace(validFiles["vendors/clevertap.yaml"], "request_schema: {}", "request_schema: { query: { type: object, required: [name] } }", 1)},
			`transformers/T1/fixtures/basic: vendor_endpoint: T1@7: sample basic: the produced request breaks clevertap.upload@1's request_schema at /query/name: missing property 'name'`},
		{"sample input does not fit the operation", map[string]string{
			"transformers/T1/fixtures/basic.input.json": `{"user": {"id": "u_1"}}`},
			`sample basic of T1@7 does not fit operation default's request_schema at /body/event`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Build(sources(t, files(tt.overrides)), time.Now())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err %v\nwant %q", err, tt.want)
			}
		})
	}
}
