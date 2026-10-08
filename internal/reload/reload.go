// Package reload watches the config file for changes the way dotfiles
// actually change: atomic saves (write + rename), symlinks pointing somewhere
// new, delete and recreate, git checkout. It watches directories, never the
// file itself, re-resolves the symlink on every event, debounces, and falls
// back to periodic content hashing in case events get lost.
package reload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher reports when the content behind Path changes.
type Watcher struct {
	Path         string        // configured path, may be a symlink
	Debounce     time.Duration // default 400 ms
	PollInterval time.Duration // default 30 s
	OnChange     func()
	Log          *slog.Logger
}

// hash returns the content hash of the file behind path, "" if unreadable.
func hash(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Run blocks until ctx is done. The first change is reported relative to the
// content at start; callers load the initial config themselves.
func (w *Watcher) Run(ctx context.Context) error {
	if w.Debounce == 0 {
		w.Debounce = 400 * time.Millisecond
	}
	if w.PollInterval == 0 {
		w.PollInterval = 30 * time.Second
	}
	if w.Log == nil {
		w.Log = slog.Default()
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fw.Close()

	watched := map[string]bool{}
	addDirs := func() {
		for _, d := range w.dirs() {
			if watched[d] {
				continue
			}
			if err := fw.Add(d); err != nil {
				w.Log.Debug("watch dir failed", "dir", d, "err", err.Error())
				continue
			}
			watched[d] = true
		}
	}
	addDirs()

	last := hash(w.Path)
	debounce := time.NewTimer(w.Debounce)
	debounce.Stop() // armed by the first event
	poll := time.NewTicker(w.PollInterval)
	defer poll.Stop()

	check := func() {
		addDirs() // the symlink may now point into a new directory
		cur := hash(w.Path)
		if cur != last {
			last = cur
			w.Log.Debug("config changed", "path", w.Path, "hash", cur[:min(12, len(cur))])
			if w.OnChange != nil {
				w.OnChange()
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-fw.Events:
			if !ok {
				return nil
			}
			if !w.relevant(ev.Name) {
				continue
			}
			debounce.Reset(w.Debounce)
		case <-debounce.C:
			check()
		case err, ok := <-fw.Errors:
			if !ok {
				return nil
			}
			w.Log.Warn("config watcher error", "err", err.Error())
		case <-poll.C:
			check()
		}
	}
}

// dirs returns the directories worth watching: the one holding the
// configured path and the one holding the resolved target.
func (w *Watcher) dirs() []string {
	out := []string{filepath.Dir(w.Path)}
	if real, err := filepath.EvalSymlinks(w.Path); err == nil {
		if d := filepath.Dir(real); d != out[0] {
			out = append(out, d)
		}
	}
	// A dangling symlink still tells us where to look.
	if target, err := os.Readlink(w.Path); err == nil {
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(w.Path), target)
		}
		if d := filepath.Dir(target); d != out[0] && (len(out) < 2 || d != out[1]) {
			out = append(out, d)
		}
	}
	return out
}

// relevant filters directory events down to the ones that can affect our
// file: the file itself, the symlink, or the resolved target. Editors' temp
// files (".config.toml.swp", "config.toml~", "4913") end with a rename to
// the real name, which we do see, so ignoring them is safe.
func (w *Watcher) relevant(name string) bool {
	base := filepath.Base(name)
	if base == filepath.Base(w.Path) {
		return true
	}
	if real, err := filepath.EvalSymlinks(w.Path); err == nil && (name == real || base == filepath.Base(real)) {
		return true
	}
	if target, err := os.Readlink(w.Path); err == nil && base == filepath.Base(target) {
		return true
	}
	return false
}
