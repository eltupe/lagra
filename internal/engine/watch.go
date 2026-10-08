package engine

import (
	"context"
	"net"
	"slices"
	"strings"
	"time"
)

// WakeWatcher detects a return from sleep by comparing the wall clock with
// the interval between two ticks: after sleep the wall clock jumps while the
// ticker only fired once. No cgo, no IOKit.
type WakeWatcher struct {
	Interval time.Duration // tick interval, default 5 s
	Slack    time.Duration // jump beyond which we call it a wake, default 2×Interval
	OnWake   func(gap time.Duration)
	// Now reads the clock; nil means the wall clock. It must not carry Go's
	// monotonic reading, which on macOS stops while the Mac sleeps and
	// would hide the gap.
	Now func() time.Time
}

// Run blocks until ctx is done.
func (w WakeWatcher) Run(ctx context.Context) {
	if w.Interval == 0 {
		w.Interval = 5 * time.Second
	}
	if w.Slack == 0 {
		w.Slack = 2 * w.Interval
	}
	if w.Now == nil {
		w.Now = func() time.Time { return time.Now().Round(0) }
	}
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	last := w.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := w.Now()
			if gap := now.Sub(last); gap > w.Interval+w.Slack && w.OnWake != nil {
				w.OnWake(gap)
			}
			last = now
		}
	}
}

// NetWatcher polls the set of interface addresses and reports changes; that
// covers Wi-Fi switches, VPN up/down and cable plug events without
// SystemConfiguration bindings.
type NetWatcher struct {
	Interval time.Duration // default 10 s
	OnChange func()
	// Addrs overrides address discovery in tests.
	Addrs func() ([]string, error)
}

// Run blocks until ctx is done.
func (n NetWatcher) Run(ctx context.Context) {
	if n.Interval == 0 {
		n.Interval = 10 * time.Second
	}
	addrs := n.Addrs
	if addrs == nil {
		addrs = interfaceAddrs
	}
	last := fingerprint(addrs)
	t := time.NewTicker(n.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cur := fingerprint(addrs)
			if cur != last && n.OnChange != nil {
				n.OnChange()
			}
			last = cur
		}
	}
}

func interfaceAddrs() ([]string, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		as, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range as {
			s := a.String()
			if strings.HasPrefix(s, "fe80:") { // link-local churn is noise
				continue
			}
			out = append(out, ifc.Name+" "+s)
		}
	}
	return out, nil
}

func fingerprint(addrs func() ([]string, error)) string {
	as, err := addrs()
	if err != nil {
		return "err"
	}
	slices.Sort(as)
	return strings.Join(as, "\n")
}
