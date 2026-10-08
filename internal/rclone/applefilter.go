package rclone

import (
	"os"
	"path"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"

	"github.com/eltupe/lagra/internal/sysmount"
)

// Apple litter: NFSv3 has no extended attributes, so macOS stores them as
// AppleDouble "._name" sidecars and Finder/Spotlight drop marker files. None
// of it belongs in a bucket. The filter refuses creating such names (EACCES,
// as macFUSE's noappledouble does) and hides existing ones from listings.
var appleLitter = map[string]bool{
	".DS_Store":       true,
	".Spotlight-V100": true,
	".fseventsd":      true,
	".Trashes":        true,
	".TemporaryItems": true,
}

// isAppleLitter reports whether any path component is Finder/Spotlight litter.
func isAppleLitter(p string) bool {
	for seg := range strings.SplitSeq(p, "/") {
		if isLitterName(seg) {
			return true
		}
	}
	return false
}

func isLitterName(name string) bool {
	return appleLitter[name] || strings.HasPrefix(name, "._")
}

// refusedOp starts the text of every refusal, so that the log can tell
// them from real permission problems (see RouteLogs).
const refusedOp = "refuse Finder metadata"

// refuse is the error for creating litter: permission denied, the way
// macFUSE's noappledouble answers, naming the file.
func refuse(name string) error {
	return &os.PathError{Op: refusedOp, Path: name, Err: os.ErrPermission}
}

func wrapFS(f billy.Filesystem, hideApple bool) billy.Filesystem {
	if f == nil {
		return nil
	}
	if _, ok := f.(*filteredFS); ok {
		return f
	}
	return &filteredFS{inner: f, hideApple: hideApple}
}

func unwrapFS(f billy.Filesystem) billy.Filesystem {
	if w, ok := f.(*filteredFS); ok {
		return w.inner
	}
	return f
}

// filteredFS is the billy.Filesystem go-nfs sees. It answers the mount
// probe's name itself (see volumeHandler) and, with hideApple, refuses to
// create Apple litter and hides existing litter. Removal is allowed so
// leftovers can be cleaned up.
type filteredFS struct {
	inner     billy.Filesystem
	hideApple bool
}

// litter reports a path that is Apple litter while the filter is on.
func (f *filteredFS) litter(p string) bool { return f.hideApple && isAppleLitter(p) }

// absent reports a path that does not exist for macOS: litter, and the
// mount probe's name, which never reaches the storage.
func (f *filteredFS) absent(p string) bool {
	return f.litter(p) || strings.HasPrefix(path.Base(p), sysmount.ProbePrefix)
}

var (
	_ billy.Filesystem = (*filteredFS)(nil)
	_ billy.Change     = (*filteredFS)(nil)
	_ billy.Capable    = (*filteredFS)(nil)
)

func (f *filteredFS) Create(name string) (billy.File, error) {
	if f.litter(name) {
		return nil, refuse(name)
	}
	return f.inner.Create(name)
}

func (f *filteredFS) Open(name string) (billy.File, error) {
	if f.absent(name) {
		return nil, os.ErrNotExist
	}
	return f.inner.Open(name)
}

func (f *filteredFS) OpenFile(name string, flag int, perm os.FileMode) (billy.File, error) {
	if f.absent(name) {
		if flag&os.O_CREATE != 0 && f.litter(name) {
			return nil, refuse(name)
		}
		return nil, os.ErrNotExist
	}
	return f.inner.OpenFile(name, flag, perm)
}

func (f *filteredFS) Stat(name string) (os.FileInfo, error) {
	if f.absent(name) {
		return nil, os.ErrNotExist
	}
	return f.inner.Stat(name)
}

func (f *filteredFS) Lstat(name string) (os.FileInfo, error) {
	if f.absent(name) {
		return nil, os.ErrNotExist
	}
	return f.inner.Lstat(name)
}

func (f *filteredFS) Rename(oldpath, newpath string) error {
	if f.absent(oldpath) {
		return os.ErrNotExist
	}
	if f.litter(newpath) {
		return refuse(newpath)
	}
	return f.inner.Rename(oldpath, newpath)
}

func (f *filteredFS) Remove(name string) error { return f.inner.Remove(name) }

func (f *filteredFS) Join(elem ...string) string { return f.inner.Join(elem...) }

func (f *filteredFS) TempFile(dir, prefix string) (billy.File, error) {
	if f.litter(dir) || f.litter(prefix) {
		return nil, refuse(path.Join(dir, prefix))
	}
	return f.inner.TempFile(dir, prefix)
}

func (f *filteredFS) ReadDir(p string) ([]os.FileInfo, error) {
	if f.absent(p) {
		return nil, os.ErrNotExist
	}
	entries, err := f.inner.ReadDir(p)
	if err != nil || !f.hideApple {
		return entries, err
	}
	out := entries[:0]
	for _, e := range entries {
		if !isLitterName(e.Name()) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *filteredFS) MkdirAll(name string, perm os.FileMode) error {
	if f.litter(name) {
		return refuse(name)
	}
	return f.inner.MkdirAll(name, perm)
}

func (f *filteredFS) Symlink(target, link string) error {
	if f.litter(link) {
		return refuse(link)
	}
	return f.inner.Symlink(target, link)
}

func (f *filteredFS) Readlink(link string) (string, error) {
	if f.absent(link) {
		return "", os.ErrNotExist
	}
	return f.inner.Readlink(link)
}

func (f *filteredFS) Chroot(p string) (billy.Filesystem, error) {
	if f.absent(p) {
		return nil, os.ErrNotExist
	}
	sub, err := f.inner.Chroot(p)
	if err != nil {
		return nil, err
	}
	return wrapFS(sub, f.hideApple), nil
}

func (f *filteredFS) Root() string { return f.inner.Root() }

// change runs op on the inner filesystem's billy.Change; litter does not
// exist.
func (f *filteredFS) change(name string, op func(billy.Change) error) error {
	if f.absent(name) {
		return os.ErrNotExist
	}
	if c, ok := f.inner.(billy.Change); ok {
		return op(c)
	}
	return billy.ErrNotSupported
}

func (f *filteredFS) Chmod(name string, mode os.FileMode) error {
	return f.change(name, func(c billy.Change) error { return c.Chmod(name, mode) })
}

func (f *filteredFS) Lchown(name string, uid, gid int) error {
	return f.change(name, func(c billy.Change) error { return c.Lchown(name, uid, gid) })
}

func (f *filteredFS) Chown(name string, uid, gid int) error {
	return f.change(name, func(c billy.Change) error { return c.Chown(name, uid, gid) })
}

func (f *filteredFS) Chtimes(name string, atime, mtime time.Time) error {
	return f.change(name, func(c billy.Change) error { return c.Chtimes(name, atime, mtime) })
}

func (f *filteredFS) Capabilities() billy.Capability {
	return billy.Capabilities(f.inner)
}
