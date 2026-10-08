package rclone

import (
	"errors"
	"os"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
)

func TestAppleLitter(t *testing.T) {
	yes := []string{"._x", "._", "a/._x.csv", ".DS_Store", "dir/.DS_Store", ".Spotlight-V100/Store", ".Trashes/501/a", "/.fseventsd"}
	no := []string{"x", "a/.hidden", "dir/_file", "a/b._c", ".metadata_never_index", "/", ""}
	for _, p := range yes {
		if !isAppleLitter(p) {
			t.Errorf("%q should be litter", p)
		}
	}
	for _, p := range no {
		if isAppleLitter(p) {
			t.Errorf("%q should not be litter", p)
		}
	}
}

func TestFilteredFS(t *testing.T) {
	mem := memfs.New()
	// pre-existing litter in the backend
	f, _ := mem.Create("dir/._old")
	_ = f.Close()
	f, _ = mem.Create("dir/real.txt")
	_ = f.Close()
	fs := wrapFS(mem, true)

	if _, err := fs.Create("dir/._new"); !errors.Is(err, os.ErrPermission) {
		t.Errorf("create sidecar: %v", err)
	}
	if _, err := fs.OpenFile("dir/.DS_Store", os.O_CREATE|os.O_WRONLY, 0o644); !errors.Is(err, os.ErrPermission) {
		t.Errorf("create DS_Store: %v", err)
	}
	if err := fs.MkdirAll(".Trashes/501", 0o755); !errors.Is(err, os.ErrPermission) {
		t.Errorf("mkdir trashes: %v", err)
	}
	if _, err := fs.Stat("dir/._old"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("existing litter must be hidden: %v", err)
	}
	entries, err := fs.ReadDir("dir")
	if err != nil || len(entries) != 1 || entries[0].Name() != "real.txt" {
		t.Errorf("readdir = %v %v", entries, err)
	}
	if err := fs.Rename("dir/real.txt", "dir/._real.txt"); !errors.Is(err, os.ErrPermission) {
		t.Errorf("rename to sidecar: %v", err)
	}
	if err := fs.Remove("dir/._old"); err != nil {
		t.Errorf("removing leftovers must work: %v", err)
	}
	g, err := fs.Create("dir/ok.txt")
	if err != nil {
		t.Fatalf("normal create: %v", err)
	}
	_ = g.Close()
	if _, err := fs.Stat("dir/ok.txt"); err != nil {
		t.Errorf("normal stat: %v", err)
	}
}

// countingFS counts the lookups that reach the storage.
type countingFS struct {
	billy.Filesystem
	lookups int
}

func (c *countingFS) Stat(name string) (os.FileInfo, error) {
	c.lookups++
	return c.Filesystem.Stat(name)
}

func (c *countingFS) Lstat(name string) (os.FileInfo, error) {
	c.lookups++
	return c.Filesystem.Lstat(name)
}

// The mount probe asks whether the volume answers; its name never reaches
// the storage, which may be unreachable. Without hide_apple_files litter
// passes through.
func TestProbeNameNeverReachesTheStorage(t *testing.T) {
	for _, hide := range []bool{true, false} {
		inner := &countingFS{Filesystem: memfs.New()}
		fs := wrapFS(inner, hide)
		for _, name := range []string{".lagra-probe-x1y2", "sub/.lagra-probe-z"} {
			if _, err := fs.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("hide=%v: lstat %s: %v", hide, name, err)
			}
		}
		if inner.lookups != 0 {
			t.Errorf("hide=%v: %d probe lookups reached the storage", hide, inner.lookups)
		}
		_, _ = fs.Stat("._x")
		if want := map[bool]int{true: 0, false: 1}[hide]; inner.lookups != want {
			t.Errorf("hide=%v: litter lookups reaching the storage = %d, want %d", hide, inner.lookups, want)
		}
	}
}
