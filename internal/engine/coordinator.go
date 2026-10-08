package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eltupe/lagra/internal/config"
	"github.com/eltupe/lagra/internal/rclone"
	"github.com/eltupe/lagra/internal/state"
	"github.com/eltupe/lagra/internal/sysmount"
)

// Coordinator runs one mount. All fields below the mutex line are owned by
// the loop goroutine; the outside world talks to it through methods that
// post closures onto the loop.
type Coordinator struct {
	deps Deps
	do   chan func()
	quit chan struct{} // closed when the loop has exited

	mu   sync.RWMutex
	snap Snapshot

	// loop-owned
	def       config.EffectiveMount
	inst      rclone.Instance
	opGen     int // counts started operations; stale retry timers check it
	inFlight  bool
	want      *desired // what to do once the current operation finishes
	manual    bool
	attempt   int
	retry     *time.Timer
	mountTick *time.Ticker
	backTick  *time.Ticker
	uploadsAt time.Time // when uploads first became > 0 in this streak
	stuckSent bool
	busySince time.Time // when the volume stopped answering while its server still does
	capSent   bool
	removed   bool
	onRemoved func()
}

// desired is a deferred intent.
type desired struct {
	action string // "connect", "disconnect", "restart", "remove", "fail"
	manual bool
	force  bool
	def    *config.EffectiveMount
	err    error // why, for "fail"
}

// NewCoordinator creates and starts the coordinator for def. It does not
// connect; the Manager decides that based on def.Enabled.
func NewCoordinator(deps Deps, def config.EffectiveMount, onRemoved func()) *Coordinator {
	deps.defaults()
	c := &Coordinator{
		deps:      deps,
		do:        make(chan func(), 64),
		quit:      make(chan struct{}),
		def:       def,
		onRemoved: onRemoved,
	}
	c.snap = Snapshot{Name: def.Name, State: Disconnected, Since: time.Now(), Enabled: def.Enabled, MountPoint: def.MountPoint}
	c.mountTick = time.NewTicker(def.MountProbeInterval)
	c.backTick = time.NewTicker(def.BackendProbeInterval)
	go c.loop()
	return c
}

// Snapshot returns the current view.
func (c *Coordinator) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snap
}

// Connect asks the mount to connect. manual clears a manual disconnect.
func (c *Coordinator) Connect(manual bool) {
	c.post(func() { c.request(desired{action: "connect", manual: manual}) })
}

// Disconnect asks the mount to disconnect. A manual disconnect is not undone
// by config reloads that leave the mount unchanged. force ignores pending
// uploads and busy mount points.
func (c *Coordinator) Disconnect(manual, force bool) {
	c.post(func() { c.request(desired{action: "disconnect", manual: manual, force: force}) })
}

// Update replaces the definition. When only live fields changed (probe
// intervals, the enabled flag) the mount stays as it is; anything else
// reconnects with the new definition, or, for a disabled mount, takes it
// over once nothing is mounted any more.
func (c *Coordinator) Update(def config.EffectiveMount) {
	c.post(func() {
		if config.SameDefinition(c.def, def) {
			c.applyLive(def)
			return
		}
		d := def
		action := "restart"
		if !def.Enabled {
			action = "disconnect"
		}
		c.request(desired{action: action, def: &d})
	})
}

// configure takes over thresholds that apply to running mounts.
func (c *Coordinator) configure(uploadStuck time.Duration) {
	c.post(func() { c.deps.UploadStuck = uploadStuck })
}

// Restart reconnects with the current definition, re-resolving secrets.
// It behaves like a restart change from a reload.
func (c *Coordinator) Restart() {
	c.post(func() {
		d := c.def
		c.request(desired{action: "restart", def: &d})
	})
}

// Sync expedites pending uploads and refreshes the directory cache, then
// probes the backend. It reports how many uploads were expedited.
func (c *Coordinator) Sync(ctx context.Context) (int, error) {
	type res struct {
		n   int
		err error
	}
	ch := make(chan res, 1)
	if !c.post(func() {
		inst := c.inst
		st := c.Snapshot().State
		if inst == nil || (st != Connected && st != Offline) {
			ch <- res{0, fmt.Errorf("mount %q is %s", c.def.Name, st)}
			return
		}
		go func() {
			n, err := inst.Sync(ctx)
			ch <- res{n, err}
			c.post(func() { c.onBackendTick(); c.onMountTick() })
		}()
	}) {
		return 0, errors.New("coordinator stopped")
	}
	select {
	case r := <-ch:
		return r.n, r.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Remove disconnects and shuts the coordinator down. Pending upload rules
// apply unless force is set.
func (c *Coordinator) Remove(force bool) {
	c.post(func() { c.request(desired{action: "remove", force: force}) })
}

// ProbeNow runs both probes immediately (after wake or a network change).
func (c *Coordinator) ProbeNow() {
	c.post(func() {
		c.onMountTick()
		c.onBackendTick()
	})
}

// Shutdown disconnects (force after a normal attempt) and stops the loop.
func (c *Coordinator) Shutdown(ctx context.Context) {
	done := make(chan struct{})
	c.post(func() {
		c.onRemoved = func() { close(done) }
		c.request(desired{action: "remove", force: true})
	})
	select {
	case <-done:
	case <-ctx.Done():
	case <-c.quit:
	}
}

func (c *Coordinator) post(f func()) bool {
	select {
	case c.do <- f:
		return true
	case <-c.quit:
		return false
	}
}

func (c *Coordinator) loop() {
	defer close(c.quit)
	defer c.mountTick.Stop()
	defer c.backTick.Stop()
	for {
		select {
		case f := <-c.do:
			f()
			if c.removed && !c.inFlight {
				if c.onRemoved != nil {
					c.onRemoved()
				}
				return
			}
		case <-c.mountTick.C:
			c.onMountTick()
		case <-c.backTick.C:
			c.onBackendTick()
		}
	}
}

// ---- snapshot helpers (loop-owned writes) ----

func (c *Coordinator) update(fn func(s *Snapshot)) {
	c.mu.Lock()
	fn(&c.snap)
	c.mu.Unlock()
}

func (c *Coordinator) setState(st State, err error) {
	c.update(func(s *Snapshot) {
		if s.State != st {
			s.Since = time.Now()
		}
		s.State = st
		s.Manual = c.manual && st == Disconnected
		if err != nil {
			s.LastError = err.Error()
		} else if st == Connected {
			s.LastError = ""
		}
		if st != Broken {
			s.Attempt = 0
			s.NextRetry = time.Time{}
		}
	})
	c.deps.Log.Info("mount state", "mount", c.def.Name, "state", string(st), "err", errString(err))
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---- requests ----

// request records the newest intent and executes it if nothing is in flight.
// A pending removal is final: later requests can only make it forced. A
// pending restart already reconnects, so a connect adds nothing, and a new
// definition that is still waiting carries over to the next intent.
func (c *Coordinator) request(d desired) {
	if c.removed {
		return
	}
	if d.action == "fail" && c.want != nil {
		// Whatever is pending tears the mount down anyway, and a pending
		// restart may bring the credentials that fix it.
		return
	}
	if w := c.want; w != nil {
		switch {
		case w.action == "remove" && d.action != "remove":
			w.force = w.force || d.force
			c.drain()
			return
		case w.action == "restart" && d.action == "connect":
			return
		}
		if d.def == nil {
			d.def = w.def
		}
	}
	c.want = &d
	c.drain()
}

// drain executes the desired action if possible; otherwise it stays pending
// and is re-evaluated on every tick and after every operation.
func (c *Coordinator) drain() {
	if c.inFlight || c.want == nil {
		return
	}
	d := *c.want
	st := c.Snapshot().State
	switch d.action {
	case "connect":
		c.want = nil
		c.clearPending()
		if d.manual {
			c.manual = false
		}
		if c.manual || !c.def.Enabled {
			c.setState(Disconnected, nil)
			return
		}
		switch st {
		case Connected, Offline, Connecting:
			return
		}
		c.stopRetry()
		c.attempt = 0
		if st == Broken {
			// Whatever is left of the hung volume goes first.
			c.startRepair()
			return
		}
		c.startConnect()
	case "disconnect", "restart", "remove", "fail":
		if d.manual {
			c.manual = true
		}
		if st == Disconnected || st == Failed {
			c.finishDesired(d)
			return
		}
		if st == Broken {
			// Nothing mounted that works; tear down what is left.
			c.stopRetry()
			c.startTeardown(d, tdForceOnly)
			return
		}
		if !d.force {
			if n := c.uploads(); n > 0 {
				c.setPending(d.action, "uploads pending")
				return
			}
		}
		mode := tdNormal
		if d.force {
			mode = tdForceFallback
		}
		c.startTeardown(d, mode)
	}
}

// finishDesired completes a disconnect-family action once nothing is mounted.
func (c *Coordinator) finishDesired(d desired) {
	c.want = nil
	c.clearPending()
	if d.def != nil {
		c.retime(*d.def)
		c.def = *d.def
		c.update(func(s *Snapshot) { s.Enabled = c.def.Enabled; s.MountPoint = c.def.MountPoint })
	}
	switch d.action {
	case "disconnect":
		c.setState(Disconnected, nil)
	case "restart":
		c.manual = false
		c.attempt = 0
		if c.def.Enabled {
			c.startConnect()
		} else {
			c.setState(Disconnected, nil)
		}
	case "remove":
		c.setState(Disconnected, nil)
		c.removed = true
	case "fail":
		c.setState(Failed, d.err)
		c.deps.Notifier.Notify("Lagra: "+c.def.Name+" failed", d.err.Error())
	}
}

// loginRejected stops a mount whose storage refused the login: every
// further request would be another failed login, and directory services
// lock accounts after a few (see rclone.LoginRejectedError). It ends in
// Failed, which new credentials or a manual connect leave.
func (c *Coordinator) loginRejected(err error) {
	c.deps.Log.Warn("login rejected; stopping the mount", "mount", c.def.Name, "err", err.Error())
	c.request(desired{action: "fail", force: true, err: err})
}

func isLoginRejected(err error) bool {
	var lr *rclone.LoginRejectedError
	return errors.As(err, &lr)
}

// retime resets the probe tickers whose interval def changes.
func (c *Coordinator) retime(def config.EffectiveMount) {
	if def.MountProbeInterval != c.def.MountProbeInterval {
		c.mountTick.Reset(def.MountProbeInterval)
	}
	if def.BackendProbeInterval != c.def.BackendProbeInterval {
		c.backTick.Reset(def.BackendProbeInterval)
	}
}

func (c *Coordinator) applyLive(def config.EffectiveMount) {
	c.retime(def)
	enabledChanged := def.Enabled != c.def.Enabled
	c.def.MountProbeInterval = def.MountProbeInterval
	c.def.BackendProbeInterval = def.BackendProbeInterval
	c.def.Enabled = def.Enabled
	c.update(func(s *Snapshot) { s.Enabled = def.Enabled; s.MountPoint = def.MountPoint })
	if enabledChanged {
		if def.Enabled {
			c.request(desired{action: "connect"})
		} else {
			c.request(desired{action: "disconnect"})
		}
	}
}

func (c *Coordinator) setPending(action, reason string) {
	c.update(func(s *Snapshot) {
		if s.Pending == nil || s.Pending.Action != action || s.Pending.Reason != reason {
			s.Pending = &PendingChange{Action: action, Reason: reason, Since: time.Now()}
		}
	})
}

func (c *Coordinator) clearPending() {
	c.update(func(s *Snapshot) { s.Pending = nil })
}

// ---- connect ----

type connectResult struct {
	inst    rclone.Instance
	mounted bool
	err     error
	final   bool // Failed rather than Broken
	offline bool
}

func (c *Coordinator) startConnect() {
	if c.inFlight {
		return
	}
	c.inFlight = true
	c.opGen++
	c.setState(Connecting, nil)
	def := c.def
	go func() {
		res := c.connectOp(context.Background(), def)
		c.post(func() {
			c.inFlight = false
			c.onConnectResult(res)
		})
	}()
}

func (c *Coordinator) connectOp(ctx context.Context, def config.EffectiveMount) connectResult {
	d := &c.deps
	// 1. secrets
	sctx, cancel := context.WithTimeout(ctx, d.SecretTimeout)
	secretVals, err := d.Secrets.Resolve(sctx, def.Account, def.Secrets)
	cancel()
	if err != nil {
		return connectResult{err: err, final: true}
	}
	opts := maps.Clone(def.Options)
	if opts == nil {
		opts = map[string]string{}
	}
	maps.Copy(opts, secretVals)
	// 2. mount point
	if err := d.Sys.EnsureMountPoint(def.MountPoint); err != nil {
		if errors.Is(err, sysmount.ErrBusy) {
			// Our own leftover from a previous life; the server is gone.
			uctx, ucancel := context.WithTimeout(ctx, d.UnmountTimeout)
			uerr := d.Sys.Unmount(uctx, def.MountPoint, true)
			ucancel()
			if uerr != nil && !errors.Is(uerr, sysmount.ErrNotMounted) {
				return connectResult{err: fmt.Errorf("clean up leftover mount: %w", uerr)}
			}
		} else {
			return connectResult{err: err, final: errors.Is(err, sysmount.ErrForeign)}
		}
	}
	// 3. server
	sctx, cancel = context.WithTimeout(ctx, d.StartTimeout)
	inst, err := d.Starter.Start(sctx, rclone.Spec{
		Name:           def.Name,
		Type:           def.Type,
		Path:           def.Path,
		Options:        opts,
		VFS:            def.VFS,
		ReadOnly:       def.ReadOnly,
		HideAppleFiles: def.HideAppleFiles,
		HandleCacheDir: filepath.Join(d.HandleCacheDir, def.Name),
	})
	cancel()
	if err != nil {
		return connectResult{err: err, final: !isTransient(err)}
	}
	// 4. mount
	mctx, cancel := context.WithTimeout(ctx, d.MountTimeout)
	err = d.Sys.Mount(mctx, inst.Port(), def.MountPoint, def.ReadOnly)
	cancel()
	if err != nil {
		return connectResult{inst: inst, err: fmt.Errorf("mount: %w", err)}
	}
	// 5. remember as managed
	if d.State != nil {
		_ = d.State.Update(func(f *state.File) {
			f.Mounts[def.Name] = state.Managed{Name: def.Name, MountPoint: def.MountPoint, Port: inst.Port(), PID: os.Getpid(), StartedAt: time.Now()}
		})
	}
	// 6. first backend probe
	pctx, cancel := context.WithTimeout(ctx, d.BackendTimeout)
	perr := inst.Probe(pctx)
	cancel()
	return connectResult{inst: inst, mounted: true, offline: perr != nil, err: perr}
}

func (c *Coordinator) onConnectResult(res connectResult) {
	if res.err != nil && !res.mounted {
		if res.inst != nil {
			inst := res.inst
			go func() { _ = inst.Stop(0) }()
		}
		if res.final {
			c.setState(Failed, res.err)
			c.deps.Notifier.Notify("Lagra: "+c.def.Name+" failed", res.err.Error())
		} else {
			c.setBroken(res.err)
		}
		c.drain()
		return
	}
	c.inst = res.inst
	c.attempt = 0
	c.capSent = false
	c.busySince = time.Time{}
	c.update(func(s *Snapshot) { s.Port = res.inst.Port(); s.MountPoint = c.def.MountPoint })
	if res.offline {
		c.setState(Offline, res.err)
	} else {
		c.setState(Connected, nil)
	}
	if isLoginRejected(res.err) {
		c.loginRejected(res.err)
	}
	c.drain()
}

// isTransient decides whether a server start error is worth retrying. A
// rejected login never is: retrying would only add failed logins.
func isTransient(err error) bool {
	if isLoginRejected(err) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"timeout", "timed out", "connection refused", "no such host", "network is unreachable", "connection reset", "eof", "temporary"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// ---- broken / repair ----

func (c *Coordinator) setBroken(err error) {
	c.attempt++
	delay := backoff(c.attempt, c.deps.BackoffBase, c.deps.BackoffCap)
	next := time.Now().Add(delay)
	c.update(func(s *Snapshot) {
		if s.State != Broken {
			s.Since = time.Now()
		}
		s.State = Broken
		s.LastError = errString(err)
		s.Attempt = c.attempt
		s.NextRetry = next
		s.Port = 0
	})
	c.deps.Log.Warn("mount broken", "mount", c.def.Name, "attempt", c.attempt, "retry_in", delay.String(), "err", errString(err))
	if delay >= c.deps.BackoffCap && !c.capSent {
		c.capSent = true
		c.deps.Notifier.Notify("Lagra: "+c.def.Name+" keeps failing", fmt.Sprintf("Retrying every %s. Last error: %v", delay, err))
	}
	gen := c.opGen
	c.stopRetry()
	c.retry = time.AfterFunc(delay, func() {
		c.post(func() {
			if c.opGen != gen || c.inFlight || c.removed {
				return
			}
			if c.Snapshot().State != Broken {
				return
			}
			c.startRepair()
		})
	})
}

func (c *Coordinator) stopRetry() {
	if c.retry != nil {
		c.retry.Stop()
		c.retry = nil
	}
}

// startRepair tears down whatever is left and connects again.
func (c *Coordinator) startRepair() {
	if c.inFlight {
		return
	}
	c.inFlight = true
	c.opGen++
	inst := c.inst
	c.inst = nil
	mp := c.def.MountPoint
	go func() {
		_ = c.teardownOp(inst, mp, tdForceOnly)
		c.post(func() {
			c.inFlight = false
			if c.want != nil {
				c.drain()
				return
			}
			c.startConnect()
		})
	}()
}

// ---- teardown (disconnect / restart / remove) ----

func (c *Coordinator) startTeardown(d desired, mode teardownMode) {
	if c.inFlight {
		return
	}
	c.inFlight = true
	c.opGen++
	inst := c.inst
	mp := c.def.MountPoint
	go func() {
		err := c.teardownOp(inst, mp, mode)
		c.post(func() {
			c.inFlight = false
			if err != nil { // ErrBusy: the volume is in use and stays
				c.setPending(d.action, "in use")
				c.deps.Log.Info("unmount deferred", "mount", c.def.Name, "reason", "busy")
				return
			}
			c.inst = nil
			c.update(func(s *Snapshot) { s.Port = 0; s.Uploads = 0 })
			c.finishDesired(d)
		})
	}()
}

// teardownMode says how hard to unmount.
type teardownMode int

const (
	tdNormal        teardownMode = iota // refuse when busy (ErrBusy)
	tdForceFallback                     // try normal, then force
	tdForceOnly                         // the mount hangs: force immediately
)

// teardownOp unmounts according to mode and stops the server. It returns
// ErrBusy when a normal unmount was refused, leaving the mount intact; any
// other unmount failure falls back to force, so no server is left behind.
func (c *Coordinator) teardownOp(inst rclone.Instance, mp string, mode teardownMode) error {
	d := &c.deps
	ctx, cancel := context.WithTimeout(context.Background(), d.UnmountTimeout)
	defer cancel()
	if c.isMounted(mp) {
		var err error
		if mode != tdForceOnly {
			err = d.Sys.Unmount(ctx, mp, false)
		}
		if mode == tdForceOnly || (err != nil && !errors.Is(err, sysmount.ErrNotMounted)) {
			if mode == tdNormal && errors.Is(err, sysmount.ErrBusy) {
				return err
			}
			if ferr := d.Sys.Unmount(ctx, mp, true); ferr != nil && !errors.Is(ferr, sysmount.ErrNotMounted) {
				d.Log.Warn("force unmount failed", "mount", c.def.Name, "err", ferr.Error())
			}
		}
	}
	if inst != nil {
		wait := 5 * time.Second
		if mode != tdNormal {
			wait = 0
		}
		if err := inst.Stop(wait); err != nil {
			d.Log.Debug("server stop", "mount", c.def.Name, "err", err.Error())
		}
	}
	if d.State != nil {
		_ = d.State.Update(func(f *state.File) { delete(f.Mounts, c.def.Name) })
	}
	return nil
}

func (c *Coordinator) isMounted(mp string) bool {
	entries, err := c.deps.Sys.Table()
	if err != nil {
		return true // assume yes; unmount will tell
	}
	return slices.ContainsFunc(entries, func(e sysmount.Entry) bool { return e.MountPoint == mp })
}

// ---- ticks ----

func (c *Coordinator) uploads() int {
	if c.inst == nil {
		return 0
	}
	return c.inst.Uploads()
}

func (c *Coordinator) onMountTick() {
	if c.removed {
		return
	}
	st := c.Snapshot().State
	if c.inFlight || (st != Connected && st != Offline) {
		c.drain()
		return
	}
	// uploads and stuck detection
	n := c.uploads()
	now := time.Now()
	if n > 0 {
		if c.uploadsAt.IsZero() {
			c.uploadsAt = now
		}
		stuck := now.Sub(c.uploadsAt) > c.deps.UploadStuck
		if stuck && !c.stuckSent {
			c.stuckSent = true
			c.deps.Notifier.Notify("Lagra: uploads stuck on "+c.def.Name, fmt.Sprintf("%d file(s) waiting for more than %s", n, c.deps.UploadStuck))
		}
		c.update(func(s *Snapshot) { s.Uploads = n; s.Stuck = stuck })
	} else {
		c.uploadsAt = time.Time{}
		c.stuckSent = false
		c.update(func(s *Snapshot) { s.Uploads = 0; s.Stuck = false })
	}
	// server died?
	if err := c.inst.Err(); err != nil {
		c.setBroken(fmt.Errorf("server stopped: %w", err))
		return
	}
	// still in the mount table? If not while the server is fine, the user
	// ejected the volume in Finder: respect that as a manual disconnect.
	if !c.isMounted(c.def.MountPoint) {
		c.deps.Log.Info("volume was ejected; treating as manual disconnect", "mount", c.def.Name)
		c.request(desired{action: "disconnect", manual: true, force: true})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.deps.ProbeTimeout)
	err := c.deps.Sys.Probe(ctx, c.def.MountPoint)
	cancel()
	c.update(func(s *Snapshot) { s.LastProbe = time.Now() })
	switch {
	case err == nil:
		if !c.busySince.IsZero() {
			c.deps.Log.Info("volume answers again", "mount", c.def.Name, "after", time.Since(c.busySince).Round(time.Second).String())
			c.busySince = time.Time{}
		}
	case errors.Is(err, sysmount.ErrHung) && c.serverAnswers():
		// The server works; the volume is busy with something slow (a large
		// listing, a download). Remounting would cut that off, so lagra waits
		// up to sysmount.Unresponsive.
		if c.busySince.IsZero() {
			c.busySince = time.Now()
			c.deps.Log.Info("volume busy; its server answers, so lagra waits", "mount", c.def.Name)
		}
		if busy := time.Since(c.busySince); busy >= c.deps.HungAfter {
			c.busySince = time.Time{}
			c.setBroken(fmt.Errorf("mount probe: the volume has not answered for %s although its server does", busy.Round(time.Second)))
			return
		}
	default:
		c.busySince = time.Time{}
		c.setBroken(fmt.Errorf("mount probe: %w", err))
		return
	}
	c.drain()
}

// serverAnswers asks the NFS server directly, past the volume's own
// connection.
func (c *Coordinator) serverAnswers() bool {
	ctx, cancel := context.WithTimeout(context.Background(), c.deps.ProbeTimeout)
	defer cancel()
	return c.inst.ProbeServer(ctx) == nil
}

func (c *Coordinator) onBackendTick() {
	if c.removed || c.inFlight || c.inst == nil {
		return
	}
	st := c.Snapshot().State
	if st != Connected && st != Offline {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.deps.BackendTimeout)
	err := c.inst.Probe(ctx)
	cancel()
	switch {
	case isLoginRejected(err):
		c.update(func(s *Snapshot) { s.LastError = err.Error() })
		c.loginRejected(err)
	case err == nil && st == Offline:
		c.setState(Connected, nil)
	case err != nil && st == Connected:
		c.setState(Offline, err)
	case err != nil:
		c.update(func(s *Snapshot) { s.LastError = err.Error() })
	}
}
