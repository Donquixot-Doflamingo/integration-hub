package roles

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// devAPIKey is the dev tenant's test key (config/README.md; local only).
const devAPIKey = "hk_test_dev_orders_service_local_only"

// HUB-234 secret grep (SECURITY.md §9, TESTING_STRATEGY §5): every role this test binary starts
// through startRoleWith logs through the production logger (JSON, value redaction, debug level)
// into roleLogs. Tests declare the secrets they hand to the roles with forbid; after the run,
// TestMain fails the package if any declared value appears anywhere in the captured logs.

// logSink is a goroutine-safe, append-only log capture.
type logSink struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *logSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var (
	roleLogs logSink

	forbiddenMu sync.Mutex
	forbidden   = map[string]string{} // secret value → label (labels are printed, values never)
)

// forbid declares secret values that must never appear in any role's logs.
func forbid(label string, values ...string) {
	forbiddenMu.Lock()
	defer forbiddenMu.Unlock()
	for _, v := range values {
		if v != "" {
			forbidden[v] = label
		}
	}
}

// leak is one forbidden value found in the logs.
type leak struct {
	label string
	line  string // the log line with every declared value replaced by its label
}

// scanLogs returns every forbidden value that occurs in logs, one entry per (label, line).
func scanLogs(logs string) []leak {
	forbiddenMu.Lock()
	defer forbiddenMu.Unlock()
	pairs := make([]string, 0, 2*len(forbidden))
	for v, label := range forbidden {
		pairs = append(pairs, v, "<"+label+">")
	}
	mask := strings.NewReplacer(pairs...) // a reported line never shows any declared value
	var out []leak
	for _, line := range strings.Split(logs, "\n") {
		for v, label := range forbidden {
			if strings.Contains(line, v) {
				out = append(out, leak{label: label, line: mask.Replace(line)})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].label < out[j].label })
	return out
}

// newRoleLogger is the logger startRoleWith hands to Run: the production NewLogger over
// roleLogs, at debug level so the scan sees every line a role can produce.
func newRoleLogger(role Role, secrets *telemetry.SecretSet) *slog.Logger {
	return telemetry.NewLogger(&roleLogs, slog.LevelDebug, secrets, string(role))
}

func TestMain(m *testing.M) {
	forbid("dev tenant API key", devAPIKey)
	code := m.Run()
	if leaks := scanLogs(roleLogs.String()); len(leaks) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: secret grep: %d log line(s) contain a secret (HUB-234):\n", len(leaks))
		for _, l := range leaks {
			fmt.Fprintf(os.Stderr, "  %s: %.400s\n", l.label, l.line)
		}
		code = 1
	}
	os.Exit(code)
}

// TestScanLogsFindsDeclaredSecrets: the scan reports a declared value wherever it sits in a
// line, never prints it, and ignores undeclared text.
func TestScanLogsFindsDeclaredSecrets(t *testing.T) {
	const v = "scan-self-test-secret-value"
	forbid("self-test", v)
	t.Cleanup(func() {
		forbiddenMu.Lock()
		delete(forbidden, v)
		forbiddenMu.Unlock()
	})
	leaks := scanLogs(`{"msg":"ok"}` + "\n" + `{"error":"dial: prefix` + v + `suffix"}`)
	if len(leaks) != 1 || leaks[0].label != "self-test" || strings.Contains(leaks[0].line, v) || !strings.Contains(leaks[0].line, "<self-test>") {
		t.Fatalf("leaks %+v", leaks)
	}
}
