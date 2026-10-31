package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestRunUsageAndUnknownRole(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no args", nil, "usage: hub <role>"},
		{"unknown role", []string{"proxy"}, `unknown role "proxy"`},
		{"bad config", []string{"gateway"}, "HUB_PG_DSN is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := run(t.Context(), tt.args, func(string) string { return "" }, &bytes.Buffer{}, &stderr)
			if code == 0 {
				t.Fatalf("exit code 0, want non-zero")
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("stderr %q does not contain %q", stderr.String(), tt.want)
			}
		})
	}
}

// TestMain lets the test binary act as the hub binary for the SIGTERM test.
func TestMain(m *testing.M) {
	if os.Getenv("HUB_TEST_EXEC_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

func TestSIGTERMStopsRoleWithinBudget(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0], "worker", "-ops-addr=127.0.0.1:0", "-ready-drain=100ms")
	cmd.Env = append(os.Environ(), "HUB_TEST_EXEC_MAIN=1", "HUB_PG_DSN=postgres://hub:dsn-password-canary@localhost/hub")
	var stderr syncBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(stderr.String(), "role started") {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("worker did not start: %s", stderr.String())
		}
		timer := time.NewTimer(20 * time.Millisecond)
		<-timer.C
	}
	sent := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		if err != nil {
			t.Fatalf("exit after SIGTERM: %v\n%s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("worker did not exit within 10s of SIGTERM")
	}
	if d := time.Since(sent); d > 10*time.Second {
		t.Fatalf("stopped after %s", d)
	}
	if strings.Contains(stderr.String(), "dsn-password-canary") {
		t.Fatalf("DSN password leaked into logs: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "role stopped") {
		t.Fatalf("missing 'role stopped' log: %s", stderr.String())
	}
}

// syncBuffer is a bytes.Buffer safe for one writer (the child's stderr copier) and concurrent readers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
