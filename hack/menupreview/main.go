// Shows Lagra's menu bar menu against a fake daemon, so the menu can be
// looked at next to a running Lagra without mounting anything:
//
//	go run ./hack/menupreview            # storages in every state
//	go run ./hack/menupreview -missing   # first run, no settings file yet
//	go run ./hack/menupreview -broken    # the settings file has an error
//
// A second anchor appears in the menu bar. Connect, Disconnect and Sync Now
// change the fake storages like the daemon would; Quit Lagra ends the preview.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/eltupe/lagra/internal/engine"
	"github.com/eltupe/lagra/internal/ipc"
	"github.com/eltupe/lagra/internal/menubar"
)

func main() {
	missing := flag.Bool("missing", false, "first run: no settings file yet")
	broken := flag.Bool("broken", false, "the settings file has an error")
	flag.Parse()
	d := newFake()
	switch {
	case *missing:
		d.missing, d.mounts = true, nil
	case *broken:
		d.cfgErr = "line 12: mount \"photos\".type: unknown backend type \"ftpx\""
	}
	if err := menubar.Run(context.Background(), d); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type fake struct {
	mu        sync.Mutex
	mounts    []engine.Snapshot
	missing   bool
	cfgErr    string
	listeners []func()
	quit      chan struct{}
	once      sync.Once
	log       *slog.Logger
}

func newFake() *fake {
	home, _ := os.UserHomeDir()
	vol := func(n string) string { return home + "/Volumes/" + n }
	return &fake{
		quit: make(chan struct{}),
		log:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})),
		mounts: []engine.Snapshot{
			{Name: "projects", State: engine.Connected, Enabled: true, MountPoint: vol("projects"), Uploads: 2},
			{Name: "backups", State: engine.Failed, Enabled: true, MountPoint: vol("backups"),
				LastError: "SMB server rejected the login for CORP\\alice: STATUS_LOGON_FAILURE"},
			{Name: "do-backups", State: engine.Offline, Enabled: true, MountPoint: vol("do-backups"),
				LastError: "dial tcp: lookup files.storage.example.org: no such host"},
			{Name: "photos", State: engine.Disconnected, Enabled: true, Manual: true, MountPoint: vol("photos")},
			{Name: "archive", State: engine.Disconnected, Enabled: false, MountPoint: vol("archive")},
			{Name: "scratch", State: engine.Broken, Enabled: true, MountPoint: vol("scratch"),
				LastError: "mount probe: no answer within 5s", Attempt: 2, NextRetry: time.Now().Add(90 * time.Second)},
			{Name: "team", State: engine.Connected, Enabled: true, MountPoint: vol("team"),
				Pending: &engine.PendingChange{Action: "restart", Reason: "in use"}},
		},
	}
}

func (f *fake) Run(ctx context.Context) error {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-f.quit:
			return nil
		case <-t.C:
			f.changed()
		}
	}
}

func (f *fake) changed() {
	f.mu.Lock()
	ls := append([]func(){}, f.listeners...)
	f.mu.Unlock()
	for _, fn := range ls {
		fn()
	}
}

func (f *fake) Log() *slog.Logger              { return f.log }
func (f *fake) SettingsURL() string            { return "" }
func (f *fake) LogDir() string                 { return os.TempDir() }
func (f *fake) OnChange(fn func())             { f.mu.Lock(); f.listeners = append(f.listeners, fn); f.mu.Unlock() }
func (f *fake) Reload() ipc.ReloadResult       { return ipc.ReloadResult{OK: f.cfgErr == ""} }
func (f *fake) RestartAccount(string) []string { return nil }

func (f *fake) Status() ipc.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	snaps := append([]engine.Snapshot(nil), f.mounts...)
	return ipc.Status{
		Version: "preview", ConfigOK: !f.missing && f.cfgErr == "", ConfigMissing: f.missing,
		ConfigError: f.cfgErr, Overall: engine.Summarize(snaps), Mounts: snaps,
	}
}

func (f *fake) set(name string, fn func(*engine.Snapshot)) error {
	f.mu.Lock()
	for i := range f.mounts {
		if f.mounts[i].Name == name {
			fn(&f.mounts[i])
			f.mu.Unlock()
			f.changed()
			return nil
		}
	}
	f.mu.Unlock()
	return ipc.ErrNoMount
}

func (f *fake) Connect(name string) error {
	err := f.set(name, func(s *engine.Snapshot) { s.State, s.Manual, s.LastError = engine.Connecting, false, "" })
	time.AfterFunc(2*time.Second, func() {
		_ = f.set(name, func(s *engine.Snapshot) {
			if s.State == engine.Connecting {
				s.State = engine.Connected
			}
		})
	})
	return err
}

func (f *fake) Disconnect(name string, _ bool) error {
	return f.set(name, func(s *engine.Snapshot) { s.State, s.Manual, s.Uploads = engine.Disconnected, true, 0 })
}

func (f *fake) Sync(_ context.Context, name string) (ipc.SyncResult, error) {
	var n int
	err := f.set(name, func(s *engine.Snapshot) { n, s.Uploads = s.Uploads, 0 })
	return ipc.SyncResult{Expedited: n}, err
}

func (f *fake) Shutdown(bool) error {
	f.once.Do(func() { close(f.quit) })
	return nil
}
