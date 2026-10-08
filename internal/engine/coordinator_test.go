package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eltupe/lagra/internal/config"
	"github.com/eltupe/lagra/internal/rclone"
	"github.com/eltupe/lagra/internal/state"
	"github.com/eltupe/lagra/internal/sysmount"
)

type harness struct {
	starter *fakeStarter
	sys     *fakeSys
	secrets *fakeSecrets
	notify  *fakeNotifier
	store   *state.Store
	deps    Deps
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{starter: &fakeStarter{}, sys: newFakeSys(), secrets: &fakeSecrets{}, notify: &fakeNotifier{}}
	h.store = &state.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	h.deps = Deps{
		Starter: h.starter, Sys: h.sys, Secrets: h.secrets, State: h.store, Notifier: h.notify,
		Log:            slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		HandleCacheDir: t.TempDir(),
		SecretTimeout:  time.Second, StartTimeout: time.Second, MountTimeout: time.Second,
		ProbeTimeout: 200 * time.Millisecond, BackendTimeout: 200 * time.Millisecond, UnmountTimeout: time.Second,
		HungAfter:   40 * time.Millisecond,
		BackoffBase: 20 * time.Millisecond, BackoffCap: 80 * time.Millisecond,
		UploadStuck: 50 * time.Millisecond,
	}
	return h
}

func def(name string) config.EffectiveMount {
	return config.EffectiveMount{
		Name: name, Account: "acct", Type: "s3", Path: "bucket", MountPoint: "/Users/t/Volumes/" + name, Enabled: true,
		MountProbeInterval: 15 * time.Millisecond, BackendProbeInterval: 15 * time.Millisecond,
		Options: map[string]string{"provider": "AWS"}, VFS: map[string]string{"vfs_cache_mode": "full"},
		Secrets: map[string]config.SecretSource{"access_key_id": {Keychain: true}},
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitState(t *testing.T, c *Coordinator, st State) {
	t.Helper()
	waitFor(t, "state "+string(st)+" (now "+string(c.Snapshot().State)+")", func() bool { return c.Snapshot().State == st })
}

func TestConnectHappyPath(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	s := c.Snapshot()
	if s.Port == 0 || !h.sys.isMounted("/Users/t/Volumes/a") {
		t.Errorf("snapshot %+v, mounted=%v", s, h.sys.isMounted("/Users/t/Volumes/a"))
	}
	spec := h.starter.starts[0]
	if spec.Options["access_key_id"] != "secret-access_key_id" || spec.Options["provider"] != "AWS" {
		t.Errorf("secrets not merged into options: %v", spec.Options)
	}
	h.secrets.mu.Lock()
	if len(h.secrets.accounts) == 0 || h.secrets.accounts[0] != "acct" {
		t.Errorf("secrets resolved for %v, want the account", h.secrets.accounts)
	}
	h.secrets.mu.Unlock()
	if spec.HandleCacheDir != filepath.Join(h.deps.HandleCacheDir, "a") {
		t.Errorf("handle cache dir = %s", spec.HandleCacheDir)
	}
	st, _ := h.store.Load()
	if rec, ok := st.Mounts["a"]; !ok || rec.MountPoint != "/Users/t/Volumes/a" || rec.Port != s.Port {
		t.Errorf("state record = %+v", st.Mounts)
	}
	c.Disconnect(true, false)
	waitState(t, c, Disconnected)
	if h.sys.isMounted("/Users/t/Volumes/a") || !h.starter.last().stopped {
		t.Error("disconnect must unmount and stop the server")
	}
	if !c.Snapshot().Manual {
		t.Error("manual flag missing")
	}
	st, _ = h.store.Load()
	if len(st.Mounts) != 0 {
		t.Errorf("state must forget disconnected mount: %+v", st.Mounts)
	}
	// A non-manual connect (config reload) must respect the manual disconnect.
	c.Connect(false)
	time.Sleep(30 * time.Millisecond)
	if c.Snapshot().State != Disconnected || h.starter.count() != 1 {
		t.Error("reload must not undo a manual disconnect")
	}
	c.Connect(true)
	waitState(t, c, Connected)
}

func TestMissingSecretIsFailed(t *testing.T) {
	h := newHarness(t)
	h.secrets.setErr(errors.New("keychain item not found"))
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Failed)
	time.Sleep(100 * time.Millisecond)
	if h.starter.count() != 0 || c.Snapshot().State != Failed {
		t.Error("failed must not retry")
	}
	if h.notify.count() != 1 {
		t.Errorf("want one notification, got %d", h.notify.count())
	}
	// A reload with the same definition triggers connect again.
	h.secrets.setErr(nil)
	c.Connect(false)
	waitState(t, c, Connected)
}

// A bucket or folder that does not exist is a setup error: Failed, no retry.
func TestMissingPathIsFailed(t *testing.T) {
	h := newHarness(t)
	h.starter.mu.Lock()
	h.starter.startErr = &rclone.PathNotFoundError{Path: "nope"}
	h.starter.mu.Unlock()
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Failed)
	time.Sleep(100 * time.Millisecond)
	if h.starter.count() != 1 || c.Snapshot().State != Failed || !strings.Contains(c.Snapshot().LastError, "does not exist") {
		t.Errorf("starts = %d, snapshot = %+v", h.starter.count(), c.Snapshot())
	}
}

func TestForeignMountIsFailed(t *testing.T) {
	h := newHarness(t)
	h.sys.set(func(s *fakeSys) {
		s.table = append(s.table, sysmount.Entry{Device: "//smb", MountPoint: "/Users/t/Volumes/a", FSType: "smbfs"})
	})
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Failed)
	if len(h.sys.forceUnms) != 0 || len(h.sys.unmounts) != 0 {
		t.Error("foreign mounts must never be touched")
	}
}

func TestLeftoverOwnMountIsReplaced(t *testing.T) {
	h := newHarness(t)
	h.sys.set(func(s *fakeSys) {
		s.table = append(s.table, sysmount.Entry{Device: sysmount.Device, MountPoint: "/Users/t/Volumes/a", FSType: "nfs"})
	})
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	if len(h.sys.forceUnms) != 1 {
		t.Errorf("leftover must be force-unmounted once, got %v", h.sys.forceUnms)
	}
}

func TestStartErrorTransientBecomesBrokenAndRepairs(t *testing.T) {
	h := newHarness(t)
	h.starter.startErr = errors.New("dial tcp: connection refused")
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Broken)
	waitFor(t, "second attempt", func() bool { return h.starter.count() >= 2 })
	h.starter.mu.Lock()
	h.starter.startErr = nil
	h.starter.mu.Unlock()
	waitState(t, c, Connected)
	if c.Snapshot().Attempt != 0 {
		t.Error("attempt must reset after success")
	}
}

func TestStartErrorPermanentIsFailed(t *testing.T) {
	h := newHarness(t)
	h.starter.startErr = errors.New("s3: invalid value for region")
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Failed)
}

// A login the storage refuses is never retried: each attempt is another
// failed login, and directory services lock accounts after a few.
func TestRejectedLoginAtStartIsFailed(t *testing.T) {
	h := newHarness(t)
	h.starter.startErr = fmt.Errorf("open smb:share: %w", &rclone.LoginRejectedError{Err: errors.New("logon failure")})
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Failed)
	time.Sleep(100 * time.Millisecond)
	if h.starter.count() != 1 || c.Snapshot().State != Failed {
		t.Errorf("a rejected login must not be retried: %d starts, %s", h.starter.count(), c.Snapshot().State)
	}
	if h.notify.count() != 1 {
		t.Errorf("want one notification, got %d", h.notify.count())
	}
	// New credentials restart the mount.
	h.starter.mu.Lock()
	h.starter.startErr = nil
	h.starter.mu.Unlock()
	c.Restart()
	waitState(t, c, Connected)
}

// A login rejected after the volume is up (the root check timed out, the
// password expired) takes the volume down, pending uploads or not: none of
// them can upload, and each request would be another failed login.
func TestRejectedLoginWhileMountedStopsTheMount(t *testing.T) {
	rejected := &rclone.LoginRejectedError{Err: errors.New("logon failure")}
	t.Run("first probe", func(t *testing.T) {
		h := newHarness(t)
		h.starter.probeErr = rejected
		c := NewCoordinator(h.deps, def("a"), nil)
		c.Connect(false)
		waitState(t, c, Failed)
		if h.sys.isMounted("/Users/t/Volumes/a") || !h.starter.last().stopped || h.starter.count() != 1 {
			t.Error("the volume must go, without a second attempt")
		}
	})
	t.Run("later probe", func(t *testing.T) {
		h := newHarness(t)
		c := NewCoordinator(h.deps, def("a"), nil)
		c.Connect(false)
		waitState(t, c, Connected)
		inst := h.starter.last()
		inst.set(func(f *fakeInstance) { f.uploads = 2; f.probeErr = rejected })
		waitState(t, c, Failed)
		time.Sleep(50 * time.Millisecond)
		if h.sys.isMounted("/Users/t/Volumes/a") || !inst.stopped || h.starter.count() != 1 {
			t.Error("the volume must go, without a second attempt")
		}
		if s := c.Snapshot(); !strings.Contains(s.LastError, "rejected the login") {
			t.Errorf("last error = %q", s.LastError)
		}
		if h.notify.count() != 1 {
			t.Errorf("want one notification, got %d", h.notify.count())
		}
	})
}

// A restart that is already waiting (new credentials, uploads still
// pending) wins over a rejected login: it may bring the fix.
func TestPendingRestartWinsOverRejectedLogin(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	inst := h.starter.last()
	inst.set(func(f *fakeInstance) { f.uploads = 1 })
	waitFor(t, "uploads visible", func() bool { return c.Snapshot().Uploads == 1 })
	c.Restart()
	waitFor(t, "pending restart", func() bool { p := c.Snapshot().Pending; return p != nil && p.Action == "restart" })
	inst.set(func(f *fakeInstance) { f.probeErr = &rclone.LoginRejectedError{Err: errors.New("logon failure")} })
	time.Sleep(50 * time.Millisecond)
	if st := c.Snapshot().State; st == Failed || h.starter.count() != 1 {
		t.Fatalf("the rejected login replaced the pending restart: %s, %d starts", st, h.starter.count())
	}
	inst.set(func(f *fakeInstance) { f.uploads = 0 })
	waitFor(t, "restart", func() bool { return h.starter.count() == 2 && c.Snapshot().State == Connected })
}

func TestBackoffCapNotifiesOnce(t *testing.T) {
	h := newHarness(t)
	h.starter.startErr = errors.New("timeout")
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitFor(t, "several attempts", func() bool { return h.starter.count() >= 5 })
	time.Sleep(200 * time.Millisecond)
	if h.notify.count() != 1 {
		t.Errorf("want exactly one cap notification, got %d", h.notify.count())
	}
	if d := backoff(3, time.Second, time.Minute); d != 4*time.Second {
		t.Errorf("backoff(3) = %s", d)
	}
	if d := backoff(20, time.Second, time.Minute); d != time.Minute {
		t.Errorf("Backoff cap = %s", d)
	}
}

func TestMountProbeHangBreaksAndRepairs(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	first := h.starter.last()
	h.sys.set(func(s *fakeSys) { s.probeErr["/Users/t/Volumes/a"] = sysmount.ErrHung })
	waitState(t, c, Broken)
	h.sys.set(func(s *fakeSys) { delete(s.probeErr, "/Users/t/Volumes/a") })
	waitFor(t, "repair", func() bool { return c.Snapshot().State == Connected && h.starter.count() == 2 })
	if !first.stopped || len(h.sys.forceUnms) == 0 {
		t.Error("repair must stop the old server and force-unmount, never reattach")
	}
}

func TestBackendProbeTogglesOffline(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	inst := h.starter.last()
	inst.set(func(f *fakeInstance) { f.probeErr = errors.New("no route") })
	waitState(t, c, Offline)
	if !h.sys.isMounted("/Users/t/Volumes/a") || h.starter.count() != 1 {
		t.Error("offline must keep the volume and must not remount")
	}
	inst.set(func(f *fakeInstance) { f.probeErr = nil })
	waitState(t, c, Connected)
}

func TestServerDeathIsBroken(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	h.starter.last().set(func(f *fakeInstance) { f.serveErr = errBoom })
	waitFor(t, "repair after server death", func() bool { return h.starter.count() >= 2 })
	waitState(t, c, Connected)
}

func TestEjectInFinderIsManualDisconnect(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	h.sys.set(func(s *fakeSys) { s.table = nil }) // user ejected
	waitState(t, c, Disconnected)
	if !c.Snapshot().Manual {
		t.Error("eject must count as manual")
	}
	time.Sleep(60 * time.Millisecond)
	if h.starter.count() != 1 {
		t.Error("must not remount after eject")
	}
}

func TestPendingUploadsDeferDisconnect(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	inst := h.starter.last()
	inst.set(func(f *fakeInstance) { f.uploads = 3 })
	waitFor(t, "uploads visible", func() bool { return c.Snapshot().Uploads == 3 })
	c.Disconnect(true, false)
	waitFor(t, "pending change", func() bool {
		p := c.Snapshot().Pending
		return p != nil && p.Reason == "uploads pending" && p.Action == "disconnect"
	})
	if c.Snapshot().State != Connected {
		t.Error("must stay connected while uploads are pending")
	}
	waitFor(t, "stuck notification", func() bool { return h.notify.count() == 1 && c.Snapshot().Stuck })
	inst.set(func(f *fakeInstance) { f.uploads = 0 })
	waitState(t, c, Disconnected)
	if c.Snapshot().Pending != nil {
		t.Error("pending must clear")
	}
}

func TestForceDisconnectIgnoresUploads(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	h.starter.last().set(func(f *fakeInstance) { f.uploads = 3 })
	c.Disconnect(true, true)
	waitState(t, c, Disconnected)
}

func TestBusyMountPointDefers(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	h.sys.set(func(s *fakeSys) { s.busy["/Users/t/Volumes/a"] = true })
	c.Disconnect(true, false)
	waitFor(t, "in use pending", func() bool {
		p := c.Snapshot().Pending
		return p != nil && p.Reason == "in use"
	})
	h.sys.set(func(s *fakeSys) { s.busy["/Users/t/Volumes/a"] = false })
	waitState(t, c, Disconnected)
	if len(h.sys.forceUnms) != 0 {
		t.Error("must not force while user did not ask")
	}
}

func TestRestartChangeReconnectsWithNewDefinition(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	nd := def("a")
	nd.Path = "other-bucket"
	c.Update(nd)
	waitFor(t, "restart", func() bool { return h.starter.count() == 2 && c.Snapshot().State == Connected })
	if h.starter.starts[1].Path != "other-bucket" {
		t.Errorf("second start used %s", h.starter.starts[1].Path)
	}
}

func TestRestartRereadsSecrets(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	c.Restart()
	waitFor(t, "second start", func() bool { return h.starter.count() == 2 && c.Snapshot().State == Connected })
	if !h.starter.instances[0].stopped {
		t.Error("old server must be stopped on restart")
	}
}

func TestSync(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	if _, err := c.Sync(context.Background()); err == nil {
		t.Error("sync of a disconnected mount must fail")
	}
	c.Connect(false)
	waitState(t, c, Connected)
	h.starter.last().set(func(f *fakeInstance) { f.uploads = 2 })
	n, err := c.Sync(context.Background())
	if err != nil || n != 2 || h.starter.last().synced != 1 {
		t.Errorf("sync = %d %v synced=%d", n, err, h.starter.last().synced)
	}
}

func TestLiveChangeDoesNotTouchMount(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	nd := def("a")
	nd.MountProbeInterval = 40 * time.Millisecond
	c.Update(nd)
	time.Sleep(60 * time.Millisecond)
	if h.starter.count() != 1 || c.Snapshot().State != Connected {
		t.Error("live change must not reconnect")
	}
	nd.Enabled = false
	c.Update(nd)
	waitState(t, c, Disconnected)
	if c.Snapshot().Manual {
		t.Error("disable via config is not a manual disconnect")
	}
	nd.Enabled = true
	c.Update(nd)
	waitState(t, c, Connected)
}

func TestDisconnectDuringConnectingCleansUp(t *testing.T) {
	h := newHarness(t)
	h.starter.block = make(chan struct{})
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connecting)
	c.Disconnect(true, false)
	close(h.starter.block)
	waitState(t, c, Disconnected)
	waitFor(t, "server stopped", func() bool { i := h.starter.last(); return i != nil && i.stopped })
	if h.sys.isMounted("/Users/t/Volumes/a") {
		t.Error("nothing may stay mounted")
	}
}

func TestRemoveEndsLoop(t *testing.T) {
	h := newHarness(t)
	removed := make(chan struct{})
	c := NewCoordinator(h.deps, def("a"), func() { close(removed) })
	c.Connect(false)
	waitState(t, c, Connected)
	c.Remove(false)
	select {
	case <-removed:
	case <-time.After(3 * time.Second):
		t.Fatal("remove did not finish")
	}
	<-c.quit
	if h.sys.isMounted("/Users/t/Volumes/a") {
		t.Error("remove must unmount")
	}
}

func TestShutdownForcesAfterBusy(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	h.sys.set(func(s *fakeSys) { s.busy["/Users/t/Volumes/a"] = true })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.Shutdown(ctx)
	if h.sys.isMounted("/Users/t/Volumes/a") {
		t.Error("shutdown must force-unmount a busy volume")
	}
}

// Connect on a mount whose volume hangs must replace the old server, not
// start a second one next to it.
func TestConnectWhileBrokenReplacesServer(t *testing.T) {
	h := newHarness(t)
	h.deps.BackoffBase, h.deps.BackoffCap = time.Hour, time.Hour
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	first := h.starter.last()
	h.sys.set(func(s *fakeSys) { s.probeErr["/Users/t/Volumes/a"] = sysmount.ErrHung })
	waitState(t, c, Broken)
	h.sys.set(func(s *fakeSys) { delete(s.probeErr, "/Users/t/Volumes/a") })
	c.Connect(true)
	waitFor(t, "reconnected", func() bool { return c.Snapshot().State == Connected && h.starter.count() == 2 })
	if !first.stopped {
		t.Error("the hung server must be stopped")
	}
}

// An unmount that fails for another reason than "busy" falls back to force
// and still stops the server.
func TestUnmountErrorStillStopsServer(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	h.sys.set(func(s *fakeSys) { s.unmountErr["/Users/t/Volumes/a"] = errors.New("timed out") })
	c.Disconnect(true, false)
	waitState(t, c, Disconnected)
	if !h.starter.last().stopped || h.sys.isMounted("/Users/t/Volumes/a") {
		t.Error("the server must be stopped and the volume gone")
	}
}

func TestConnectClearsPendingDisconnect(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	h.starter.last().set(func(f *fakeInstance) { f.uploads = 3 })
	c.Disconnect(true, false)
	waitFor(t, "pending disconnect", func() bool { return c.Snapshot().Pending != nil })
	c.Connect(true)
	waitFor(t, "pending cleared", func() bool { return c.Snapshot().Pending == nil })
	if c.Snapshot().State != Connected {
		t.Error("must stay connected")
	}
}

// A restart waiting for uploads keeps its new definition when the user
// presses Connect meanwhile.
func TestPendingRestartSurvivesConnect(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	inst := h.starter.last()
	inst.set(func(f *fakeInstance) { f.uploads = 3 })
	waitFor(t, "uploads visible", func() bool { return c.Snapshot().Uploads == 3 })
	nd := def("a")
	nd.Path = "other-bucket"
	c.Update(nd)
	waitFor(t, "pending restart", func() bool { p := c.Snapshot().Pending; return p != nil && p.Action == "restart" })
	c.Connect(true)
	inst.set(func(f *fakeInstance) { f.uploads = 0 })
	waitFor(t, "restarted", func() bool { return h.starter.count() == 2 && c.Snapshot().State == Connected })
	if got := h.starter.starts[1].Path; got != "other-bucket" {
		t.Errorf("restart used %s", got)
	}
}

// Ejecting the volume while a removal waits for uploads finishes the
// removal instead of turning it into a plain disconnect.
func TestEjectFinishesPendingRemoval(t *testing.T) {
	h := newHarness(t)
	removed := make(chan struct{})
	c := NewCoordinator(h.deps, def("a"), func() { close(removed) })
	c.Connect(false)
	waitState(t, c, Connected)
	h.starter.last().set(func(f *fakeInstance) { f.uploads = 3 })
	waitFor(t, "uploads visible", func() bool { return c.Snapshot().Uploads == 3 })
	c.Remove(false)
	waitFor(t, "pending remove", func() bool { p := c.Snapshot().Pending; return p != nil && p.Action == "remove" })
	h.sys.set(func(s *fakeSys) { s.table = nil }) // user ejected
	select {
	case <-removed:
	case <-time.After(3 * time.Second):
		t.Fatal("removal did not finish after eject")
	}
}

// A volume that does not answer while its server does is busy, not broken:
// remounting would cut off the slow request it is working on.
func TestBusyVolumeIsNotRemounted(t *testing.T) {
	h := newHarness(t)
	h.deps.HungAfter = time.Hour
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	h.sys.set(func(s *fakeSys) { s.probeErr["/Users/t/Volumes/a"] = sysmount.ErrHung })
	time.Sleep(100 * time.Millisecond) // several probe ticks
	if st := c.Snapshot().State; st != Connected || h.starter.count() != 1 {
		t.Fatalf("busy volume: state %s, starts %d", st, h.starter.count())
	}
	h.sys.set(func(s *fakeSys) { delete(s.probeErr, "/Users/t/Volumes/a") })
	time.Sleep(40 * time.Millisecond)
	if c.Snapshot().State != Connected || h.starter.count() != 1 {
		t.Error("the volume must simply carry on")
	}
}

// When the server itself does not answer either, the volume is broken at
// once.
func TestHungVolumeWithSilentServerBreaks(t *testing.T) {
	h := newHarness(t)
	h.deps.HungAfter = time.Hour
	c := NewCoordinator(h.deps, def("a"), nil)
	c.Connect(false)
	waitState(t, c, Connected)
	h.starter.last().set(func(f *fakeInstance) { f.serverErr = errBoom })
	h.sys.set(func(s *fakeSys) { s.probeErr["/Users/t/Volumes/a"] = sysmount.ErrHung })
	waitState(t, c, Broken)
}

// A volume that stays silent longer than macOS's NFS client would wait is
// hung even though its server answers.
func TestLongSilenceBreaksEvenWithServer(t *testing.T) {
	h := newHarness(t)
	c := NewCoordinator(h.deps, def("a"), nil) // HungAfter 40 ms
	c.Connect(false)
	waitState(t, c, Connected)
	h.sys.set(func(s *fakeSys) { s.probeErr["/Users/t/Volumes/a"] = sysmount.ErrHung })
	waitState(t, c, Broken)
	if !strings.Contains(c.Snapshot().LastError, "has not answered") {
		t.Errorf("error = %q", c.Snapshot().LastError)
	}
}
