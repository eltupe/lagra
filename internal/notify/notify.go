// Package notify shows macOS user notifications through osascript. No cgo,
// no bundle identifier needed; notifications appear as coming from Script
// Editor's runtime, which is acceptable for a menu-bar tool signed ad hoc.
package notify

import (
	"context"
	"log/slog"
	"os/exec"
	"time"
)

// OSA sends notifications asynchronously.
type OSA struct {
	Log *slog.Logger
	// Run overrides process execution in tests.
	Run func(ctx context.Context, name string, args ...string) error
}

// Notify implements engine.Notifier.
func (o OSA) Notify(title, body string) {
	run := o.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) error {
			return exec.CommandContext(ctx, name, args...).Run()
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Arguments travel via argv, so quotes in title/body are safe.
		err := run(ctx, "/usr/bin/osascript",
			"-e", "on run argv",
			"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
			"-e", "end run",
			title, body)
		if err != nil && o.Log != nil {
			o.Log.Warn("notification failed", "err", err.Error())
		}
	}()
}
