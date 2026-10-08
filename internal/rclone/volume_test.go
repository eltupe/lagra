package rclone

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	gonfs "github.com/willscott/go-nfs"
)

// slowStat is an NFS handler whose storage takes as long as release says.
type slowStat struct {
	gonfs.Handler
	release chan struct{}
	calls   atomic.Int32
}

func (s *slowStat) FSStat(ctx context.Context, f billy.Filesystem, st *gonfs.FSStat) error {
	s.calls.Add(1)
	<-s.release
	st.TotalSize, st.FreeSize, st.AvailableSize = 100, 40, 40
	return nil
}

// macOS mounts only once FSSTAT is answered; a storage that does not answer
// must not keep the volume from mounting.
func TestFSStatAnswersInTime(t *testing.T) {
	old := statWait
	statWait = 50 * time.Millisecond
	defer func() { statWait = old }()
	inner := &slowStat{release: make(chan struct{})}
	h := newVolumeHandler(inner, false)

	var st gonfs.FSStat
	start := time.Now()
	for range 3 {
		if err := h.FSStat(context.Background(), nil, &st); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("three FSSTATs took %s", d)
	}
	if st != unknownStat {
		t.Errorf("before the storage answers: %+v, want unknown sizes", st)
	}
	if n := inner.calls.Load(); n != 1 {
		t.Errorf("%d FSSTATs went to the storage at once, want 1", n)
	}

	close(inner.release)
	time.Sleep(20 * time.Millisecond)
	if err := h.FSStat(context.Background(), nil, &st); err != nil {
		t.Fatal(err)
	}
	if st.TotalSize != 100 || st.FreeSize != 40 {
		t.Errorf("after the storage answered: %+v", st)
	}
}
