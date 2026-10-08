// Serves the settings page against a throwaway config and a fake daemon, so
// the UI can be looked at in any browser without mounting anything:
//
//	go run ./hack/uipreview            # page files from disk: edit, reload
//	go run ./hack/uipreview -missing   # first run, no settings file yet
//
// The fake mounts cover every state the status card knows; Connect,
// Disconnect and Sync change them like the daemon would.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/eltupe/lagra/internal/app"
	"github.com/eltupe/lagra/internal/engine"
	"github.com/eltupe/lagra/internal/ipc"
	"github.com/eltupe/lagra/internal/rclone"
	"github.com/eltupe/lagra/internal/secrets"
	"github.com/eltupe/lagra/internal/settings"
	"github.com/eltupe/lagra/internal/webui"
)

const sample = `[general]
launch_at_login = true

[defaults]
hide_apple_files = true

[[account]]
name = "hetzner"
[account.options]
endpoint = "nbg1.your-objectstorage.com"
provider = "Hetzner"
region = "nbg1"
[account.secrets]
access_key_id = { keychain = true }
secret_access_key = { keychain = true }

[[account]]
name = "digitalocean"
[account.options]
endpoint = "fra1.digitaloceanspaces.com"
provider = "DigitalOcean"
region = "fra1"
[account.secrets]
access_key_id = { keychain = true }
secret_access_key = { keychain = true }

[[account]]
name = "alice"
public = ["host", "user"]
[account.options]
user = "alice"
[account.secrets]
pass = { keychain = true }

[[mount]]
name = "projects"
account = "hetzner"
type = "s3"
path = "projects"

[[mount]]
name = "backups"
account = "hetzner"
type = "s3"
path = "backups"

[[mount]]
name = "do-backups"
account = "digitalocean"
type = "s3"
path = "backups"

[[mount]]
name = "photos"
account = "alice"
type = "sftp"
path = "/volume1/photos"
read_only = true
[mount.options]
host = "nas.local"
`

func main() {
	missing := flag.Bool("missing", false, "start without a settings file (first run)")
	embedded := flag.Bool("embedded", false, "serve the page files built into the binary instead of internal/webui/static")
	flag.Parse()

	dir, err := os.MkdirTemp("", "lagra-uipreview-")
	check(err)
	defer os.RemoveAll(dir)
	cfg := filepath.Join(dir, "config.toml")
	if !*missing {
		check(os.WriteFile(cfg, []byte(sample), 0o644))
	}
	logPath := filepath.Join(dir, "lagra.log")
	check(os.WriteFile(logPath, []byte(sampleLog()), 0o644))

	home, _ := os.UserHomeDir()
	ctl := newFakeCtl(home)
	reg := rclone.Registry{}
	deps := webui.Deps{
		Settings: &settings.Service{Path: cfg, Registry: reg, Keychain: &fakeKeychain{items: map[string]string{
			"hetzner/access_key_id": "x", "hetzner/secret_access_key": "x", "digitalocean/access_key_id": "x", "nas/pass": "x",
		}}},
		Ctl: ctl, Registry: reg, LogPath: logPath, Version: "preview", Backends: app.Backends,
	}
	if !*embedded {
		if _, err := os.Stat("internal/webui/static/index.html"); err != nil {
			check(fmt.Errorf("run from the repository root, or pass -embedded"))
		}
		deps.Files = os.DirFS("internal/webui/static")
	}
	srv, err := webui.New(deps)
	check(err)
	fmt.Println(srv.URL())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	check(srv.Serve(ctx))
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "uipreview:", err)
		os.Exit(1)
	}
}

func sampleLog() string {
	t := time.Now().Add(-10 * time.Minute)
	var b strings.Builder
	for i, l := range []string{
		`level=INFO msg="lagra started" version=preview config=~/.config/lagra/config.toml`,
		`level=INFO msg=connecting mount=projects`,
		`level=INFO msg=connected mount=projects mount_point=~/Volumes/projects`,
		`level=INFO msg=connecting mount=backups`,
		`level=ERROR msg="connect failed" mount=backups err="bucket \"backups\" does not exist: NoSuchBucket: The specified bucket does not exist."`,
		`level=WARN msg="storage unreachable" mount=do-backups err="dial tcp: lookup fra1.digitaloceanspaces.com: no such host"`,
	} {
		fmt.Fprintf(&b, "time=%s %s\n", t.Add(time.Duration(i)*time.Second).Format(time.RFC3339), l)
	}
	return b.String()
}

// fakeCtl plays the daemon: fixed states to start with, and Connect,
// Disconnect and Sync move them along like the real state machine.
type fakeCtl struct {
	mu     sync.Mutex
	mounts []engine.Snapshot
}

func newFakeCtl(home string) *fakeCtl {
	vol := func(n string) string { return filepath.Join(home, "Volumes", n) }
	return &fakeCtl{mounts: []engine.Snapshot{
		{Name: "projects", State: engine.Connected, Enabled: true, MountPoint: vol("projects"), Uploads: 2},
		{Name: "backups", State: engine.Failed, Enabled: true, MountPoint: vol("backups"),
			LastError: `mount backups: bucket "backups" does not exist: NoSuchBucket: The specified bucket does not exist. status code: 404, request id: tx00000a1b2c3d4e5f6-0067a1b2c3-1a2b3c4-nbg1, host id: 1a2b3c4-nbg1-nbg1`},
		{Name: "do-backups", State: engine.Offline, Enabled: true, MountPoint: vol("do-backups"),
			LastError: `Get "https://fra1.digitaloceanspaces.com/backups?delimiter=%2F&max-keys=1": dial tcp: lookup fra1.digitaloceanspaces.com: no such host`},
		{Name: "photos", State: engine.Disconnected, Enabled: true, Manual: true, MountPoint: vol("photos")},
	}}
}

func (f *fakeCtl) Status() ipc.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ipc.Status{Version: "preview", ConfigOK: true, Overall: engine.OverallOK, Mounts: append([]engine.Snapshot(nil), f.mounts...)}
}

func (f *fakeCtl) set(name string, fn func(*engine.Snapshot)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.mounts {
		if f.mounts[i].Name == name {
			fn(&f.mounts[i])
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ipc.ErrNoMount, name)
}

func (f *fakeCtl) Connect(name string) error {
	err := f.set(name, func(s *engine.Snapshot) { s.State, s.Manual, s.LastError = engine.Connecting, false, "" })
	time.AfterFunc(1500*time.Millisecond, func() {
		_ = f.set(name, func(s *engine.Snapshot) {
			if s.State == engine.Connecting {
				s.State = engine.Connected
			}
		})
	})
	return err
}

func (f *fakeCtl) Disconnect(name string, force bool) error {
	return f.set(name, func(s *engine.Snapshot) { s.State, s.Manual, s.Uploads = engine.Disconnected, true, 0 })
}

func (f *fakeCtl) Sync(name string) (ipc.SyncResult, error) {
	var n int
	err := f.set(name, func(s *engine.Snapshot) { n, s.Uploads = s.Uploads, 0 })
	return ipc.SyncResult{Expedited: n}, err
}

func (f *fakeCtl) RestartAccount(string) []string { return nil }
func (f *fakeCtl) Reload() ipc.ReloadResult       { return ipc.ReloadResult{OK: true} }
func (f *fakeCtl) Shutdown(bool) error            { return nil }

// fakeKeychain keeps secrets in memory; nothing reaches the real keychain.
type fakeKeychain struct {
	mu    sync.Mutex
	items map[string]string
}

func (k *fakeKeychain) Get(_ context.Context, _, account string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.items[account]
	if !ok {
		return "", secrets.ErrNotFound
	}
	return v, nil
}

func (k *fakeKeychain) Exists(_ context.Context, _, account string) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.items[account]
	return ok, nil
}

func (k *fakeKeychain) Set(_ context.Context, _, account, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.items[account] = value
	return nil
}

func (k *fakeKeychain) Delete(_ context.Context, _, account string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.items, account)
	return nil
}
