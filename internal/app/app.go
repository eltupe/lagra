// Package app wires everything into the running daemon: config loading and
// hot reload, the engine, wake/network watchers, the IPC server, the
// LaunchAgent and logging. Both `lagra run` (headless) and `lagra app` (menu
// bar) are built on it.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/eltupe/lagra/internal/config"
	"github.com/eltupe/lagra/internal/engine"
	"github.com/eltupe/lagra/internal/ipc"
	"github.com/eltupe/lagra/internal/launchagent"
	"github.com/eltupe/lagra/internal/logging"
	"github.com/eltupe/lagra/internal/notify"
	"github.com/eltupe/lagra/internal/rclone"
	"github.com/eltupe/lagra/internal/reload"
	"github.com/eltupe/lagra/internal/secrets"
	"github.com/eltupe/lagra/internal/settings"
	"github.com/eltupe/lagra/internal/state"
	"github.com/eltupe/lagra/internal/sysmount"
	"github.com/eltupe/lagra/internal/webui"
)

// Options configure the daemon.
type Options struct {
	ConfigFlag string
	Version    string
	// Only restricts the daemon to one mount and connects it even when
	// enabled = false (used by `lagra mount <name>` without a daemon).
	Only string
	// LogToStderr mirrors logs to stderr in addition to the log file.
	LogToStderr bool
	// PickDirectory shows a native folder chooser for the settings page.
	PickDirectory func(start string) (string, bool)
}

// App is the daemon.
type App struct {
	opts      Options
	cfgPath   string
	log       *slog.Logger
	logFile   *logging.Rotating
	mgr       *engine.Manager
	reg       config.Registry
	agent     *launchagent.Agent
	socket    string
	startedAt time.Time
	notifier  engine.Notifier
	settings  *settings.Service
	web       *webui.Server
	logPath   string

	mu        sync.Mutex
	current   *config.Effective
	cfgErr    string
	loadedAt  time.Time
	cacheDir  string
	listeners []func()

	shutdown     chan struct{}
	shutdownOnce sync.Once
}

// New prepares the daemon without starting anything.
func New(opts Options) (*App, error) {
	cfgPath, err := config.Locate(opts.ConfigFlag)
	if err != nil {
		return nil, err
	}
	logDir, err := logging.DefaultDir()
	if err != nil {
		return nil, err
	}
	lf := &logging.Rotating{Path: filepath.Join(logDir, "lagra.log"), MaxBytes: 10 << 20, Keep: 5}
	var w io.Writer = lf
	if opts.LogToStderr {
		w = io.MultiWriter(lf, os.Stderr)
	}
	log := logging.New(w)
	rclone.RouteLogs(log.Handler())

	statePath, err := state.DefaultPath()
	if err != nil {
		return nil, err
	}
	sock, err := ipc.DefaultSocket()
	if err != nil {
		return nil, err
	}
	notifier := notify.OSA{Log: log}
	a := &App{
		opts:      opts,
		cfgPath:   cfgPath,
		log:       log,
		logFile:   lf,
		logPath:   lf.Path,
		reg:       rclone.Registry{},
		socket:    sock,
		startedAt: time.Now(),
		notifier:  notifier,
		shutdown:  make(chan struct{}),
	}
	a.settings = &settings.Service{Path: cfgPath, Registry: a.reg, Keychain: secrets.SecurityKeychain{}}
	a.settings.OnSecretsChanged = func(account string) {
		for _, name := range a.RestartAccount(account) {
			a.log.Info("secrets changed; reconnecting", "account", account, "mount", name)
		}
	}
	web, err := webui.New(webui.Deps{
		Settings: a.settings, Ctl: a, Registry: a.reg, Backends: Backends,
		LogPath: lf.Path, Version: opts.Version, PickDirectory: opts.PickDirectory, Log: log,
	})
	if err != nil {
		return nil, fmt.Errorf("settings server: %w", err)
	}
	a.web = web
	if exe, err := os.Executable(); err == nil {
		a.agent, _ = launchagent.New(exe, []string{"app"}, logDir)
	}
	deps := engine.Deps{
		Starter:  rclone.InProcess{},
		Sys:      &sysmount.Darwin{},
		Secrets:  &secrets.Resolver{Keychain: secrets.SecurityKeychain{}},
		State:    &state.Store{Path: statePath},
		Notifier: notifier,
		Log:      log,
	}
	a.mgr = engine.NewManager(deps)
	return a, nil
}

// Backends lists rclone's backend types for the settings page.
func Backends() []webui.Backend {
	var out []webui.Backend
	for _, b := range rclone.Backends() {
		out = append(out, webui.Backend{Name: b.Name, Description: b.Description})
	}
	return out
}

// OnChange registers a callback invoked (from any goroutine) whenever the
// status may have changed. The menu bar uses it to refresh.
func (a *App) OnChange(fn func()) {
	a.mu.Lock()
	a.listeners = append(a.listeners, fn)
	a.mu.Unlock()
}

func (a *App) changed() {
	a.mu.Lock()
	ls := append([]func(){}, a.listeners...)
	a.mu.Unlock()
	for _, fn := range ls {
		fn()
	}
}

// Log returns the daemon logger.
func (a *App) Log() *slog.Logger { return a.log }

// LogDir returns the folder of the daemon log.
func (a *App) LogDir() string { return filepath.Dir(a.logPath) }

// Run starts everything and blocks until a shutdown is requested or ctx is
// done. It always tries to unmount cleanly on the way out.
func (a *App) Run(ctx context.Context) error {
	defer a.logFile.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.log.Info("lagra starting", "version", a.opts.Version, "pid", os.Getpid(), "config", a.cfgPath)

	// The socket first: a second daemon must stop before it cleans up or
	// mounts anything the first one owns.
	ctl := &ipc.Server{Socket: a.socket, Ctl: a, Log: a.log}
	ctlLn, err := ctl.Listen()
	if err != nil {
		a.log.Error("control socket", "err", err.Error())
		return err
	}

	if err := a.mgr.CleanOrphans(ctx); err != nil {
		a.log.Warn("orphan cleanup", "err", err.Error())
	}
	a.Reload()

	var wg sync.WaitGroup
	wg.Go(func() {
		w := &reload.Watcher{Path: a.cfgPath, Log: a.log, OnChange: func() {
			r := a.Reload()
			if r.OK {
				a.log.Info("config reloaded", "added", r.Added, "removed", r.Removed, "restart", r.Restart)
			}
		}}
		if err := w.Run(ctx); err != nil {
			a.log.Error("config watcher stopped", "err", err.Error())
		}
	})
	wg.Go(func() {
		engine.WakeWatcher{OnWake: func(gap time.Duration) {
			a.log.Info("wake detected", "gap", gap.String())
			a.mgr.ProbeAll()
		}}.Run(ctx)
	})
	wg.Go(func() {
		engine.NetWatcher{OnChange: func() {
			a.log.Info("network change detected")
			a.mgr.ProbeAll()
		}}.Run(ctx)
	})
	wg.Go(func() {
		if err := ctl.Serve(ctx, ctlLn); err != nil {
			a.log.Error("control socket", "err", err.Error())
		}
	})
	if a.web != nil {
		wg.Go(func() {
			if err := a.web.Serve(ctx); err != nil {
				a.log.Error("settings server", "err", err.Error())
			}
		})
	}
	wg.Go(func() { a.statusTicker(ctx) })

	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-a.shutdown:
			break loop
		case s := <-sigs:
			if s == syscall.SIGHUP {
				a.log.Info("SIGHUP: reloading config")
				a.Reload()
				continue
			}
			a.log.Info("signal received, shutting down", "signal", s.String())
			break loop
		}
	}
	cancel()
	// Pending uploads were checked when quit was asked for; from here on
	// every mount goes, by force after a clean attempt.
	sctx, scancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer scancel()
	a.mgr.Shutdown(sctx)
	wg.Wait()
	a.log.Info("lagra stopped")
	return nil
}

// statusTicker wakes menu listeners regularly so counters stay fresh.
func (a *App) statusTicker(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.changed()
		}
	}
}

// ---- ipc.Controller ----

// Status implements ipc.Controller.
func (a *App) Status() ipc.Status {
	a.mu.Lock()
	cur, cfgErr, loaded := a.current, a.cfgErr, a.loadedAt
	a.mu.Unlock()
	real, _ := config.Resolve(a.cfgPath)
	snaps := a.mgr.Snapshots()
	return ipc.Status{
		SettingsURL:   a.SettingsURL(),
		Version:       a.opts.Version,
		PID:           os.Getpid(),
		StartedAt:     a.startedAt,
		ConfigPath:    a.cfgPath,
		ConfigReal:    real,
		ConfigOK:      cur != nil && cfgErr == "",
		ConfigError:   cfgErr,
		ConfigMissing: a.configMissing(),
		LoadedAt:      loaded,
		Overall:       a.overall(snaps, cfgErr),
		Mounts:        snaps,
	}
}

func (a *App) overall(snaps []engine.Snapshot, cfgErr string) engine.Overall {
	if cfgErr != "" {
		return engine.OverallError
	}
	return engine.Summarize(snaps)
}

// Connect implements ipc.Controller.
func (a *App) Connect(name string) error {
	c, ok := a.mgr.Get(name)
	if !ok {
		return fmt.Errorf("%w: %s", ipc.ErrNoMount, name)
	}
	if !c.Snapshot().Enabled {
		return fmt.Errorf("mount %q is disabled in the config (enabled = false); enable it there", name)
	}
	c.Connect(true)
	a.changed()
	return nil
}

// Disconnect implements ipc.Controller.
func (a *App) Disconnect(name string, force bool) error {
	c, ok := a.mgr.Get(name)
	if !ok {
		return fmt.Errorf("%w: %s", ipc.ErrNoMount, name)
	}
	c.Disconnect(true, force)
	a.changed()
	return nil
}

// Sync implements ipc.Controller.
func (a *App) Sync(ctx context.Context, name string) (ipc.SyncResult, error) {
	c, ok := a.mgr.Get(name)
	if !ok {
		return ipc.SyncResult{}, fmt.Errorf("%w: %s", ipc.ErrNoMount, name)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	n, err := c.Sync(ctx)
	if err != nil {
		return ipc.SyncResult{}, err
	}
	a.changed()
	return ipc.SyncResult{Expedited: n}, nil
}

// RestartAccount implements ipc.Controller: every mount of account that is
// enabled and not disconnected by hand reconnects, so new secrets take
// effect. A disabled or disconnected mount reads them on its next connect.
func (a *App) RestartAccount(account string) []string {
	a.mu.Lock()
	cur := a.current
	a.mu.Unlock()
	if cur == nil {
		return nil
	}
	var done []string
	for _, m := range cur.Mounts {
		if m.Account != account {
			continue
		}
		if c, ok := a.mgr.Get(m.Name); ok {
			if s := c.Snapshot(); s.Enabled && !s.Manual {
				c.Restart()
				done = append(done, m.Name)
			}
		}
	}
	if len(done) > 0 {
		a.changed()
	}
	return done
}

// Reload implements ipc.Controller: parse, validate, then apply. An invalid
// file leaves the running configuration untouched.
func (a *App) Reload() ipc.ReloadResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reloadLocked()
}

func (a *App) reloadLocked() ipc.ReloadResult {
	doc, err := config.ReadFile(a.cfgPath)
	if err != nil {
		return a.reloadFailed(ipc.ReloadResult{}, err.Error())
	}
	env, err := config.OSEnv()
	if err != nil {
		return a.reloadFailed(ipc.ReloadResult{}, err.Error())
	}
	eff, issues := config.Validate(doc, a.reg, env)
	var res ipc.ReloadResult
	for _, is := range issues {
		if is.Warning {
			res.Warnings = append(res.Warnings, is.Error())
		} else {
			res.Issues = append(res.Issues, is.Error())
		}
	}
	if issues.HasErrors() {
		return a.reloadFailed(res, issues.Error())
	}
	for _, w := range res.Warnings {
		a.log.Warn("config warning", "warning", w)
	}
	if a.opts.Only != "" {
		eff = restrictTo(eff, a.opts.Only)
		if eff == nil {
			return a.reloadFailed(res, fmt.Sprintf("mount %q is not defined in %s", a.opts.Only, a.cfgPath))
		}
	}
	a.applyGeneral(eff.General)
	diff := a.mgr.Apply(eff)
	res.OK = true
	res.Added, res.Removed, res.Restart = diff.Added, diff.Removed, diff.Restart
	a.current, a.cfgErr, a.loadedAt = eff, "", time.Now()
	go a.changed()
	return res
}

// reloadFailed records a rejected config; res keeps issues and warnings
// already sorted out, msg is the whole story for the log.
func (a *App) reloadFailed(res ipc.ReloadResult, msg string) ipc.ReloadResult {
	missing := a.configMissing()
	if missing {
		msg = "no config file yet at " + a.cfgPath
		a.log.Info("no config file yet; open Settings to create one", "path", a.cfgPath)
	} else {
		a.log.Error("config rejected; keeping the running configuration", "err", msg)
	}
	if a.cfgErr != msg && !missing {
		a.notifier.Notify("Lagra: config error", firstLine(msg))
	}
	a.cfgErr = msg
	go a.changed()
	res.OK = false
	if len(res.Issues) == 0 {
		res.Issues = strings.Split(msg, "\n")
	}
	return res
}

// configMissing reports whether the config file (or its symlink target) is absent.
func (a *App) configMissing() bool {
	_, err := os.Stat(a.cfgPath)
	return errors.Is(err, os.ErrNotExist)
}

// SettingsURL returns the local settings page address (with token), or "".
func (a *App) SettingsURL() string { return a.web.URL() }

// applyGeneral applies the live [general] settings.
func (a *App) applyGeneral(g config.EffectiveGeneral) {
	logging.LevelVar.Set(logging.Level(g.LogLevel))
	if a.cacheDir == "" {
		a.cacheDir = g.CacheDir
		if err := rclone.SetCacheDir(filepath.Join(g.CacheDir, "vfs")); err != nil {
			a.log.Warn("cache dir", "err", err.Error())
		}
	} else if a.cacheDir != g.CacheDir {
		a.log.Warn("cache_dir changed; restart lagra for it to take effect", "old", a.cacheDir, "new", g.CacheDir)
	}
	a.mgr.Configure(func(d *engine.Deps) {
		d.HandleCacheDir = filepath.Join(a.cacheDir, "nfs-handles")
		d.UploadStuck = g.UploadStuckAfter
	})
	if a.agent != nil && a.opts.Only == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var err error
		if g.LaunchAtLogin {
			err = a.agent.Enable(ctx)
		} else {
			err = a.agent.Disable(ctx)
		}
		if err != nil {
			a.log.Warn("launch agent", "err", err.Error())
		}
	}
}

// Shutdown implements ipc.Controller.
func (a *App) Shutdown(force bool) error {
	if !force {
		if n := a.mgr.PendingUploads(); n > 0 {
			return fmt.Errorf("%w: %d file(s) still uploading", ipc.ErrPendingUploads, n)
		}
	}
	a.shutdownOnce.Do(func() { close(a.shutdown) })
	return nil
}

func restrictTo(eff *config.Effective, name string) *config.Effective {
	m, ok := eff.Mount(name)
	if !ok {
		return nil
	}
	m.Enabled = true
	return &config.Effective{General: eff.General, Mounts: []config.EffectiveMount{m}}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
