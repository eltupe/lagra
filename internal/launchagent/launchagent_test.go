package launchagent

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnableDisable(t *testing.T) {
	var calls []string
	a := &Agent{
		PlistPath: filepath.Join(t.TempDir(), "agents", Label+".plist"),
		Program:   "/Applications/Lagra.app/Contents/MacOS/lagra",
		Args:      []string{"app"},
		LogDir:    "/Users/x/Library/Logs/lagra",
		UID:       501,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, strings.Join(args, " "))
			return nil, nil
		},
	}
	ctx := context.Background()
	if err := a.Enable(ctx); err != nil {
		t.Fatal(err)
	}
	if !a.Installed() {
		t.Fatal("plist missing")
	}
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "bootstrap gui/501 ") {
		t.Errorf("calls = %v", calls)
	}
	// idempotent
	if err := a.Enable(ctx); err != nil || len(calls) != 1 {
		t.Errorf("second enable must be a no-op: %v %v", err, calls)
	}
	p, _ := a.Plist()
	for _, want := range []string{"<string>io.github.eltupe.lagra.agent</string>", "<string>app</string>", "RunAtLoad", "launchd.err.log"} {
		if !strings.Contains(string(p), want) {
			t.Errorf("plist lacks %s", want)
		}
	}
	if err := a.Disable(ctx); err != nil {
		t.Fatal(err)
	}
	if a.Installed() || calls[len(calls)-1] != "bootout gui/501/"+Label {
		t.Errorf("disable: installed=%v calls=%v", a.Installed(), calls)
	}
	if err := a.Disable(ctx); err != nil {
		t.Error("disable must be idempotent")
	}
}

// An app path with XML special characters must still give a valid plist.
func TestPlistEscapesPaths(t *testing.T) {
	a := &Agent{Program: "/Users/x/Apps & Tools/<Lagra>.app/Contents/MacOS/lagra", Args: []string{"app"}, LogDir: "/tmp/a&b"}
	p, err := a.Plist()
	if err != nil {
		t.Fatal(err)
	}
	d := xml.NewDecoder(bytes.NewReader(p))
	d.Strict = true
	var strs []string
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid plist: %v\n%s", err, p)
		}
		if cd, ok := tok.(xml.CharData); ok && strings.Contains(string(cd), "Lagra") {
			strs = append(strs, string(cd))
		}
	}
	if len(strs) != 1 || strs[0] != a.Program {
		t.Errorf("program round trip = %q", strs)
	}
}
