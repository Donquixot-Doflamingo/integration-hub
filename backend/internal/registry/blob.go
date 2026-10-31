package registry

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"testing/fstest"
	"time"
)

// blobFormat versions the snapshot blob layout.
const blobFormat = 1

// maxBlobBytes bounds an unpacked blob (decompression bomb guard).
const maxBlobBytes = 64 << 20

// blob is the snapshot transport format (TECH_SPEC §7): the sources a snapshot was built from,
// so any node can rebuild and verify it. It holds no secrets (only credential refs).
type blob struct {
	Format    int               `json:"format"`
	Checksum  string            `json:"checksum"`
	Config    map[string]string `json:"config"`    // path → file content
	Contracts map[string]string `json:"contracts"` // capability schemas used
}

// Pack serialises the sources of snap (gzip JSON). snap must have been built from src.
func Pack(src Sources, snap *Snapshot) ([]byte, error) {
	b := blob{Format: blobFormat, Checksum: snap.Checksum, Config: map[string]string{}, Contracts: map[string]string{}}
	err := fs.WalkDir(src.Config, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(src.Config, p)
		if err != nil {
			return err
		}
		b.Config[p] = string(data)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("pack config: %w", err)
	}
	for _, cfile := range capabilityFiles(snap) {
		data, err := fs.ReadFile(src.Contracts, cfile)
		if err != nil {
			return nil, fmt.Errorf("pack contracts: %w", err)
		}
		b.Contracts[cfile] = string(data)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(b); err != nil {
		return nil, fmt.Errorf("pack: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("pack: %w", err)
	}
	return buf.Bytes(), nil
}

// capabilityFiles lists the contract schema files a snapshot uses, by schema_ref directory.
func capabilityFiles(snap *Snapshot) []string {
	set := map[string]bool{}
	for _, c := range snap.Capabilities {
		dir := c.SchemaDir
		for _, op := range c.Operations {
			set[dir+"/"+op.RequestFile] = true
			set[dir+"/"+op.ResponseFile] = true
		}
		set[c.SchemaRef] = true
	}
	return slices.Sorted(maps.Keys(set))
}

// ErrCorruptBlob is returned when a blob cannot be decoded or does not rebuild to its checksum.
var ErrCorruptBlob = errors.New("corrupt snapshot blob")

// Unpack rebuilds a snapshot from a blob and verifies it reproduces the checksum it was packed
// with (and want, when non-empty, e.g. the checksum recorded in config_snapshots). A corrupted or
// tampered blob is refused.
func Unpack(data []byte, want string, now time.Time) (*Snapshot, Sources, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, Sources{}, fmt.Errorf("%w: %w", ErrCorruptBlob, err)
	}
	raw, err := io.ReadAll(io.LimitReader(zr, maxBlobBytes+1))
	if err != nil || len(raw) > maxBlobBytes {
		return nil, Sources{}, fmt.Errorf("%w: unreadable or larger than %d bytes", ErrCorruptBlob, maxBlobBytes)
	}
	var b blob
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, Sources{}, fmt.Errorf("%w: %w", ErrCorruptBlob, err)
	}
	if b.Format != blobFormat {
		return nil, Sources{}, fmt.Errorf("%w: unknown format %d", ErrCorruptBlob, b.Format)
	}
	if want != "" && b.Checksum != want {
		return nil, Sources{}, fmt.Errorf("%w: blob is for %s, want %s", ErrCorruptBlob, b.Checksum, want)
	}
	src := Sources{Config: toFS(b.Config), Contracts: toFS(b.Contracts)}
	snap, err := Build(src, now)
	if err != nil {
		return nil, Sources{}, fmt.Errorf("%w: %w", ErrCorruptBlob, err)
	}
	if snap.Checksum != b.Checksum {
		return nil, Sources{}, fmt.Errorf("%w: content rebuilds to %s, blob says %s", ErrCorruptBlob, snap.Checksum, b.Checksum)
	}
	return snap, src, nil
}

func toFS(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for p, content := range files {
		if strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
			continue // never resolve outside the blob
		}
		m[p] = &fstest.MapFile{Data: []byte(content)}
	}
	return m
}
