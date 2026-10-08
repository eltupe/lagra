// Package atomicfile replaces files so that a reader sees the old or the new
// content, never a mix: a temp file in the same directory, then a rename.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write replaces path with data and gives it perm. path must not be a
// symlink; resolve it first, or the link is replaced by a regular file.
func Write(path string, data []byte, perm os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
