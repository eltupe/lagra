package reload

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func start(t *testing.T, path string) (*int32, func()) {
	t.Helper()
	var n int32
	w := &Watcher{Path: path, Debounce: 40 * time.Millisecond, PollInterval: time.Hour, OnChange: func() { atomic.AddInt32(&n, 1) }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond) // let the watcher settle
	return &n, func() { cancel(); <-done }
}

func waitCount(t *testing.T, n *int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(n) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("change count = %d, want %d", atomic.LoadInt32(n), want)
}

func atomicWrite(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp-editor"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func TestAtomicSaveAndDebounce(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	atomicWrite(t, p, "a = 1\n")
	n, stop := start(t, p)
	defer stop()
	// several quick saves collapse into one change
	atomicWrite(t, p, "a = 2\n")
	atomicWrite(t, p, "a = 3\n")
	atomicWrite(t, p, "a = 4\n")
	waitCount(t, n, 1)
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(n) != 1 {
		t.Errorf("debounce failed: %d", *n)
	}
	// same content again is not a change
	atomicWrite(t, p, "a = 4\n")
	time.Sleep(120 * time.Millisecond)
	if atomic.LoadInt32(n) != 1 {
		t.Errorf("identical content must not trigger: %d", *n)
	}
}

func TestSymlinkTargetEditedInOtherDir(t *testing.T) {
	repo := t.TempDir()
	cfgDir := t.TempDir()
	target := filepath.Join(repo, "config.toml")
	atomicWrite(t, target, "a = 1\n")
	link := filepath.Join(cfgDir, "config.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	n, stop := start(t, link)
	defer stop()
	atomicWrite(t, target, "a = 2\n") // editor saves in the dotfiles repo
	waitCount(t, n, 1)
}

func TestSymlinkRepointed(t *testing.T) {
	repo := t.TempDir()
	cfgDir := t.TempDir()
	v1 := filepath.Join(repo, "v1.toml")
	v2 := filepath.Join(repo, "v2.toml")
	atomicWrite(t, v1, "a = 1\n")
	atomicWrite(t, v2, "a = 2\n")
	link := filepath.Join(cfgDir, "config.toml")
	_ = os.Symlink(v1, link)
	n, stop := start(t, link)
	defer stop()
	// atomically swap the symlink (ln -sfn semantics)
	tmp := link + ".tmp"
	_ = os.Symlink(v2, tmp)
	if err := os.Rename(tmp, link); err != nil {
		t.Fatal(err)
	}
	waitCount(t, n, 1)
}

func TestDeleteAndRecreate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	atomicWrite(t, p, "a = 1\n")
	n, stop := start(t, p)
	defer stop()
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	waitCount(t, n, 1) // gone counts as a change (validation will fail and keep the old config)
	atomicWrite(t, p, "a = 1\n")
	waitCount(t, n, 2)
}

func TestPollFallback(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	atomicWrite(t, p, "a = 1\n")
	var n int32
	w := &Watcher{Path: p, Debounce: time.Hour, PollInterval: 30 * time.Millisecond, OnChange: func() { atomic.AddInt32(&n, 1) }}
	ctx := t.Context()
	go func() { _ = w.Run(ctx) }()
	time.Sleep(40 * time.Millisecond)
	// write in place (not via rename) then rely on polling since debounce is huge
	if err := os.WriteFile(p, []byte("a = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitCount(t, &n, 1)
}

func TestUnrelatedFilesIgnored(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	atomicWrite(t, p, "a = 1\n")
	n, stop := start(t, p)
	defer stop()
	atomicWrite(t, filepath.Join(dir, "other.toml"), "x = 1\n")
	time.Sleep(120 * time.Millisecond)
	if atomic.LoadInt32(n) != 0 {
		t.Errorf("unrelated file triggered %d changes", *n)
	}
}
