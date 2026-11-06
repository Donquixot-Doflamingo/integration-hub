// Package multiop is a test-only registry overlay (HUB-343): a non-interactive capability
// test.two_step@v1 with two operations, send and verify, each on its own endpoint of the
// fictional vendor stepvendor, and the integration int_two_step whose targets (primary sv-primary,
// failover sv-backup) give verify its own transformer pair under operations. Every transformer
// has golden fixtures; the registry runs them when the overlay is built.
//
// The overlay only adds objects: it relies on the tenant orders-service (backend/config and
// the registry test fixtures both have it). Tests merge Config over their registry directory
// and Contracts over their contracts directory.
package multiop

import (
	"embed"
	"io/fs"
)

//go:embed all:fixture
var files embed.FS

// Integration, Capability and the operation names of the overlay.
const (
	Integration = "int_two_step"
	Capability  = "test.two_step@v1"
	OpSend      = "send"
	OpVerify    = "verify"
	// VersionFile is the overlay's only integration version file.
	VersionFile = "integrations/int_two_step/versions/1.yaml"
)

// Config is the registry overlay (capabilities, vendors, accounts, transformers with fixtures,
// integrations), rooted like backend/config.
func Config() fs.FS { return sub("fixture/config") }

// Contracts holds the capability schemas, rooted like contracts/.
func Contracts() fs.FS { return sub("fixture/contracts") }

// Files returns every file of fsys as path → content (for tests that edit the overlay).
func Files(fsys fs.FS) map[string]string {
	out := map[string]string{}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		out[p] = string(b)
		return err
	})
	return out
}

func sub(dir string) fs.FS {
	f, err := fs.Sub(files, dir)
	if err != nil {
		panic(err) // the directory is embedded: cannot happen
	}
	return f
}
