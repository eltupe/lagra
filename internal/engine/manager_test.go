package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eltupe/lagra/internal/config"
	"github.com/eltupe/lagra/internal/state"
	"github.com/eltupe/lagra/internal/sysmount"
)

func effOf(ms ...config.EffectiveMount) *config.Effective {
	return &config.Effective{General: config.EffectiveGeneral{LogLevel: "info"}, Mounts: ms}
}

func TestManagerApplyDiff(t *testing.T) {
	h := newHarness(t)
	m := NewManager(h.deps)
	a, b, off := def("a"), def("b"), def("off")
	off.Enabled = false
	m.Apply(effOf(a, b, off))
	waitFor(t, "a and b connected", func() bool {
		s := m.Snapshots()
		return len(s) == 3 && s[0].State == Connected && s[1].State == Connected && s[2].State == Disconnected
	})
	if h.starter.count() != 2 {
		t.Fatalf("starts = %d", h.starter.count())
	}
	// change a (restart), keep b, drop off, add c, enable nothing else
	a2 := def("a")
	a2.ReadOnly = true
	c := def("c")
	diff := m.Apply(effOf(a2, b, c))
	if len(diff.Restart) != 1 || diff.Restart[0] != "a" || len(diff.Added) != 1 || diff.Added[0] != "c" || len(diff.Unchanged) != 1 || diff.Unchanged[0] != "b" {
		t.Errorf("diff = %+v", diff)
	}
	waitFor(t, "a restarted, c connected", func() bool { return h.starter.count() == 4 })
	waitFor(t, "all connected", func() bool {
		for _, s := range m.Snapshots() {
			if s.State != Connected {
				return false
			}
		}
		return len(m.Snapshots()) == 3
	})
	// b must not have been touched
	nb := 0
	h.starter.mu.Lock()
	for _, spec := range h.starter.starts {
		if spec.Name == "b" {
			nb++
		}
	}
	h.starter.mu.Unlock()
	if nb != 1 {
		t.Errorf("b was started %d times", nb)
	}
	if Summarize(m.Snapshots()) != OverallOK {
		t.Errorf("overall = %s", Summarize(m.Snapshots()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m.Shutdown(ctx)
	if tbl, _ := h.sys.Table(); len(tbl) != 0 {
		t.Errorf("shutdown left mounts: %v", tbl)
	}
	st, _ := h.store.Load()
	if len(st.Mounts) != 0 {
		t.Errorf("state after shutdown: %v", st.Mounts)
	}
}

func TestCleanOrphans(t *testing.T) {
	h := newHarness(t)
	_ = h.store.Save(&state.File{Version: 1, Mounts: map[string]state.Managed{
		"dead":  {Name: "dead", MountPoint: "/Users/t/Volumes/dead", Port: 1},
		"gone":  {Name: "gone", MountPoint: "/Users/t/Volumes/gone", Port: 2},
		"taken": {Name: "taken", MountPoint: "/Users/t/Volumes/taken", Port: 3},
	}})
	h.sys.set(func(s *fakeSys) {
		s.table = []sysmount.Entry{
			{Device: sysmount.Device, MountPoint: "/Users/t/Volumes/dead", FSType: "nfs"},
			{Device: "//smb/x", MountPoint: "/Users/t/Volumes/taken", FSType: "smbfs"}, // foreign now
			{Device: "localhost:/", MountPoint: "/Users/t/other", FSType: "nfs"},       // foreign rclone nfsmount
		}
	})
	m := NewManager(h.deps)
	if err := m.CleanOrphans(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.sys.forceUnms) != 1 || h.sys.forceUnms[0] != "/Users/t/Volumes/dead" {
		t.Errorf("force unmounts = %v", h.sys.forceUnms)
	}
	tbl, _ := h.sys.Table()
	if len(tbl) != 2 {
		t.Errorf("foreign mounts must survive: %v", tbl)
	}
	st, _ := h.store.Load()
	if len(st.Mounts) != 0 {
		t.Errorf("state must be empty after cleanup: %v", st.Mounts)
	}
}

func TestSummarize(t *testing.T) {
	cases := []struct {
		snaps []Snapshot
		want  Overall
	}{
		{nil, OverallIdle},
		{[]Snapshot{{State: Disconnected}}, OverallIdle},
		{[]Snapshot{{State: Connected}}, OverallOK},
		{[]Snapshot{{State: Connected}, {State: Offline}}, OverallPartial},
		{[]Snapshot{{State: Connected}, {State: Broken}}, OverallError},
		{[]Snapshot{{State: Connected, Pending: &PendingChange{}}}, OverallPartial},
	}
	for _, c := range cases {
		if got := Summarize(c.snaps); got != c.want {
			t.Errorf("Summarize(%v) = %s, want %s", c.snaps, got, c.want)
		}
	}
}

func TestNetWatcherDetectsChange(t *testing.T) {
	var addrs atomic.Value
	addrs.Store([]string{"en0 10.0.0.1/24"})
	changed := make(chan struct{}, 1)
	w := NetWatcher{Interval: 5 * time.Millisecond, OnChange: func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}, Addrs: func() ([]string, error) { return addrs.Load().([]string), nil }}
	ctx := t.Context()
	go w.Run(ctx)
	time.Sleep(15 * time.Millisecond)
	addrs.Store([]string{"en0 10.0.0.2/24"})
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("no change detected")
	}
}

// Editing a disabled mount and then enabling it mounts the edited version.
func TestManagerEditWhileDisabled(t *testing.T) {
	h := newHarness(t)
	m := NewManager(h.deps)
	off := def("a")
	off.Enabled = false
	m.Apply(effOf(off))
	edited := def("a")
	edited.Enabled = false
	edited.Path = "other-bucket"
	m.Apply(effOf(edited))
	on := def("a")
	on.Path = "other-bucket"
	m.Apply(effOf(on))
	waitFor(t, "connected", func() bool { s := m.Snapshots(); return len(s) == 1 && s[0].State == Connected })
	if got := h.starter.starts[0].Path; got != "other-bucket" {
		t.Errorf("mounted %s, want the edited path", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m.Shutdown(ctx)
}

// A mount removed from the file while uploads are pending stays visible and
// counts for quit until its removal is done.
func TestManagerKeepsWaitingRemoval(t *testing.T) {
	h := newHarness(t)
	m := NewManager(h.deps)
	m.Apply(effOf(def("a")))
	waitFor(t, "connected", func() bool { s := m.Snapshots(); return len(s) == 1 && s[0].State == Connected })
	inst := h.starter.last()
	inst.set(func(f *fakeInstance) { f.uploads = 3 })
	waitFor(t, "uploads visible", func() bool { return m.PendingUploads() == 3 })
	m.Apply(effOf())
	waitFor(t, "pending removal", func() bool {
		s := m.Snapshots()
		return len(s) == 1 && s[0].Pending != nil && s[0].Pending.Action == "remove"
	})
	if m.PendingUploads() != 3 {
		t.Errorf("pending uploads = %d, want 3", m.PendingUploads())
	}
	inst.set(func(f *fakeInstance) { f.uploads = 0 })
	waitFor(t, "removal done", func() bool { return len(m.Snapshots()) == 0 })
	if h.sys.isMounted("/Users/t/Volumes/a") {
		t.Error("the volume must be gone")
	}
}

func TestManagerUploadStuckIsLive(t *testing.T) {
	h := newHarness(t)
	h.deps.UploadStuck = time.Hour
	m := NewManager(h.deps)
	m.Apply(effOf(def("a")))
	waitFor(t, "connected", func() bool { s := m.Snapshots(); return len(s) == 1 && s[0].State == Connected })
	m.Configure(func(d *Deps) { d.UploadStuck = 30 * time.Millisecond })
	h.starter.last().set(func(f *fakeInstance) { f.uploads = 1 })
	waitFor(t, "stuck notification", func() bool { return h.notify.count() == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m.Shutdown(ctx)
}

// The wall clock jumps over a sleep while the ticker fired only once.
func TestWakeWatcherSeesWallClockJump(t *testing.T) {
	var calls atomic.Int64
	base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	woke := make(chan time.Duration, 1)
	w := WakeWatcher{Interval: 5 * time.Millisecond, OnWake: func(gap time.Duration) {
		select {
		case woke <- gap:
		default:
		}
	}, Now: func() time.Time {
		n := calls.Add(1)
		if n > 3 {
			return base.Add(time.Hour)
		}
		return base.Add(time.Duration(n) * 5 * time.Millisecond)
	}}
	ctx := t.Context()
	go w.Run(ctx)
	select {
	case gap := <-woke:
		if gap < 59*time.Minute {
			t.Errorf("gap = %s", gap)
		}
	case <-time.After(time.Second):
		t.Fatal("no wake detected")
	}
}
