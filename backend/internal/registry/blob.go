package registry

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing/fstest"
	"time"
)

// blobFormat versions the snapshot blob layout. Format 1 also packed capability schemas (the
// contracts side) and holds a legacy tree: Unpack upgrades it (UpgradeLegacy, ADR-0023).
const (
	blobFormat       = 2
	legacyBlobFormat = 1
)

// maxBlobBytes bounds an unpacked blob (decompression bomb guard).
const maxBlobBytes = 64 << 20

// blob is the snapshot transport format (TECH_SPEC §7): the sources a snapshot was built from,
// so any node can rebuild and verify it. It holds no secrets (only credential refs).
type blob struct {
	Format    int               `json:"format"`
	Checksum  string            `json:"checksum"`
	Config    map[string]string `json:"config"`              // path → file content
	Contracts map[string]string `json:"contracts,omitempty"` // format 1: capability schemas used
}

// Pack serialises the sources of snap (gzip JSON). snap must have been built from src.
func Pack(src Sources, snap *Snapshot) ([]byte, error) {
	b := blob{Format: blobFormat, Checksum: snap.Checksum, Config: map[string]string{}}
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

// ErrCorruptBlob is returned when a blob cannot be decoded or does not rebuild to its checksum.
var ErrCorruptBlob = errors.New("corrupt snapshot blob")

// Unpack rebuilds a snapshot from a blob and verifies it reproduces the checksum it was packed
// with (and want, when non-empty, e.g. the checksum recorded in config_snapshots). A corrupted or
// tampered blob is refused. A legacy blob (format 1, before ADR-0023) is verified against its
// legacy checksum, then upgraded: the snapshot's Checksum is the upgraded content's, and
// Snapshot.Legacy says where it came from (the admin archives the upgraded content once).
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
	if b.Format != blobFormat && b.Format != legacyBlobFormat {
		return nil, Sources{}, fmt.Errorf("%w: unknown format %d", ErrCorruptBlob, b.Format)
	}
	if want != "" && b.Checksum != want {
		return nil, Sources{}, fmt.Errorf("%w: blob is for %s, want %s", ErrCorruptBlob, b.Checksum, want)
	}
	if b.Format == legacyBlobFormat {
		return unpackLegacy(b, now)
	}
	src := Sources{Config: toFS(b.Config)}
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

func unpackLegacy(b blob, now time.Time) (*Snapshot, Sources, error) {
	cfg, lg, err := UpgradeLegacy(toFS(b.Config), toFS(b.Contracts))
	if err != nil {
		return nil, Sources{}, fmt.Errorf("%w: legacy blob: %w", ErrCorruptBlob, err)
	}
	if lg == nil { // a format-1 blob without capabilities: its checksum is over the files alone
		lg = &Legacy{}
		lg.Checksum, _ = checksum(cfg)
	}
	if lg.Checksum != b.Checksum {
		return nil, Sources{}, fmt.Errorf("%w: legacy content hashes to %s, blob says %s", ErrCorruptBlob, lg.Checksum, b.Checksum)
	}
	src := Sources{Config: cfg}
	snap, err := Build(src, now)
	if err != nil {
		return nil, Sources{}, fmt.Errorf("%w: legacy blob does not upgrade: %w", ErrCorruptBlob, err)
	}
	snap.Legacy = lg
	return snap, src, nil
}
