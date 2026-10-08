package rclone

import (
	"context"
	"math"
	"net"
	"sync"
	"time"

	"github.com/go-git/go-billy/v5"
	gonfs "github.com/willscott/go-nfs"
)

// volumeHandler wraps rclone's NFS handler for one volume. Every
// filesystem it hands to go-nfs is a filteredFS, and two kinds of request
// are answered without waiting for the storage, so that a storage that
// cannot be reached leaves an offline volume rather than none:
//
//   - FSSTAT, which macOS sends while mounting. rclone's VFS asks the
//     storage for its free space (SMB can tell), which waits for a
//     connection, up to rclone's connect timeout of a minute when the
//     server does not answer; mount_nfs gets 30 s.
//   - Lookups of the mount probe's name (sysmount.ProbePrefix), which
//     filteredFS answers: the mount probe asks whether the volume answers;
//     whether the storage does is the backend probe's question.
//
// Handles are translated back to the inner filesystem because rclone's
// handle cache type-asserts it.
type volumeHandler struct {
	gonfs.Handler
	hideApple bool

	statMu   sync.Mutex
	statDone chan struct{} // closed when the running FSStat returns; nil while none runs
	stat     gonfs.FSStat  // the storage's last answer
}

// statWait is how long FSSTAT waits for the storage before it answers
// with the last numbers it has.
var statWait = 2 * time.Second

// unknownStat is what rclone reports for a storage that cannot tell its
// free space (S3): every size unknown, which macOS reads as plenty.
var unknownStat = gonfs.FSStat{TotalSize: math.MaxUint64, FreeSize: math.MaxUint64, AvailableSize: math.MaxUint64}

func newVolumeHandler(h gonfs.Handler, hideApple bool) *volumeHandler {
	return &volumeHandler{Handler: h, hideApple: hideApple, stat: unknownStat}
}

func (h *volumeHandler) Mount(ctx context.Context, conn net.Conn, req gonfs.MountRequest) (gonfs.MountStatus, billy.Filesystem, []gonfs.AuthFlavor) {
	st, f, auths := h.Handler.Mount(ctx, conn, req)
	return st, wrapFS(f, h.hideApple), auths
}

func (h *volumeHandler) Change(f billy.Filesystem) billy.Change {
	if w, ok := f.(*filteredFS); ok {
		if h.Handler.Change(w.inner) == nil {
			return nil
		}
		return w
	}
	return h.Handler.Change(f)
}

// FSStat asks the storage once at a time and answers within statWait,
// with the last answer when the storage takes longer.
func (h *volumeHandler) FSStat(ctx context.Context, f billy.Filesystem, s *gonfs.FSStat) error {
	h.statMu.Lock()
	done := h.statDone
	if done == nil {
		done = make(chan struct{})
		h.statDone = done
		inner := unwrapFS(f)
		go func() {
			var fresh gonfs.FSStat
			err := h.Handler.FSStat(context.WithoutCancel(ctx), inner, &fresh)
			h.statMu.Lock()
			if err == nil {
				h.stat = fresh
			}
			h.statDone = nil
			h.statMu.Unlock()
			close(done)
		}()
	}
	h.statMu.Unlock()
	select {
	case <-done:
	case <-time.After(statWait):
	case <-ctx.Done():
	}
	h.statMu.Lock()
	*s = h.stat
	h.statMu.Unlock()
	return nil
}

func (h *volumeHandler) ToHandle(f billy.Filesystem, p []string) []byte {
	return h.Handler.ToHandle(unwrapFS(f), p)
}

func (h *volumeHandler) FromHandle(b []byte) (billy.Filesystem, []string, error) {
	f, p, err := h.Handler.FromHandle(b)
	return wrapFS(f, h.hideApple), p, err
}

func (h *volumeHandler) InvalidateHandle(f billy.Filesystem, b []byte) error {
	return h.Handler.InvalidateHandle(unwrapFS(f), b)
}
