package registry

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// copyDevConfig copies backend/config into a temp dir the test may edit.
func copyDevConfig(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir("../../config", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("../../config", p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func editFile(t *testing.T, path, old, repl string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("%s has no %q", path, old)
	}
	writeAtomic(t, path, []byte(strings.Replace(string(b), old, repl, 1)))
}

// writeAtomic replaces path by renaming a fully written temp file over it, as editors do. A plain
// os.WriteFile truncates first, so the 50 ms watcher could read the empty file and report "EOF"
// instead of the edit under test (HUB-239).
func writeAtomic(t *testing.T, path string, b []byte) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), filepath.Base(path)) // outside the watched tree, same filesystem
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func TestWatchSwapsValidEditsAndKeepsLastGoodOnInvalid(t *testing.T) {
	dir := copyDevConfig(t)
	src := Sources{Config: os.DirFS(dir), Contracts: os.DirFS("../../../contracts")}
	var store Store
	first, err := Build(src, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store.Swap(first)

	swapped := make(chan *Snapshot, 4)
	failed := make(chan error, 4)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, src, &store, WatchOptions{Interval: 50 * time.Millisecond,
			OnSwap: func(s *Snapshot) { swapped <- s }, OnError: func(err error) { failed <- err }})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Watch: %v", err)
		}
	})
	account := filepath.Join(dir, "accounts", "ct-brandA-in.yaml")

	// 1. A valid edit swaps within 2 s.
	start := time.Now()
	editFile(t, account, "rps: 50", "rps: 75")
	select {
	case s := <-swapped:
		if took := time.Since(start); took > 2*time.Second {
			t.Fatalf("swap took %s, want < 2s", took)
		}
		if s.Version != 2 || s.Accounts["ct-brandA-in"].Quota.RPS != 75 || store.Snapshot() != s && store.Snapshot().Version != 2 {
			t.Fatalf("swapped snapshot v%d rps %d", s.Version, s.Accounts["ct-brandA-in"].Quota.RPS)
		}
	case err := <-failed:
		t.Fatalf("valid edit failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("no swap after a valid edit")
	}

	// 2. An invalid edit is rejected and the previous snapshot keeps serving.
	editFile(t, account, "vendor: clevertap", "vendor: nobody")
	select {
	case err := <-failed:
		if !strings.Contains(err.Error(), `accounts/ct-brandA-in.yaml: vendor: unknown vendor "nobody"`) {
			t.Fatalf("error %v", err)
		}
	case s := <-swapped:
		t.Fatalf("invalid config was swapped in (v%d)", s.Version)
	case <-time.After(3 * time.Second):
		t.Fatal("invalid edit not reported")
	}
	if cur := store.Snapshot(); cur.Version != 2 || cur.Accounts["ct-brandA-in"].Vendor != "clevertap" {
		t.Fatalf("last good snapshot not kept: v%d", cur.Version)
	}

	// 3. Fixing it with new content swaps again. (Restoring exactly the content already served
	// would not swap: the checksum is unchanged.)
	b, _ := os.ReadFile(account)
	fixed := strings.NewReplacer("vendor: nobody", "vendor: clevertap", "rps: 75", "rps: 80").Replace(string(b))
	writeAtomic(t, account, []byte(fixed))
	select {
	case s := <-swapped:
		if s.Version != 3 || s.Accounts["ct-brandA-in"].Quota.RPS != 80 {
			t.Fatalf("version %d rps %d, want 3 / 80", s.Version, s.Accounts["ct-brandA-in"].Quota.RPS)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no swap after fixing the config")
	}
}
