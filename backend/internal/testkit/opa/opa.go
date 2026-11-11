// Package opa runs a real OPA server in a throwaway docker container for integration tests of
// the admin authorization (HUB-474, HUB-479). Tests skip when docker is not available.
package opa

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Image is the OPA image the ISFC authz-policies tooling pins (override with OPA_IMAGE).
const Image = "docker.io/openpolicyagent/opa:1.4.2-static"

// Start serves the Rego (and data.json) in dir with `opa run --server` and returns its base URL.
// The container is removed when the test ends. It skips the test when docker is missing.
func Start(t testing.TB, dir string) string {
	t.Helper()
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker not found: skipping the real OPA test")
	}
	image := Image
	if v := os.Getenv("OPA_IMAGE"); v != "" {
		image = v
	}
	dir = shareable(t, dir)
	out, err := exec.CommandContext(t.Context(), docker, "run", //nolint:gosec // test helper: docker is looked up on PATH, args are fixed
		"-d", "--rm", "-p", "127.0.0.1::8181", "-v", dir+":/policy:ro",
		image, "run", "--server", "--addr=0.0.0.0:8181", "/policy").CombinedOutput()
	if err != nil {
		t.Skipf("cannot start the OPA container (%v): %s", err, strings.TrimSpace(string(out)))
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, docker, "rm", "-f", id).Run() //nolint:gosec // test helper, id comes from docker run
	})
	port, err := exec.CommandContext(t.Context(), docker, "port", id, "8181/tcp").Output() //nolint:gosec // test helper, id comes from docker run
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(port)), "\n")
	_, p, ok := strings.Cut(line, ":")
	if !ok {
		t.Fatalf("unexpected docker port output %q", port)
	}
	// "0.0.0.0:49153" or "127.0.0.1:49153": the port is after the last colon.
	if i := strings.LastIndex(line, ":"); i >= 0 {
		p = line[i+1:]
	}
	base := fmt.Sprintf("http://127.0.0.1:%s", p)
	deadline := time.Now().Add(30 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/health", nil) //nolint:gosec // loopback URL of the container this helper started
		if resp, err := http.DefaultClient.Do(req); err == nil {                               //nolint:gosec // loopback URL of the container this helper started
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return base
			}
		}
		if time.Now().After(deadline) {
			logs, _ := exec.CommandContext(t.Context(), docker, "logs", id).CombinedOutput() //nolint:gosec // test helper, id comes from docker run
			t.Fatalf("OPA did not become healthy: %s", logs)
		}
		select {
		case <-t.Context().Done():
			t.Fatal("test ended while waiting for OPA")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// shareable copies dir under the user's home directory and returns the copy. Docker VMs such as
// colima share only $HOME with the host, so a bind mount of t.TempDir() (under /var/folders on
// macOS) would be empty inside the container.
func shareable(t testing.TB, dir string) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		return dir
	}
	dst, err := os.MkdirTemp(home, ".hub-opa-test-")
	if err != nil {
		return dir
	}
	t.Cleanup(func() { _ = os.RemoveAll(dst) })
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755) //nolint:gosec // the container user must read it
		}
		b, err := os.ReadFile(path) //nolint:gosec // test fixture under the caller's own dir
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644) //nolint:gosec // the container user must read it
	})
	if err != nil {
		t.Fatalf("copy policies: %v", err)
	}
	return dst
}
