//go:build integration

package authz

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/opa"
)

// A small policy in the shape of entry.rego: allow when the subject holds the permission.
const tinyPolicy = `package integrationhub

import rego.v1

default allow := false

allow if "HUB_VENDOR:READ" in input.sub.permissions

decision := {"allow": allow, "method": input.http.method, "path": input.http.path, "meta": {}}
`

// TestDecideAgainstRealOPA proves the request and answer shapes with a real OPA container.
func TestDecideAgainstRealOPA(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.rego"), []byte(tinyPolicy), 0o644); err != nil { //nolint:gosec // read by the container user
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // the container user must read it
		t.Fatal(err)
	}
	c, err := New(Config{URL: opa.Start(t, dir)})
	if err != nil {
		t.Fatal(err)
	}
	in := sampleInput()
	if d, err := c.Decide(t.Context(), in); err != nil || !d.Allow {
		t.Fatalf("allow: %+v %v", d, err)
	}
	in.Sub = map[string]any{"id": "u-2", "type": "USER", "permissions": []any{}}
	if d, err := c.Decide(t.Context(), in); err != nil || d.Allow {
		t.Fatalf("deny: %+v %v", d, err)
	}
	other, _ := New(Config{URL: c.endpoint[:len(c.endpoint)-len("/v1/data/integrationhub/decision")], Package: "nothingloaded"})
	if _, err := other.Decide(t.Context(), in); err == nil {
		t.Fatal("an unloaded package must be unavailable, not a deny")
	}
}
