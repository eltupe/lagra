// Package menubar renders lagra's state in the macOS menu bar with
// fyne.io/systray and forwards clicks to the app. Everything shown is derived
// from a Model that is unit-tested; this file only maps it onto systray items.
package menubar

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
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
	Title   string
	Tooltip string
	// Icon is a status dot before the title, coloured like the settings
	// window's: "ok", "busy", "warn", "bad", "idle", or "" for none.
	Icon     string
	Disabled bool
	// Hidden entries stay in the menu, invisible, so that the menu keeps
	// its structure while a storage changes state; see shape.
	Hidden    bool
	Separator bool
	Children  []Item
	// Action is "", "settings", "connect:<name>", "disconnect:<name>",
	// "sync:<name>", "open:<path>", "reload", "openconfig", "openlogs",
	// "connectall" or "quit".
	Action string
}

var separator = Item{Separator: true}

// Model is a pure function of the daemon status.
type Model struct {
	Tooltip string
	Icon    []byte
	Items   []Item
}

// Build computes the menu for a status: what Lagra is doing, the storages
// in a submenu, the app's own commands, Quit.
func Build(st ipc.Status) Model {
	m := Model{Icon: iconFor(st.Overall, st.ConfigOK)}
	mounts := append([]engine.Snapshot(nil), st.Mounts...)
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Name < mounts[j].Name })
	summary, uploads := summarize(mounts)

	var status []Item
	switch {
	case st.ConfigMissing:
		m.Icon = icon("idle")
		m.Tooltip = "Lagra: not set up yet"
		status = append(status, Item{Title: "Set Up Lagra…", Action: "settings"})
	case !st.ConfigOK:
		m.Tooltip = "Lagra: config error"
		status = append(status, Item{Title: "Config error: " + truncate(firstLine(st.ConfigError), 60), Tooltip: st.ConfigError, Disabled: true})
	default:
		m.Tooltip = "Lagra: " + summary
	}
	if st.ConfigOK || len(mounts) > 0 {
		status = append(status,
			Item{Title: summary, Disabled: true},
			Item{Title: fmt.Sprintf("Uploading %s…", plural(uploads, "file")), Disabled: true, Hidden: uploads == 0},
		)
	}

	var storages []Item
	if len(mounts) > 0 {
		list := make([]Item, 0, len(mounts))
		connectable := false
		for _, s := range mounts {
			list = append(list, mountItem(s))
			connectable = connectable || s.Enabled && !mounted(s) && s.State != engine.Connecting
		}
		storages = append(storages,
			Item{Title: "Storages", Children: list},
			Item{Title: "Connect All", Action: "connectall", Disabled: !connectable},
		)
	}

	var commands []Item
	if !st.ConfigMissing {
		commands = append(commands,
			Item{Title: "Settings…", Action: "settings"},
			Item{Title: "Edit Config File", Action: "openconfig"},
			Item{Title: "Reload Config", Action: "reload"},
		)
	}
	commands = append(commands, Item{Title: "Show Logs in Finder", Action: "openlogs"})

	m.Items = sections(status, storages, commands, []Item{{Title: "Quit Lagra", Action: "quit"}})
	return m
}

// summarize says how many of the storages that are turned on are
// connected, and counts the files waiting to upload.
func summarize(mounts []engine.Snapshot) (string, int) {
	on, connected, uploads := 0, 0, 0
	for _, s := range mounts {
		uploads += s.Uploads
		if !s.Enabled {
			continue
		}
		on++
		if s.State == engine.Connected {
			connected++
		}
	}
	switch {
	case len(mounts) == 0:
		return "No storages yet", uploads
	case on == 0:
		return "All storages are off", uploads
	}
	return fmt.Sprintf("%d of %d connected", connected, on), uploads
}

// sections joins groups of items with separators, leaving out empty groups.
func sections(groups ...[]Item) []Item {
	var out []Item
	for _, g := range groups {
		if len(g) == 0 {
			continue
		}
		if len(out) > 0 {
			out = append(out, separator)
		}
		out = append(out, g...)
	}
	return out
}

// mountItem is a storage in the Storages submenu: its name with a status
// dot, and a submenu that says what is going on above a separator and
// offers what can be done below it. Every state has the same entries, the
// ones that do not apply are hidden, so a state change never rebuilds the
// menu under the pointer. A storage turned off in the config is greyed out.
func mountItem(s engine.Snapshot) Item {
	words := statusWords(s)
	it := Item{Title: s.Name, Tooltip: words, Icon: dot(s)}
	if !s.Enabled {
		it.Disabled = true
		return it
	}
	if s.Uploads > 0 {
		it.Title += " — uploading " + plural(s.Uploads, "file")
	}
	uploads := fmt.Sprintf("Uploading %s…", plural(s.Uploads, "file"))
	if s.Stuck {
		uploads = fmt.Sprintf("%s seem stuck", plural(s.Uploads, "upload"))
	}
	showErr := s.LastError != "" && (s.State == engine.Broken || s.State == engine.Failed || s.State == engine.Offline)
	connect := "Connect"
	if s.State == engine.Broken {
		connect = "Reconnect Now"
	}
	on := mounted(s)
	it.Children = []Item{
		{Title: words, Disabled: true},
		{Title: uploads, Disabled: true, Hidden: s.Uploads == 0},
		{Title: pendingWords(s.Pending), Disabled: true, Hidden: s.Pending == nil},
		{Title: truncate(firstLine(s.LastError), 70), Tooltip: s.LastError, Disabled: true, Hidden: !showErr},
		separator,
		{Title: "Open in Finder", Action: "open:" + s.MountPoint, Hidden: !on},
		{Title: "Sync Now", Action: "sync:" + s.Name, Hidden: !on},
		{Title: connect, Action: "connect:" + s.Name, Hidden: on || s.State == engine.Connecting},
		{Title: "Disconnect", Action: "disconnect:" + s.Name, Hidden: !on && s.State != engine.Connecting},
	}
	return it
}

// mounted reports whether the volume is in Finder.
func mounted(s engine.Snapshot) bool {
	return s.State == engine.Connected || s.State == engine.Offline
}

// statusWords names a storage's state in the words of the settings window.
func statusWords(s engine.Snapshot) string {
	if !s.Enabled {
		return "Off"
	}
	switch s.State {
	case engine.Connected:
		return "Connected"
	case engine.Offline:
		return "Storage unreachable"
	case engine.Connecting:
		return "Connecting…"
	case engine.Broken:
		if d := time.Until(s.NextRetry).Round(time.Second); !s.NextRetry.IsZero() && d > 0 {
			return "Reconnecting in " + d.String()
		}
		return "Reconnecting"
	case engine.Failed:
		return "Not connected"
	case engine.Disconnected:
		if s.Manual {
			return "Disconnected by you"
		}
		return "Disconnected"
	}
	return string(s.State)
}

// dot is a storage's status colour, the same as in the settings window.
func dot(s engine.Snapshot) string {
	if !s.Enabled {
		return "idle"
	}
	switch s.State {
	case engine.Connected:
		return "ok"
	case engine.Connecting:
		return "busy"
	case engine.Offline, engine.Broken:
		return "warn"
	case engine.Failed:
		return "bad"
	}
	return "idle"
}

// pendingWords says what a held-back change waits for.
func pendingWords(p *engine.PendingChange) string {
	if p == nil {
		return ""
	}
	what := map[string]string{"disconnect": "Disconnects", "restart": "Reconnects with your changes", "remove": "Goes away"}[p.Action]
	if what == "" {
		what = "Continues"
	}
	if p.Reason == "in use" {
		return what + " once no app uses the volume"
	}
	return what + " once all uploads are done"
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
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

// Daemon is what the menu bar shows and drives: *app.App, or a fake in
// hack/menupreview.
type Daemon interface {
	ipc.Controller
	Run(ctx context.Context) error
	Log() *slog.Logger
	OnChange(fn func())
	SettingsURL() string
	LogDir() string
}

var _ Daemon = (*app.App)(nil)

// Run starts the menu bar and blocks until the app stops; it returns the
// app's error, e.g. when another daemon already runs. It must be called
// from the main goroutine (Cocoa requirement).
func Run(ctx context.Context, a Daemon) error {
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
	app Daemon

	mu      sync.Mutex
	last    string   // what the menu shows now, to skip identical renders
	shape   string   // the structure on screen, see shape
	nodes   []*node  // the entries on screen, one per Item
	actions sync.Map // *systray.MenuItem -> its action
}

// node is one entry on screen; separators have none.
type node struct {
	mi       *systray.MenuItem
	shown    Item // what mi shows now
	children []*node
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

// render brings the menu up to date. Entries change in place, so an open
// menu stays open. systray can only append entries and cannot hide
// separators, so when the structure changes (a storage added or removed,
// the config broken or fixed) the menu is built anew from the top.
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
	systray.SetTemplateIcon(m.Icon, m.Icon)
	systray.SetTooltip(m.Tooltip)
	if s := shape(m.Items); s != u.shape {
		u.app.Log().Debug("menu bar: build", "shape", s)
		systray.ResetMenu() // closes the old entries' ClickedCh
		u.actions.Clear()
		u.nodes = u.add(nil, m.Items)
		u.shape = s
	}
	u.update(u.nodes, m.Items)
}

// shape is the menu's structure: entries, dots and separators, nested, but
// not what the entries say or whether they are hidden.
func shape(items []Item) string {
	var b strings.Builder
	for _, it := range items {
		switch {
		case it.Separator:
			b.WriteByte('-')
		case it.Icon != "":
			b.WriteByte('o') // systray can set an icon but not take it away
		default:
			b.WriteByte('i')
		}
		if len(it.Children) > 0 {
			b.WriteString("(" + shape(it.Children) + ")")
		}
	}
	return b.String()
}

// add appends blank entries for items to parent's submenu, or to the menu
// for nil; update fills them in. A separator needs its submenu to exist
// already, so it must not come first.
func (u *ui) add(parent *systray.MenuItem, items []Item) []*node {
	nodes := make([]*node, len(items))
	for i, it := range items {
		if it.Separator {
			if parent == nil {
				systray.AddSeparator()
			} else {
				parent.AddSeparator()
			}
			continue
		}
		var mi *systray.MenuItem
		if parent == nil {
			mi = systray.AddMenuItem("", "")
		} else {
			mi = parent.AddSubMenuItem("", "")
		}
		u.watch(mi)
		nodes[i] = &node{mi: mi, children: u.add(mi, it.Children)}
	}
	return nodes
}

// update tells systray what changed since the last render, entry by entry.
func (u *ui) update(nodes []*node, items []Item) {
	for i, it := range items {
		n := nodes[i]
		if n == nil {
			continue
		}
		was := n.shown
		if it.Title != was.Title {
			n.mi.SetTitle(it.Title)
		}
		if it.Tooltip != was.Tooltip {
			n.mi.SetTooltip(it.Tooltip)
		}
		if it.Icon != was.Icon && it.Icon != "" {
			n.mi.SetIcon(dotIcon(it.Icon))
		}
		if it.Disabled != was.Disabled {
			if it.Disabled {
				n.mi.Disable()
			} else {
				n.mi.Enable()
			}
		}
		if it.Hidden != was.Hidden {
			if it.Hidden {
				n.mi.Hide()
			} else {
				n.mi.Show()
			}
		}
		u.actions.Store(n.mi, it.Action)
		n.shown = it
		u.update(n.children, it.Children)
	}
}

func (u *ui) watch(mi *systray.MenuItem) {
	go func() {
		for range mi.ClickedCh {
			if v, ok := u.actions.Load(mi); ok {
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
	case "connectall":
		for _, s := range u.app.Status().Mounts {
			if s.Enabled && !mounted(s) && s.State != engine.Connecting {
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
