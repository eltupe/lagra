package menubar

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/eltupe/lagra/internal/engine"
	"github.com/eltupe/lagra/internal/ipc"
)

func TestBuildModel(t *testing.T) {
	st := ipc.Status{ConfigOK: true, Overall: engine.OverallPartial, Mounts: []engine.Snapshot{
		{Name: "b", State: engine.Disconnected, Enabled: false},
		{Name: "a", State: engine.Connected, Enabled: true, MountPoint: "/Users/x/Volumes/a", Uploads: 2},
		{Name: "c", State: engine.Broken, Enabled: true, LastError: "mount probe: hung", Attempt: 3, NextRetry: time.Now().Add(40 * time.Second)},
		{Name: "d", State: engine.Connected, Enabled: true, Pending: &engine.PendingChange{Action: "disconnect", Reason: "uploads pending"}},
	}}
	m := Build(st)
	if m.Tooltip != "Lagra: partial" || len(m.Icon) == 0 {
		t.Errorf("tooltip/icon: %q %d", m.Tooltip, len(m.Icon))
	}
	titles := []string{}
	for _, it := range m.Items {
		titles = append(titles, it.Title)
	}
	joined := strings.Join(titles, "|")
	for _, want := range []string{"a — connected, 2 uploading", "b — disabled", "c — broken, retry in", "d — connected", "Settings…", "Reload config", "Quit Lagra"} {
		if !strings.Contains(joined, want) {
			t.Errorf("menu lacks %q in %s", want, joined)
		}
	}
	// order: mounts sorted, disabled greyed but present
	if !strings.HasPrefix(m.Items[0].Title, "a ") || !strings.HasPrefix(m.Items[1].Title, "b ") || !m.Items[1].Disabled {
		t.Errorf("order/disabled wrong: %v", titles)
	}
	a := m.Items[0]
	if a.Children[0].Action != "open:/Users/x/Volumes/a" || a.Children[1].Action != "sync:a" || a.Children[2].Action != "disconnect:a" || !strings.Contains(a.Children[3].Title, "2 upload") {
		t.Errorf("a children = %+v", a.Children)
	}
	c := m.Items[2]
	if c.Children[0].Action != "connect:c" || !strings.Contains(c.Children[1].Title, "hung") {
		t.Errorf("c children = %+v", c.Children)
	}
	d := m.Items[3]
	if !strings.Contains(d.Children[len(d.Children)-1].Title, "Change pending: disconnect (uploads pending)") {
		t.Errorf("d children = %+v", d.Children)
	}
}

func TestBuildConfigError(t *testing.T) {
	m := Build(ipc.Status{ConfigOK: false, ConfigError: "line 3: mount \"x\".type: unknown backend type \"ftpx\"\nline 9: ..."})
	if !strings.HasPrefix(m.Items[0].Title, "Config error: line 3") || strings.Contains(m.Items[0].Title, "line 9") {
		t.Errorf("config error item = %q", m.Items[0].Title)
	}
	if string(m.Icon) != string(icon("error")) {
		t.Error("config error must use the warning icon")
	}
}

func TestBuildFirstRun(t *testing.T) {
	m := Build(ipc.Status{ConfigOK: false, ConfigError: "no config file yet at /x/config.toml", ConfigMissing: true})
	if m.Items[0].Title != "Set up Lagra…" || m.Items[0].Action != "settings" {
		t.Errorf("first run item = %+v", m.Items[0])
	}
	if string(m.Icon) != string(icon("idle")) {
		t.Error("first run must not show the error icon")
	}
}

func TestIconsRender(t *testing.T) {
	for _, k := range []string{"ok", "idle", "partial", "error"} {
		if b := icon(k); len(b) < 100 || string(b[1:4]) != "PNG" {
			t.Errorf("icon %s not a PNG (%d bytes)", k, len(b))
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
