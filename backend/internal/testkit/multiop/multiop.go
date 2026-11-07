// Package multiop is a test-only registry overlay (HUB-343, ADR-0023): the non-interactive
// integration int_two_step with two operations, send and verify, each with its own request
// schema and each on its own endpoint of the fictional vendor stepvendor. Its targets (primary
// sv-primary, failover sv-backup) give verify its own transformer pair under operations. Every
// transformer has golden fixtures; the registry runs them when the overlay is built.
//
// The overlay only adds objects: it relies on the tenant orders-service (backend/config and
// the registry test fixtures both have it). Tests merge Config over their registry directory.
package multiop

import (
	"embed"
	"io/fs"
)

//go:embed all:fixture
var files embed.FS

// Integration and the operation names of the overlay.
const (
	Integration = "int_two_step"
	OpSend      = "send"
	OpVerify    = "verify"
	// VersionFile is the overlay's only integration version file.
	VersionFile = "integrations/int_two_step/versions/1.yaml"
)

// Config is the registry overlay (vendors, accounts, transformers with fixtures,
// integrations), rooted like backend/config.
func Config() fs.FS { return sub("fixture/config") }

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
