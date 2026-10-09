// Package engine owns the runtime behaviour of mounts: one coordinator
// goroutine per mount runs the state machine (Disconnected, Connecting,
// Connected, Offline, Broken, Failed), probes, repairs with backoff and
// defers changes that cannot be applied yet (pending changes). The Manager
// applies config diffs to the set of coordinators and cleans up orphans.
//
// Everything that touches the system arrives through Deps so the whole
// package is testable with fakes.
package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/eltupe/lagra/internal/config"
	"github.com/eltupe/lagra/internal/rclone"
	"github.com/eltupe/lagra/internal/state"
	"github.com/eltupe/lagra/internal/sysmount"
)

// State of one mount.
type State string

const (
	// Disconnected: deliberately not mounted, because the mount is disabled
	// in the config or the user disconnected it. A manual disconnect survives
	// reloads that leave the mount unchanged, but not a restart.
	Disconnected State = "disconnected"
	// Connecting: the server is starting or mount_nfs is running.
	Connecting State = "connecting"
	// Connected: the volume is present and the backend answers.
	Connected State = "connected"
	// Offline: the volume is present but the backend does not answer.
	// Nothing is repaired; the volume stays.
	Offline State = "offline"
	// Broken: the volume is missing or hangs. The coordinator stops the
	// server, starts a new one and mounts again, with backoff.
	Broken State = "broken"
	// Failed: a permanent error that retrying cannot fix (missing secret,
	// options the backend rejects, a login the storage refuses, a foreign
	// mount on the mount point). Only a reload, new credentials or a manual
	// connect leave it.
	Failed State = "failed"
)

// SecretResolver resolves an account's secret sources into option values.
type SecretResolver interface {
	Resolve(ctx context.Context, account string, sources map[string]config.SecretSource) (map[string]string, error)
}

// Notifier shows a user-visible notification. Implementations must be cheap
// and never block the caller for long.
type Notifier interface {
	Notify(title, body string)
}

type nopNotifier struct{}

func (nopNotifier) Notify(string, string) {}

// Deps are the engine's dependencies.
type Deps struct {
	Starter        rclone.Starter
	Sys            sysmount.System
	Secrets        SecretResolver
	State          *state.Store
	Notifier       Notifier
	Log            *slog.Logger
	HandleCacheDir string // root for per-mount NFS handle caches
	UploadStuck    time.Duration

	// Timeouts, overridable for tests.
	SecretTimeout  time.Duration
	StartTimeout   time.Duration
	MountTimeout   time.Duration
	ProbeTimeout   time.Duration
	HungAfter      time.Duration // a busy volume whose server answers is hung only after this
	BackendTimeout time.Duration
	UnmountTimeout time.Duration
	BackoffBase    time.Duration
	BackoffCap     time.Duration
}

func (d *Deps) defaults() {
	if d.Notifier == nil {
		d.Notifier = nopNotifier{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	set := func(p *time.Duration, v time.Duration) {
		if *p == 0 {
			*p = v
		}
	}
	set(&d.SecretTimeout, 30*time.Second)
	set(&d.StartTimeout, 60*time.Second)
	set(&d.MountTimeout, 30*time.Second)
	set(&d.ProbeTimeout, sysmount.ProbeTimeout)
	set(&d.HungAfter, sysmount.Unresponsive)
	set(&d.BackendTimeout, 15*time.Second)
	set(&d.UnmountTimeout, 30*time.Second)
	set(&d.BackoffBase, 5*time.Second)
	set(&d.BackoffCap, 5*time.Minute)
	set(&d.UploadStuck, config.DefaultUploadStuckAfter)
}

// PendingChange is a change lagra accepted but deliberately has not applied.
type PendingChange struct {
	Action string    `json:"action"` // disconnect, restart, remove
	Reason string    `json:"reason"` // "uploads pending", "in use"
	Since  time.Time `json:"since"`
}

// Snapshot is the externally visible view of one mount.
type Snapshot struct {
	Name       string         `json:"name"`
	State      State          `json:"state"`
	Since      time.Time      `json:"since"`
	Enabled    bool           `json:"enabled"`
	Manual     bool           `json:"manual,omitzero"` // disconnected by the user
	MountPoint string         `json:"mount_point"`
	Port       int            `json:"port,omitzero"`
	Uploads    int            `json:"uploads"`
	Stuck      bool           `json:"uploads_stuck,omitzero"`
	LastError  string         `json:"last_error,omitempty"`
	Attempt    int            `json:"attempt,omitzero"`
	NextRetry  time.Time      `json:"next_retry,omitzero"`
	Pending    *PendingChange `json:"pending,omitempty"`
	LastProbe  time.Time      `json:"last_probe,omitzero"`
}

// backoff returns the delay before retry number attempt (1-based):
// base, 2*base, 4*base, ... capped at limit.
func backoff(attempt int, base, limit time.Duration) time.Duration {
	d := base
	for i := 1; i < attempt && d < limit; i++ {
		d *= 2
	}
	return min(d, limit)
}
