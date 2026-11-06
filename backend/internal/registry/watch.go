package registry

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io/fs"
	"time"
)

// WatchOptions configures Watch.
type WatchOptions struct {
	// Interval between fingerprint checks; a change is built once it has been stable for one
	// more interval (debounce), so a swap lands within about 2 × Interval of the last write.
	Interval time.Duration
	// BeforeSwap is called with a built snapshot just before it is published (warm caches the
	// new snapshot needs, such as credentials of new accounts).
	BeforeSwap func(*Snapshot)
	// OnSwap is called after a new snapshot is published.
	OnSwap func(*Snapshot)
	// OnError is called when a changed config fails to build. The previous snapshot keeps serving.
	OnError func(error)
	// Now is the clock for BuiltAt (tests); defaults to time.Now.
	Now func() time.Time
}

// Watch polls src for changes and rebuilds and swaps the snapshot in store until ctx ends
// (DESIGN §19 M1 dev file watcher). It is for local development only; production nodes get
// snapshots from the control plane (TECH_SPEC §7). An invalid change never replaces the last
// good snapshot (DESIGN §6.4).
func Watch(ctx context.Context, src Sources, store *Store, opts WatchOptions) error {
	if opts.Interval <= 0 {
		opts.Interval = 300 * time.Millisecond
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	// No baseline fingerprint: the first stable reading is built and compared by checksum with
	// the current snapshot, so an edit made just before Watch started is not missed.
	var last []byte
	t := time.NewTicker(opts.Interval)
	defer t.Stop()
	var pending []byte // fingerprint seen once, waiting to be stable
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		fp, err := fingerprint(src)
		if err != nil {
			if opts.OnError != nil {
				opts.OnError(err)
			}
			continue
		}
		switch {
		case string(fp) == string(last):
			pending = nil
			continue
		case string(fp) != string(pending):
			pending = fp // changed: wait one interval for the writes to settle
			continue
		}
		last, pending = fp, nil
		snap, err := Build(src, opts.Now())
		if err != nil {
			if opts.OnError != nil {
				opts.OnError(err)
			}
			continue
		}
		if cur := store.Snapshot(); cur != nil && cur.Checksum == snap.Checksum {
			continue // touched but identical content
		}
		if opts.BeforeSwap != nil {
			opts.BeforeSwap(snap)
		}
		pub := store.Swap(snap)
		if opts.OnSwap != nil {
			opts.OnSwap(pub)
		}
	}
}

// fingerprint summarises names, sizes and modification times of every file in both trees.
func fingerprint(src Sources) ([]byte, error) {
	h := sha256.New()
	for _, fsys := range []fs.FS{src.Config, src.Contracts} {
		if fsys == nil {
			continue
		}
		err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			h.Write([]byte(p))
			var b [16]byte
			binary.BigEndian.PutUint64(b[:8], uint64(info.Size()))               // #nosec G115 -- size is never negative
			binary.BigEndian.PutUint64(b[8:], uint64(info.ModTime().UnixNano())) // #nosec G115 -- only compared for equality
			h.Write(b[:])
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return h.Sum(nil), nil
}
