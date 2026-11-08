package menubar

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/eltupe/lagra/internal/engine"
	"github.com/eltupe/lagra/internal/ipc"
)

// visible is what the menu shows: hidden entries left out, submenus too.
func visible(items []Item) []Item {
	var out []Item
	for _, it := range items {
		if !it.Hidden {
			out = append(out, it)
		}
	}
	return out
}

func titles(items []Item) string {
	var t []string
	for _, it := range visible(items) {
		if it.Separator {
			t = append(t, "---")
		} else {
			t = append(t, it.Title)
		}
	}
	return strings.Join(t, "|")
}

func find(t *testing.T, items []Item, title string) Item {
	t.Helper()
	for _, it := range items {
		if it.Title == title {
			return it
		}
	}
	t.Fatalf("no %q in %s", title, titles(items))
	return Item{}
}

func TestBuildModel(t *testing.T) {
	st := ipc.Status{ConfigOK: true, Overall: engine.OverallPartial, Mounts: []engine.Snapshot{
		{Name: "b", State: engine.Disconnected, Enabled: false},
		{Name: "a", State: engine.Connected, Enabled: true, MountPoint: "/Users/x/Volumes/a", Uploads: 2},
		{Name: "c", State: engine.Broken, Enabled: true, LastError: "mount probe: hung", Attempt: 3, NextRetry: time.Now().Add(40 * time.Second)},
		{Name: "d", State: engine.Connected, Enabled: true, Pending: &engine.PendingChange{Action: "disconnect", Reason: "uploads pending"}},
	}}
	m := Build(st)
	if m.Tooltip != "Lagra: 2 of 3 connected" || len(m.Icon) == 0 {
		t.Errorf("tooltip/icon: %q %d", m.Tooltip, len(m.Icon))
	}
	want := "2 of 3 connected|Uploading 2 files…|---|Storages|Connect All|---|Settings…|Edit Config File|Reload Config|Show Logs in Finder|---|Quit Lagra"
	if got := titles(m.Items); got != want {
		t.Errorf("menu =\n%s\nwant\n%s", got, want)
	}
	if find(t, m.Items, "Connect All").Disabled {
		t.Error("Connect All must be enabled while c is broken")
	}

	// storages sorted, each with a dot; b is off and greyed out
	storages := find(t, m.Items, "Storages").Children
	if got := titles(storages); got != "a — uploading 2 files|b|c|d" {
		t.Errorf("storages = %s", got)
	}
	if b := storages[1]; !b.Disabled || b.Icon != "idle" || b.Tooltip != "Off" || b.Children != nil {
		t.Errorf("b = %+v", b)
	}
	if storages[0].Icon != "ok" || storages[2].Icon != "warn" {
		t.Errorf("dots = %q %q", storages[0].Icon, storages[2].Icon)
	}

	a := storages[0].Children
	if got := titles(a); got != "Connected|Uploading 2 files…|---|Open in Finder|Sync Now|Disconnect" {
		t.Errorf("a = %s", got)
	}
	if find(t, a, "Open in Finder").Action != "open:/Users/x/Volumes/a" || find(t, a, "Sync Now").Action != "sync:a" || find(t, a, "Disconnect").Action != "disconnect:a" {
		t.Errorf("a actions = %+v", a)
	}
	c := visible(storages[2].Children)
	if !strings.HasPrefix(c[0].Title, "Reconnecting in ") || c[1].Title != "mount probe: hung" || c[3].Title != "Reconnect Now" || c[3].Action != "connect:c" {
		t.Errorf("c = %s", titles(c))
	}
	if d := titles(storages[3].Children); !strings.Contains(d, "|Disconnects once all uploads are done|---|") {
		t.Errorf("d = %s", d)
	}
}

// A storage keeps the same entries in every state, only showing or hiding
// some, so the menu is never rebuilt while the user has it open, and no
// state leaves a separator at an edge or two in a row.
func TestEveryStateKeepsTheShape(t *testing.T) {
	var shapes []string
	for _, state := range []engine.State{engine.Connected, engine.Offline, engine.Connecting, engine.Broken, engine.Failed, engine.Disconnected} {
		for _, s := range []engine.Snapshot{
			{State: state},
			{State: state, Uploads: 3, Stuck: true, LastError: "boom", Pending: &engine.PendingChange{Action: "restart", Reason: "in use"}},
		} {
			s.Name, s.Enabled = "x", true
			m := Build(ipc.Status{ConfigOK: true, Mounts: []engine.Snapshot{s}})
			shapes = append(shapes, shape(m.Items))
			checkSeparators(t, string(state), m.Items)
		}
	}
	for _, sh := range shapes[1:] {
		if sh != shapes[0] {
			t.Fatalf("shapes differ: %q vs %q", shapes[0], sh)
		}
	}
}

func checkSeparators(t *testing.T, what string, items []Item) {
	t.Helper()
	v := visible(items)
	for i, it := range v {
		if it.Separator && (i == 0 || i == len(v)-1 || v[i-1].Separator) {
			t.Errorf("%s: separator misplaced in %s", what, titles(items))
		}
		checkSeparators(t, what+" > "+it.Title, it.Children)
	}
}

func TestBuildConfigError(t *testing.T) {
	m := Build(ipc.Status{ConfigOK: false, ConfigError: "line 3: mount \"x\".type: unknown backend type \"ftpx\"\nline 9: ..."})
	if !strings.HasPrefix(m.Items[0].Title, "Config error: line 3") || strings.Contains(m.Items[0].Title, "line 9") || !strings.Contains(m.Items[0].Tooltip, "line 9") {
		t.Errorf("config error item = %+v", m.Items[0])
	}
	if got := titles(m.Items); !strings.Contains(got, "|---|Settings…|Edit Config File|") {
		t.Errorf("config error menu = %s", got)
	}
	if string(m.Icon) != string(icon("error")) {
		t.Error("config error must use the warning icon")
	}
}

func TestBuildFirstRun(t *testing.T) {
	m := Build(ipc.Status{ConfigOK: false, ConfigError: "no config file yet at /x/config.toml", ConfigMissing: true})
	if got := titles(m.Items); got != "Set Up Lagra…|---|Show Logs in Finder|---|Quit Lagra" {
		t.Errorf("first run menu = %s", got)
	}
	if m.Items[0].Action != "settings" {
		t.Errorf("first run item = %+v", m.Items[0])
	}
	if string(m.Icon) != string(icon("idle")) {
		t.Error("first run must not show the error icon")
	}
}

func TestBuildNoStorages(t *testing.T) {
	m := Build(ipc.Status{ConfigOK: true})
	if got := titles(m.Items); got != "No storages yet|---|Settings…|Edit Config File|Reload Config|Show Logs in Finder|---|Quit Lagra" {
		t.Errorf("empty menu = %s", got)
	}
}

func TestConnectAllOnlyWhenSomethingToConnect(t *testing.T) {
	m := Build(ipc.Status{ConfigOK: true, Mounts: []engine.Snapshot{
		{Name: "a", State: engine.Connected, Enabled: true},
		{Name: "b", State: engine.Offline, Enabled: true},
		{Name: "c", State: engine.Disconnected, Enabled: false},
	}})
	if !find(t, m.Items, "Connect All").Disabled {
		t.Error("Connect All has nothing to do: connect leaves offline storages alone and c is off")
	}
}

func TestIconsRender(t *testing.T) {
	for _, k := range []string{"ok", "idle", "partial", "error"} {
		if b := icon(k); len(b) < 100 || string(b[1:4]) != "PNG" {
			t.Errorf("icon %s not a PNG (%d bytes)", k, len(b))
		}
	}
	for _, k := range []string{"ok", "busy", "warn", "bad", "idle"} {
		if b := dotIcon(k); len(b) < 50 || string(b[1:4]) != "PNG" {
			t.Errorf("dot %s not a PNG (%d bytes)", k, len(b))
		}
	}
}

func TestTruncateKeepsCharactersWhole(t *testing.T) {
	got := truncate("Zürich: Verbindung über SFTP fehlgeschlagen", 3)
	if got != "Zü…" || !utf8.ValidString(got) {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("kurz", 10); got != "kurz" {
		t.Errorf("short string changed: %q", got)
	}
}
