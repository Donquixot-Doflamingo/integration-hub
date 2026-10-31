package edge

import (
	"os"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// devKey is the documented test-only dev key of orders-service (backend/config/README.md).
const devKey = "hk_test_dev_orders_service_local_only"

type staticSnapshots struct{ s *registry.Snapshot }

func (s staticSnapshots) Snapshot() *registry.Snapshot { return s.s }

// devSnapshot builds the dev registry (backend/config + contracts).
func devSnapshot(t testing.TB) *registry.Snapshot {
	t.Helper()
	snap, err := registry.Build(registry.Sources{Config: os.DirFS("../../config"), Contracts: os.DirFS("../../../contracts")}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var st registry.Store
	return st.Swap(snap)
}
