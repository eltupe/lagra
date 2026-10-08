// Package launchagent installs or removes the LaunchAgent that starts lagra
// at login. It is driven by launch_at_login in the config and applied on
// every reload.
package launchagent

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
)

// Label is the LaunchAgent label.
const Label = "io.github.eltupe.lagra.agent"

// Runner executes launchctl; tests replace it.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Agent manages the plist and launchctl registration.
type Agent struct {
	PlistPath string // default ~/Library/LaunchAgents/<Label>.plist
	Program   string // absolute path to the lagra binary
	Args      []string
	LogDir    string
	UID       int
	Run       Runner
}

// New returns an agent for the current user.
func New(program string, args []string, logDir string) (*Agent, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Agent{
		PlistPath: filepath.Join(home, "Library", "LaunchAgents", Label+".plist"),
		Program:   program,
		Args:      args,
		LogDir:    logDir,
		UID:       os.Getuid(),
	}, nil
}

// plistTmpl escapes every value: a path may contain & or <.
var plistTmpl = template.Must(template.New("plist").Funcs(template.FuncMap{"xml": xmlText}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{xml .Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{xml .Program}}</string>
{{- range .Args}}
		<string>{{xml .}}</string>
{{- end}}
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>StandardOutPath</key>
	<string>{{xml .LogDir}}/launchd.out.log</string>
	<key>StandardErrorPath</key>
	<string>{{xml .LogDir}}/launchd.err.log</string>
</dict>
</plist>
`))

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Plist renders the plist content.
func (a *Agent) Plist() ([]byte, error) {
	var buf bytes.Buffer
	err := plistTmpl.Execute(&buf, map[string]any{
		"Label": Label, "Program": a.Program, "Args": a.Args, "LogDir": a.LogDir,
	})
	return buf.Bytes(), err
}

func (a *Agent) run() Runner {
	if a.Run != nil {
		return a.Run
	}
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
}

// Installed reports whether the plist exists.
func (a *Agent) Installed() bool {
	_, err := os.Stat(a.PlistPath)
	return err == nil
}

// Enable writes the plist (if changed) and registers it with launchd.
func (a *Agent) Enable(ctx context.Context) error {
	want, err := a.Plist()
	if err != nil {
		return err
	}
	have, _ := os.ReadFile(a.PlistPath)
	if bytes.Equal(have, want) {
		return nil // already installed with identical content
	}
	if err := os.MkdirAll(filepath.Dir(a.PlistPath), 0o755); err != nil {
		return err
	}
	if len(have) > 0 {
		_, _ = a.run()(ctx, "/bin/launchctl", "bootout", a.domain()+"/"+Label)
	}
	if err := os.WriteFile(a.PlistPath, want, 0o644); err != nil {
		return err
	}
	if out, err := a.run()(ctx, "/bin/launchctl", "bootstrap", a.domain(), a.PlistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap: %s", firstLine(out, err))
	}
	return nil
}

// Disable unregisters and removes the plist. It does not stop a running
// instance that was started by hand.
func (a *Agent) Disable(ctx context.Context) error {
	if !a.Installed() {
		return nil
	}
	_, _ = a.run()(ctx, "/bin/launchctl", "bootout", a.domain()+"/"+Label)
	if err := os.Remove(a.PlistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (a *Agent) domain() string { return "gui/" + strconv.Itoa(a.UID) }

func firstLine(out []byte, err error) string {
	s := bytes.TrimSpace(out)
	if len(s) == 0 {
		return err.Error()
	}
	if i := bytes.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return string(s)
}
