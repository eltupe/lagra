// Package menubar renders lagra's state in the macOS menu bar with
// fyne.io/systray and forwards clicks to the app. Everything shown is derived
// from a Model that is unit-tested; this file only maps it onto systray items.
package menubar

import (
	"cmp"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/eltupe/lagra/internal/app"
	"github.com/eltupe/lagra/internal/engine"
	"github.com/eltupe/lagra/internal/ipc"
	"github.com/eltupe/lagra/internal/window"
)

// Item is one line of the menu as the model computes it.
type Item struct {
	Title    string
	Disabled bool
	Checked  bool
	Children []Item
	// Action is "", "settings", "connect:<name>", "disconnect:<name>",
	// "sync:<name>", "open:<path>", "reload", "openconfig", "openlogs",
	// "reconnect" or "quit".
	Action string
}

// Model is a pure function of the daemon status.
type Model struct {
	Tooltip string
	Icon    []byte
	Items   []Item
}

// Build computes the menu for a status.
func Build(st ipc.Status) Model {
	m := Model{Icon: iconFor(st.Overall, st.ConfigOK)}
	switch {
	case st.ConfigMissing:
		m.Icon = icon("idle")
		m.Tooltip = "Lagra: not set up yet"
		m.Items = append(m.Items, Item{Title: "Set up Lagra…", Action: "settings"})
	case !st.ConfigOK:
		m.Tooltip = "Lagra: config error"
		m.Items = append(m.Items, Item{Title: "Config error: " + truncate(firstLine(st.ConfigError), 60), Disabled: true})
	default:
		m.Tooltip = "Lagra: " + string(st.Overall)
	}
	mounts := append([]engine.Snapshot(nil), st.Mounts...)
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Name < mounts[j].Name })
	if len(mounts) == 0 && st.ConfigOK {
		m.Items = append(m.Items, Item{Title: "No mounts configured", Disabled: true})
	}
	for _, s := range mounts {
		m.Items = append(m.Items, mountItem(s))
	}
	m.Items = append(m.Items,
		Item{Title: "-"},
		Item{Title: "Settings…", Action: "settings"},
		Item{Title: "Reconnect all", Action: "reconnect"},
		Item{Title: "Reload config", Action: "reload"},
		Item{Title: "Open config in editor", Action: "openconfig"},
		Item{Title: "Open logs", Action: "openlogs"},
		Item{Title: "-"},
		Item{Title: "Quit Lagra", Action: "quit"},
	)
	return m
}

func mountItem(s engine.Snapshot) Item {
	title := fmt.Sprintf("%s — %s", s.Name, label(s))
	it := Item{Title: title, Disabled: !s.Enabled}
	if !s.Enabled {
		it.Title = s.Name + " — disabled"
		return it
	}
	switch s.State {
	case engine.Connected, engine.Offline:
		it.Children = append(it.Children,
			Item{Title: "Open in Finder", Action: "open:" + s.MountPoint},
			Item{Title: "Sync now", Action: "sync:" + s.Name},
			Item{Title: "Disconnect", Action: "disconnect:" + s.Name},
		)
	case engine.Connecting:
		it.Children = append(it.Children, Item{Title: "Disconnect", Action: "disconnect:" + s.Name})
	default:
		it.Children = append(it.Children, Item{Title: "Connect", Action: "connect:" + s.Name})
	}
	if s.Uploads > 0 {
		t := fmt.Sprintf("%d upload(s) pending", s.Uploads)
		if s.Stuck {
			t += " (stuck)"
		}
		it.Children = append(it.Children, Item{Title: t, Disabled: true})
	}
	if s.Pending != nil {
		it.Children = append(it.Children, Item{Title: fmt.Sprintf("Change pending: %s (%s)", s.Pending.Action, s.Pending.Reason), Disabled: true})
	}
	if s.LastError != "" && (s.State == engine.Broken || s.State == engine.Failed || s.State == engine.Offline) {
		it.Children = append(it.Children, Item{Title: truncate(firstLine(s.LastError), 70), Disabled: true})
	}
	return it
}

func label(s engine.Snapshot) string {
	switch s.State {
	case engine.Connected:
		if s.Uploads > 0 {
			return fmt.Sprintf("connected, %d uploading", s.Uploads)
		}
		return "connected"
	case engine.Offline:
		return "offline (backend unreachable)"
	case engine.Connecting:
		return "connecting…"
	case engine.Broken:
		if !s.NextRetry.IsZero() {
			return fmt.Sprintf("broken, retry in %s", time.Until(s.NextRetry).Round(time.Second))
		}
		return "broken"
	case engine.Failed:
		return "failed"
	case engine.Disconnected:
		if s.Manual {
			return "disconnected by you"
		}
		return "disconnected"
	}
	return string(s.State)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// truncate shortens s to n characters, never inside one: a cut UTF-8
// sequence would blank the whole menu line.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// Run starts the menu bar and blocks until the app stops; it returns the
// app's error, e.g. when another daemon already runs. It must be called
// from the main goroutine (Cocoa requirement).
func Run(ctx context.Context, a *app.App) error {
	ui := &ui{app: a}
	var runErr error
	appDone := make(chan struct{})
	go func() {
		runErr = a.Run(ctx)
		close(appDone)
	}()
	go func() {
		<-appDone
		systray.Quit()
	}()
	systray.Run(ui.onReady, func() {})
	<-appDone
	return runErr
}

type ui struct {
	app *app.App

	mu      sync.Mutex
	items   []*systray.MenuItem // top-level items, reused
	subs    [][]*systray.MenuItem
	last    string   // what the menu shows now, to skip identical renders
	actions sync.Map // "i/j" -> action of that item
}

func (u *ui) onReady() {
	u.app.Log().Info("menu bar: systray ready")
	// Quitting from outside (logout, brew upgrade) cannot ask about pending
	// uploads; they resume at the next start.
	handleQuitEvent(func() {
		u.app.Log().Info("quit requested by macOS")
		_ = u.app.Shutdown(true)
	})
	systray.SetTemplateIcon(icon("idle"), icon("idle"))
	systray.SetTooltip("Lagra")
	u.render()
	u.app.OnChange(func() { u.render() })
	u.app.Log().Info("menu bar: initial menu built")
}

// render rebuilds the menu. systray cannot delete items, so items are created
// on demand and hidden when unused.
func (u *ui) render() {
	u.mu.Lock()
	defer u.mu.Unlock()
	m := Build(u.app.Status())
	// The app wakes the menu every two seconds; most of the time nothing
	// changed and systray need not hear about it.
	shown := fmt.Sprintf("%s\x00%v\x00%s", m.Tooltip, m.Items, m.Icon)
	if shown == u.last {
		return
	}
	u.last = shown
	u.app.Log().Debug("menu bar: render", "items", len(m.Items))
	systray.SetTemplateIcon(m.Icon, m.Icon)
	systray.SetTooltip(m.Tooltip)
	for i, it := range m.Items {
		u.place(i, it)
	}
	for i := len(m.Items); i < len(u.items); i++ {
		u.items[i].Hide()
	}
}

func (u *ui) place(i int, it Item) {
	for len(u.items) <= i {
		mi := systray.AddMenuItem("", "")
		u.items = append(u.items, mi)
		u.subs = append(u.subs, nil)
		u.watch(mi, i, -1)
	}
	mi := u.items[i]
	if it.Title == "-" {
		mi.SetTitle("—")
		mi.Disable()
		mi.Show()
		return
	}
	mi.SetTitle(it.Title)
	mi.SetTooltip(it.Title)
	if it.Disabled {
		mi.Disable()
	} else {
		mi.Enable()
	}
	mi.Show()
	u.actions.Store(key(i, -1), it.Action)
	for j, ch := range it.Children {
		for len(u.subs[i]) <= j {
			sub := mi.AddSubMenuItem("", "")
			u.subs[i] = append(u.subs[i], sub)
			u.watch(sub, i, len(u.subs[i])-1)
		}
		sub := u.subs[i][j]
		sub.SetTitle(ch.Title)
		if ch.Disabled {
			sub.Disable()
		} else {
			sub.Enable()
		}
		sub.Show()
		u.actions.Store(key(i, j), ch.Action)
	}
	for j := len(it.Children); j < len(u.subs[i]); j++ {
		u.subs[i][j].Hide()
	}
}

func key(i, j int) string { return fmt.Sprintf("%d/%d", i, j) }

func (u *ui) watch(mi *systray.MenuItem, i, j int) {
	go func() {
		for range mi.ClickedCh {
			if v, ok := u.actions.Load(key(i, j)); ok {
				u.act(v.(string))
			}
		}
	}()
}

func (u *ui) act(action string) {
	if action == "" {
		return
	}
	kind, arg, _ := strings.Cut(action, ":")
	switch kind {
	case "settings":
		if url := u.app.SettingsURL(); url != "" {
			window.Open(url, "Lagra", 900, 720)
		}
	case "connect":
		_ = u.app.Connect(arg)
	case "disconnect":
		_ = u.app.Disconnect(arg, false)
	case "sync":
		go func() { _, _ = u.app.Sync(context.Background(), arg) }()
	case "open":
		_ = exec.Command("/usr/bin/open", arg).Start()
	case "reload":
		u.app.Reload()
	case "openconfig":
		st := u.app.Status()
		p := cmp.Or(st.ConfigReal, st.ConfigPath)
		_ = exec.Command("/usr/bin/open", "-t", p).Start()
	case "openlogs":
		_ = exec.Command("/usr/bin/open", u.app.LogDir()).Start()
	case "reconnect":
		for _, s := range u.app.Status().Mounts {
			if s.Enabled {
				_ = u.app.Connect(s.Name)
			}
		}
	case "quit":
		if err := u.app.Shutdown(false); err != nil {
			// Ask before dropping pending uploads.
			if confirmQuit(err.Error()) {
				_ = u.app.Shutdown(true)
			}
		}
	}
}

// confirmQuit shows a native dialog through osascript; returns true to quit anyway.
func confirmQuit(reason string) bool {
	script := `display dialog (item 1 of argv) buttons {"Keep running", "Quit anyway"} default button "Keep running" with icon caution with title "Lagra"`
	out, err := exec.Command("/usr/bin/osascript", "-e", "on run argv", "-e", script, "-e", "end run",
		reason+"\n\nQuit anyway? Uploads resume the next time Lagra starts.").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "Quit anyway")
}
