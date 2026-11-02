package control

import (
	"io/fs"
	"testing/fstest"
)

// Capability schemas are read from the contracts side of the sources (registry.Sources.Contracts),
// not from the config tree. An admin edit that creates a capability or a new minor writes its
// schema files there: the edit's contracts become an overlay of the written files over the
// previous contracts, and the archive packs the schema files the new snapshot uses
// (registry.Pack), so they travel with the snapshot like every config file.

// GetContract returns a file of the edit's contracts side (capability schemas).
func (t Tree) GetContract(path string) ([]byte, bool) {
	if t.t.contracts == nil {
		return nil, false
	}
	b, err := fs.ReadFile(t.t.contracts, path)
	return b, err == nil
}

// PutContract creates or replaces a file of the edit's contracts side (capability schemas).
func (t Tree) PutContract(path string, b []byte) {
	o, ok := t.t.contracts.(*overlayFS)
	if !ok {
		o = &overlayFS{over: fstest.MapFS{}, base: t.t.contracts}
		t.t.contracts = o
	}
	o.over[path] = &fstest.MapFile{Data: b}
}

// overlayFS serves written files first, then the base file system.
type overlayFS struct {
	over fstest.MapFS
	base fs.FS
}

func (o *overlayFS) Open(name string) (fs.File, error) {
	if _, ok := o.over[name]; ok {
		return o.over.Open(name)
	}
	if o.base == nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return o.base.Open(name)
}
