package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/eltupe/lagra/internal/config"
	"github.com/eltupe/lagra/internal/rclone"
	"github.com/eltupe/lagra/internal/sysmount"
)

// fakeInstance is a started server.
type fakeInstance struct {
	mu        sync.Mutex
	port      int
	uploads   int
	probeErr  error
	serverErr error
	serveErr  error
	stopped   bool
	synced    int
}

func (f *fakeInstance) Port() int { return f.port }
func (f *fakeInstance) Probe(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probeErr
}
func (f *fakeInstance) ProbeServer(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.serverErr
}
func (f *fakeInstance) Sync(ctx context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.synced++
	return f.uploads, nil
}
func (f *fakeInstance) Uploads() int { f.mu.Lock(); defer f.mu.Unlock(); return f.uploads }
func (f *fakeInstance) Stop(time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return nil
}
func (f *fakeInstance) Err() error { f.mu.Lock(); defer f.mu.Unlock(); return f.serveErr }
func (f *fakeInstance) set(fn func(*fakeInstance)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// fakeStarter records starts and hands out instances.
type fakeStarter struct {
	mu        sync.Mutex
	starts    []rclone.Spec
	instances []*fakeInstance
	startErr  error
	probeErr  error         // backend probe error of new instances
	block     chan struct{} // when set, Start waits for it or ctx
	nextPort  int
}

func (s *fakeStarter) Start(ctx context.Context, spec rclone.Spec) (rclone.Instance, error) {
	s.mu.Lock()
	s.starts = append(s.starts, spec)
	block := s.block
	err := s.startErr
	probeErr := s.probeErr
	s.nextPort++
	port := 40000 + s.nextPort
	s.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	inst := &fakeInstance{port: port, probeErr: probeErr}
	s.mu.Lock()
	s.instances = append(s.instances, inst)
	s.mu.Unlock()
	return inst, nil
}

func (s *fakeStarter) last() *fakeInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.instances) == 0 {
		return nil
	}
	return s.instances[len(s.instances)-1]
}

func (s *fakeStarter) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.starts) }

// fakeSys is an in-memory mount table.
type fakeSys struct {
	mu         sync.Mutex
	table      []sysmount.Entry
	probeErr   map[string]error
	busy       map[string]bool  // unmount without force fails
	unmountErr map[string]error // unmount without force fails with this
	mountErr   error
	mounts     int
	unmounts   []string
	forceUnms  []string
}

func newFakeSys() *fakeSys {
	return &fakeSys{probeErr: map[string]error{}, busy: map[string]bool{}, unmountErr: map[string]error{}}
}

func (s *fakeSys) Table() ([]sysmount.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sysmount.Entry(nil), s.table...), nil
}

func (s *fakeSys) EnsureMountPoint(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.table {
		if e.MountPoint == path {
			if e.Ours() {
				return fmt.Errorf("%w: %s", sysmount.ErrBusy, path)
			}
			return fmt.Errorf("%w: %s", sysmount.ErrForeign, path)
		}
	}
	return nil
}

func (s *fakeSys) Mount(ctx context.Context, port int, mountPoint string, readOnly bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mountErr != nil {
		return s.mountErr
	}
	s.table = append(s.table, sysmount.Entry{Device: sysmount.Device, MountPoint: mountPoint, FSType: "nfs"})
	s.mounts++
	return nil
}

func (s *fakeSys) Unmount(ctx context.Context, mountPoint string, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if force {
		s.forceUnms = append(s.forceUnms, mountPoint)
	} else {
		s.unmounts = append(s.unmounts, mountPoint)
	}
	if s.busy[mountPoint] && !force {
		return fmt.Errorf("%w: %s", sysmount.ErrBusy, mountPoint)
	}
	if err := s.unmountErr[mountPoint]; err != nil && !force {
		return err
	}
	for i, e := range s.table {
		if e.MountPoint == mountPoint {
			s.table = append(s.table[:i], s.table[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("%w: %s", sysmount.ErrNotMounted, mountPoint)
}

func (s *fakeSys) Probe(ctx context.Context, mountPoint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probeErr[mountPoint]
}

func (s *fakeSys) isMounted(mp string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.table {
		if e.MountPoint == mp {
			return true
		}
	}
	return false
}

func (s *fakeSys) set(fn func(*fakeSys)) { s.mu.Lock(); defer s.mu.Unlock(); fn(s) }

type fakeSecrets struct {
	err      error
	mu       sync.Mutex
	accounts []string // every account Resolve was asked for
}

func (f *fakeSecrets) Resolve(ctx context.Context, account string, src map[string]config.SecretSource) (map[string]string, error) {
	f.mu.Lock()
	f.accounts = append(f.accounts, account)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k := range src {
		out[k] = "secret-" + k
	}
	return out, nil
}

func (f *fakeSecrets) setErr(err error) { f.mu.Lock(); f.err = err; f.mu.Unlock() }

type fakeNotifier struct {
	mu    sync.Mutex
	items []string
}

func (n *fakeNotifier) Notify(title, body string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.items = append(n.items, title)
}

func (n *fakeNotifier) count() int { n.mu.Lock(); defer n.mu.Unlock(); return len(n.items) }

var errBoom = errors.New("boom")
