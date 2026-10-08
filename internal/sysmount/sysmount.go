// Package sysmount wraps the macOS side of mounting: mount_nfs, diskutil,
// the kernel mount table and a hang-safe probe of a mount point. Everything
// that touches the system sits behind the System interface so the engine can
// be tested with a fake.
package sysmount

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Entry is one line of the mount table.
type Entry struct {
	Device     string // e.g. "localhost:/"
	MountPoint string
	FSType     string // e.g. "nfs"
}

// Ours reports whether the entry looks like one of lagra's NFS mounts.
func (e Entry) Ours() bool { return e.FSType == "nfs" && e.Device == Device }

// System is what the engine needs from the operating system.
type System interface {
	// Table returns the current mount table.
	Table() ([]Entry, error)
	// EnsureMountPoint creates the mount point directory if needed and checks
	// that nothing is mounted on it.
	EnsureMountPoint(path string) error
	// Mount runs mount_nfs against the local server on port.
	Mount(ctx context.Context, port int, mountPoint string, readOnly bool) error
	// Unmount detaches the mount point; force uses diskutil's force mode.
	Unmount(ctx context.Context, mountPoint string, force bool) error
	// Probe checks that the mount point answers within the context deadline.
	Probe(ctx context.Context, mountPoint string) error
}

// Errors the engine distinguishes.
var (
	ErrHung       = errors.New("mount point did not answer before the deadline")
	ErrStale      = errors.New("stale NFS file handle")
	ErrNotMounted = errors.New("mount point is not mounted")
	ErrBusy       = errors.New("mount point is busy")
	ErrForeign    = errors.New("mount point is occupied by a mount lagra does not manage")
)

// The mount is soft: macOS's NFS client gives up on a request after retrans
// more tries and the program gets an error instead of hanging. timeo (in
// tenths of a second) is only the upper bound of each try; in practice
// macOS fails a request after about 8 s (read) to 40 s (listing), see
// docs/internals.md.
const (
	nfsTimeo   = 600 // 60 s
	nfsRetrans = 2
)

// Unresponsive is how long a volume may leave the mount probe unanswered
// while its server still answers before lagra calls it hung and remounts:
// timeo × (retrans+1), the longest macOS's NFS client could wait for one
// request.
const Unresponsive = nfsTimeo * 100 * time.Millisecond * (nfsRetrans + 1)

// nfsOptions builds the -o argument for mount_nfs, from grrclone's set (see
// docs/internals.md): soft + timeo/retrans make a dead backend error out
// instead of hanging; nolocks and locallocks because rclone's NFS server has
// no lock daemon; nfc normalises file names the way Finder expects.
func nfsOptions(port int, readOnly bool) string {
	opts := []string{
		"port=" + strconv.Itoa(port), "mountport=" + strconv.Itoa(port), "tcp",
		"soft", "intr", "timeo=" + strconv.Itoa(nfsTimeo), "retrans=" + strconv.Itoa(nfsRetrans),
		"nolocks", "locallocks", "nfc",
		"rsize=131072", "wsize=131072",
	}
	if readOnly {
		opts = append(opts, "rdonly")
	}
	return strings.Join(opts, ",")
}

// Device is what the mount table shows as the source for lagra's mounts.
const Device = "localhost:/"

// Runner executes a command; tests replace it.
type Runner func(ctx context.Context, name string, args ...string) (combined []byte, err error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.Bytes(), err
}

// Darwin is the real System.
type Darwin struct {
	Run       Runner
	TableFunc func() ([]Entry, error)           // override for tests; nil = kernel table
	LstatFunc func(string) (os.FileInfo, error) // override for tests; nil = os.Lstat

	mu      sync.Mutex
	probing map[string]bool // mount points with an lstat still under way
}

func (d *Darwin) run() Runner {
	if d.Run == nil {
		return execRunner
	}
	return d.Run
}

// Table implements System.
func (d *Darwin) Table() ([]Entry, error) {
	if d.TableFunc != nil {
		return d.TableFunc()
	}
	return kernelTable()
}

// EnsureMountPoint implements System.
func (d *Darwin) EnsureMountPoint(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create mount point: %w", err)
	}
	entries, err := d.Table()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.MountPoint == path {
			if e.Ours() {
				return fmt.Errorf("%w: %s", ErrBusy, path) // one of ours, still mounted
			}
			return fmt.Errorf("%w: %s (%s from %s)", ErrForeign, path, e.FSType, e.Device)
		}
	}
	return nil
}

// Mount implements System.
func (d *Darwin) Mount(ctx context.Context, port int, mountPoint string, readOnly bool) error {
	out, err := d.run()(ctx, "/sbin/mount_nfs", "-o", nfsOptions(port, readOnly), Device, mountPoint)
	if err != nil {
		return fmt.Errorf("mount_nfs: %s", firstLine(out, err))
	}
	return nil
}

// Unmount implements System. diskutil asks the system to detach cleanly;
// force is what rclone itself uses on macOS.
func (d *Darwin) Unmount(ctx context.Context, mountPoint string, force bool) error {
	args := []string{"unmount"}
	if force {
		args = append(args, "force")
	}
	args = append(args, mountPoint)
	out, err := d.run()(ctx, "/usr/sbin/diskutil", args...)
	if err != nil {
		all := strings.ToLower(string(out))
		msg := firstLine(out, err)
		switch {
		case strings.Contains(all, "not currently mounted") || strings.Contains(all, "not a mount point") || strings.Contains(all, "does not exist"):
			return fmt.Errorf("%w: %s", ErrNotMounted, mountPoint)
		case strings.Contains(all, "busy") || strings.Contains(all, "in use"):
			return fmt.Errorf("%w: %s", ErrBusy, msg)
		}
		return fmt.Errorf("diskutil unmount: %s", msg)
	}
	return nil
}

// Probe implements System: an lstat of a random, non-existent name under the
// mount point. ENOENT means the server answered; a timeout or ESTALE means the
// mount is broken. The lstat runs in its own goroutine because a hung NFS
// call cannot be interrupted; the goroutine is abandoned on timeout. While it
// is still under way, the next probe of that mount point reports ErrHung at
// once instead of queueing another request on a busy volume.
func (d *Darwin) Probe(ctx context.Context, mountPoint string) error {
	d.mu.Lock()
	if d.probing[mountPoint] {
		d.mu.Unlock()
		return ErrHung
	}
	if d.probing == nil {
		d.probing = map[string]bool{}
	}
	d.probing[mountPoint] = true
	d.mu.Unlock()
	lstat := d.LstatFunc
	if lstat == nil {
		lstat = os.Lstat
	}
	target := filepath.Join(mountPoint, ProbePrefix+rand.Text())
	done := make(chan error, 1)
	go func() {
		_, err := lstat(target)
		d.mu.Lock()
		delete(d.probing, mountPoint)
		d.mu.Unlock()
		done <- err
	}()
	select {
	case err := <-done:
		return classifyProbe(err)
	case <-ctx.Done():
		return ErrHung
	}
}

func classifyProbe(err error) error {
	switch {
	case err == nil, errors.Is(err, os.ErrNotExist):
		return nil // the server answered (that the name exists is odd, but fine)
	case errors.Is(err, syscall.ESTALE):
		return ErrStale
	case errors.Is(err, syscall.ETIMEDOUT):
		return ErrHung
	}
	return fmt.Errorf("probe: %w", err)
}

func firstLine(out []byte, err error) string {
	s := strings.TrimSpace(string(out))
	if s == "" {
		return err.Error()
	}
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// ProbePrefix starts the name the mount probe looks up. The NFS server
// answers it without asking the storage, so that the probe tells whether
// the volume answers, not whether the storage does.
const ProbePrefix = ".lagra-probe-"

// ProbeTimeout is the deadline for one mount probe.
const ProbeTimeout = 5 * time.Second
