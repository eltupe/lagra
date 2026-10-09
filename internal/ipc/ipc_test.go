package ipc

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/eltupe/lagra/internal/engine"
)

type fakeCtl struct {
	connected    []string
	disconnected []string
	force        bool
	shutdowns    int
}

func (f *fakeCtl) Status() Status {
	return Status{Version: "test", Overall: engine.OverallOK, Mounts: []engine.Snapshot{{Name: "a", State: engine.Connected}}}
}
func (f *fakeCtl) Connect(name string) error {
	if name == "nope" {
		return ErrNoMount
	}
	f.connected = append(f.connected, name)
	return nil
}
func (f *fakeCtl) Disconnect(name string, force bool) error {
	f.disconnected = append(f.disconnected, name)
	f.force = force
	return nil
}
func (f *fakeCtl) RestartAccount(string) []string { return []string{"a"} }
func (f *fakeCtl) Sync(context.Context, string) (SyncResult, error) {
	return SyncResult{Expedited: 1}, nil
}
func (f *fakeCtl) Reload() ReloadResult {
	return ReloadResult{OK: false, Issues: []string{"line 3: bad"}}
}
func (f *fakeCtl) Shutdown(force bool) error {
	if !force {
		return ErrPendingUploads
	}
	f.shutdowns++
	return nil
}

func TestRoundTrip(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "m.sock")
	ctl := &fakeCtl{}
	srv := &Server{Socket: sock, Ctl: ctl}
	ctx := t.Context()
	go func() { _ = srv.ListenAndServe(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for !Ping(sock) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	c := NewClient(sock)
	st, err := c.Status(ctx)
	if err != nil || st.Version != "test" || len(st.Mounts) != 1 {
		t.Fatalf("status %+v %v", st, err)
	}
	if err := c.Connect(ctx, "a"); err != nil || ctl.connected[0] != "a" {
		t.Errorf("connect: %v", err)
	}
	if err := c.Connect(ctx, "nope"); !errors.Is(err, ErrNoMount) {
		t.Errorf("unknown mount: %v", err)
	}
	if err := c.Disconnect(ctx, "a", true); err != nil || !ctl.force {
		t.Errorf("disconnect force: %v", err)
	}
	if names, err := c.RestartAccount(ctx, "acct"); err != nil || len(names) != 1 || names[0] != "a" {
		t.Errorf("restart account: %v %v", names, err)
	}
	r, err := c.Reload(ctx)
	if err != nil || r.OK || r.Issues[0] != "line 3: bad" {
		t.Errorf("reload: %+v %v", r, err)
	}
	if err := c.Shutdown(ctx, false); !errors.Is(err, ErrPendingUploads) {
		t.Errorf("shutdown without force: %v", err)
	}
	if err := c.Shutdown(ctx, true); err != nil || ctl.shutdowns != 1 {
		t.Errorf("shutdown force: %v", err)
	}
	// second server on the same socket must refuse
	if _, err := (&Server{Socket: sock, Ctl: ctl}).Listen(); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second daemon must be refused: %v", err)
	}
}

func TestNoDaemon(t *testing.T) {
	c := NewClient(filepath.Join(t.TempDir(), "none.sock"))
	if _, err := c.Status(context.Background()); !errors.Is(err, ErrNoDaemon) {
		t.Errorf("want ErrNoDaemon, got %v", err)
	}
}
