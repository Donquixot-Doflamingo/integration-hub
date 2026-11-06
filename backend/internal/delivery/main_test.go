package delivery

import (
	"testing"

	"go.uber.org/goleak"
)

// The worker, scheduler and shadower start goroutines: none may outlive a test (go-testing).
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
