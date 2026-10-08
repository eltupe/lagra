// Package settings is the write side of the config file used by the settings
// window: it edits the TOML text surgically (see tomledit), validates the
// result before writing, writes atomically through symlinks, and keeps
// keychain items in step with account names. The file stays the single source
// of truth; the daemon picks changes up through the normal hot reload.
package settings

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

	"github.com/eltupe/lagra/internal/atomicfile"
	"github.com/eltupe/lagra/internal/config"
	"github.com/eltupe/lagra/internal/secrets"
	"github.com/eltupe/lagra/internal/tomledit"
)

// Service edits one config file.
type Service struct {
	Path     string // configured path, may be a symlink
	Registry config.Registry
	Keychain secrets.Keychain
	Env      config.Env
	// OnSecretsChanged is called after keychain values of an account were
	// written. The daemon uses it to reconnect the account's mounts, because
	// a secret change leaves the file untouched and the hot reload would not
	// notice.
	OnSecretsChanged func(account string)
}

// View is what the UI needs to render the whole configuration.
type View struct {
	Path     string        `json:"path"`
	RealPath string        `json:"real_path"`
	Exists   bool          `json:"exists"`
	General  GeneralView   `json:"general"`
	Defaults DefaultsView  `json:"defaults"`
	Accounts []AccountView `json:"accounts"`
	Mounts   []MountView   `json:"mounts"`
	Issues   config.Issues `json:"issues"`
	Valid    bool          `json:"valid"`
}

// GeneralView mirrors [general] under its TOML key names, durations as text.
type GeneralView struct {
	LogLevel             string `json:"log_level"`
	CacheDir             string `json:"cache_dir"`
	MountProbeInterval   string `json:"mount_probe_interval"`
	BackendProbeInterval string `json:"backend_probe_interval"`
	UploadStuckAfter     string `json:"upload_stuck_after"`
	LaunchAtLogin        *bool  `json:"launch_at_login"`
}

// DefaultsView mirrors [defaults] with VFS values stringified.
type DefaultsView struct {
	MountRoot            string            `json:"mount_root"`
	ReadOnly             *bool             `json:"read_only"`
	Enabled              *bool             `json:"enabled"`
	HideAppleFiles       *bool             `json:"hide_apple_files"`
	MountProbeInterval   string            `json:"mount_probe_interval"`
	BackendProbeInterval string            `json:"backend_probe_interval"`
	VFS                  map[string]string `json:"vfs"`
}

// AccountView is one account as written plus the mounts that use it.
type AccountView struct {
	Name    string                `json:"name"`
	Public  []string              `json:"public"`
	Options map[string]string     `json:"options"`
	Secrets map[string]SecretView `json:"secrets"`
	UsedBy  []string              `json:"used_by"`
}

// MountView is one mount as written.
type MountView struct {
	Name                 string            `json:"name"`
	Account              string            `json:"account"`
	Type                 string            `json:"type"`
	Path                 string            `json:"path"`
	Options              map[string]string `json:"options"`
	MountPoint           string            `json:"mount_point"`
	ReadOnly             *bool             `json:"read_only"`
	Enabled              *bool             `json:"enabled"`
	HideAppleFiles       *bool             `json:"hide_apple_files"`
	MountProbeInterval   string            `json:"mount_probe_interval"`
	BackendProbeInterval string            `json:"backend_probe_interval"`
	VFS                  map[string]string `json:"vfs"`
}

// SecretView describes a secret source without its value.
type SecretView struct {
	Source  string   `json:"source"` // keychain | command
	Command []string `json:"command,omitempty"`
	Timeout string   `json:"timeout,omitempty"`
	Stored  *bool    `json:"stored,omitempty"` // keychain: item exists?
}

// AccountInput is what the UI sends to save an account.
type AccountInput struct {
	Name    string                 `json:"name"`
	Public  []string               `json:"public"`
	Options map[string]string      `json:"options"`
	Secrets map[string]SecretInput `json:"secrets"`
}

// MountInput is what the UI sends to save a mount. NewAccount, when set, is
// created in the same edit and the mount uses it (Add storage with a new
// account).
type MountInput struct {
	Name    string            `json:"name"`
	Account string            `json:"account"`
	Type    string            `json:"type"`
	Path    string            `json:"path"`
	Options map[string]string `json:"options"`
	// Public lists options of this mount that rclone marks sensitive and
	// the user keeps in the file anyway, typically the server; they are
	// added to the account's public list in the same edit.
	Public               []string          `json:"public"`
	MountPoint           string            `json:"mount_point"`
	ReadOnly             *bool             `json:"read_only"`
	Enabled              *bool             `json:"enabled"`
	HideAppleFiles       *bool             `json:"hide_apple_files"`
	MountProbeInterval   string            `json:"mount_probe_interval"`
	BackendProbeInterval string            `json:"backend_probe_interval"`
	VFS                  map[string]string `json:"vfs"`
	NewAccount           *AccountInput     `json:"new_account,omitempty"`
}

// SecretInput describes a secret source and, for the keychain, optionally a
// new value to store. An empty Value keeps the existing item.
type SecretInput struct {
	Source  string   `json:"source"` // keychain | command
	Value   string   `json:"value,omitempty"`
	Command []string `json:"command,omitempty"`
	Timeout string   `json:"timeout,omitempty"`
}

// GeneralInput carries [general] fields as strings; "" removes a key.
type GeneralInput map[string]string

// DefaultsInput carries [defaults] fields; VFS is written to [defaults.vfs].
type DefaultsInput struct {
	Fields map[string]string `json:"fields"`
	VFS    map[string]string `json:"vfs"`
}

// ValidationError is returned when the edited file would be invalid; nothing
// was written.
type ValidationError struct {
	Issues config.Issues
}

// invalid is a ValidationError about one field.
func invalid(path, msg string) error {
	return &ValidationError{Issues: config.Issues{{Path: path, Msg: msg}}}
}

func (e *ValidationError) Error() string {
	var parts []string
	for _, i := range e.Issues {
		if !i.Warning {
			parts = append(parts, i.Msg)
		}
	}
	return "config would be invalid: " + strings.Join(parts, "; ")
}

func (s *Service) env() (config.Env, error) {
	if s.Env.LookupEnv != nil {
		return s.Env, nil
	}
	return config.OSEnv()
}

// Load reads and validates the file for display.
func (s *Service) Load(ctx context.Context) (*View, error) {
	v := &View{Path: s.Path}
	real, err := filepath.EvalSymlinks(s.Path)
	if err == nil {
		v.RealPath = real
	}
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return nil, err
	}
	v.Exists = true
	doc, err := config.Parse(s.Path, raw)
	if err != nil {
		var is *config.Issue
		if errors.As(err, &is) {
			v.Issues = config.Issues{is}
			return v, nil
		}
		return nil, err
	}
	s.fill(ctx, v, doc)
	return v, nil
}

func (s *Service) fill(ctx context.Context, v *View, doc *config.Document) {
	f := doc.File
	g := f.General
	v.General = GeneralView{
		LogLevel: g.LogLevel, CacheDir: g.CacheDir, LaunchAtLogin: g.LaunchAtLogin,
		MountProbeInterval: durStr(g.MountProbeInterval), BackendProbeInterval: durStr(g.BackendProbeInterval), UploadStuckAfter: durStr(g.UploadStuckAfter),
	}
	v.Defaults = DefaultsView{
		MountRoot: f.Defaults.MountRoot, ReadOnly: f.Defaults.ReadOnly, Enabled: f.Defaults.Enabled, HideAppleFiles: f.Defaults.HideAppleFiles,
		MountProbeInterval: durStr(f.Defaults.MountProbeInterval), BackendProbeInterval: durStr(f.Defaults.BackendProbeInterval),
		VFS: stringMap(f.Defaults.VFS),
	}
	usedBy := map[string][]string{}
	for _, m := range f.Mounts {
		usedBy[m.Account] = append(usedBy[m.Account], m.Name)
	}
	stored := s.storedSecrets(ctx, f.Accounts)
	for _, a := range f.Accounts {
		av := AccountView{
			Name: a.Name, Public: a.Public, Options: stringMap(a.Options),
			Secrets: map[string]SecretView{}, UsedBy: usedBy[a.Name],
		}
		for opt, src := range a.Secrets {
			sv := SecretView{Source: src.Kind(), Command: src.Command, Timeout: durStr(src.Timeout)}
			if src.Kind() == "keychain" && s.Keychain != nil {
				ok := stored[secrets.Account(a.Name, opt)]
				sv.Stored = &ok
			}
			av.Secrets[opt] = sv
		}
		v.Accounts = append(v.Accounts, av)
	}
	for _, m := range f.Mounts {
		v.Mounts = append(v.Mounts, MountView{
			Name: m.Name, Account: m.Account, Type: m.Type, Path: m.Path, Options: stringMap(m.Options), MountPoint: m.MountPoint,
			ReadOnly: m.ReadOnly, Enabled: m.Enabled, HideAppleFiles: m.HideAppleFiles,
			MountProbeInterval: durStr(m.MountProbeInterval), BackendProbeInterval: durStr(m.BackendProbeInterval),
			VFS: stringMap(m.VFS),
		})
	}
	env, err := s.env()
	if err != nil {
		v.Issues = config.Issues{{Msg: err.Error()}}
		return
	}
	_, v.Issues = config.Validate(doc, s.Registry, env)
	v.Valid = !v.Issues.HasErrors()
}

// storedSecrets checks which keychain items of the accounts exist, all at
// once and without reading the secrets themselves.
func (s *Service) storedSecrets(ctx context.Context, accounts []config.Account) map[string]bool {
	out := map[string]bool{}
	if s.Keychain == nil {
		return out
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, a := range accounts {
		for opt, src := range a.Secrets {
			if src.Kind() != "keychain" {
				continue
			}
			item := secrets.Account(a.Name, opt)
			wg.Go(func() {
				cctx, cancel := context.WithTimeout(ctx, secrets.Timeout)
				defer cancel()
				ok, _ := s.Keychain.Exists(cctx, secrets.Service, item)
				mu.Lock()
				out[item] = ok
				mu.Unlock()
			})
		}
	}
	wg.Wait()
	return out
}

// Create writes a minimal config file when none exists.
func (s *Service) Create() error {
	if _, err := os.Stat(s.Path); err == nil {
		return nil
	}
	target := s.writeTarget()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	content := "# lagra configuration — see `lagra config example` for every option.\n\n[general]\nlog_level = \"info\"\n"
	return s.write(content)
}

// SaveAccount validates and writes one account. oldName is the account
// being edited ("" for a new one); a different Name renames it in place,
// points every mount that used it at the new name and moves its keychain
// items along.
func (s *Service) SaveAccount(ctx context.Context, oldName string, in AccountInput) error {
	in.Name = strings.TrimSpace(in.Name)
	src, err := s.read()
	if err != nil {
		return err
	}
	if err := checkName("account", in.Name, oldName, tomledit.Names(src, tomledit.Account)); err != nil {
		return err
	}
	a, pending, err := toAccount(in)
	if err != nil {
		return err
	}
	edited := src
	renamed := oldName != "" && oldName != in.Name
	if oldName == "" {
		edited = tomledit.Insert(edited, tomledit.Account, tomledit.RenderAccount(a))
	} else {
		edited = tomledit.Replace(edited, tomledit.Account, oldName, tomledit.RenderAccount(a))
	}
	if renamed {
		doc, err := config.Parse(s.Path, []byte(src))
		if err != nil {
			return err
		}
		for _, m := range doc.File.Mounts {
			if m.Account == oldName {
				edited = tomledit.SetBlockKey(edited, tomledit.Mount, m.Name, "account", tomledit.Quote(in.Name))
			}
		}
	}
	if err := s.validate(edited); err != nil {
		return err
	}
	// Keychain first: a written file pointing at missing items would leave
	// the mounts in Failed until the next save.
	if renamed && s.Keychain != nil {
		s.renameSecrets(ctx, oldName, in.Name, a)
	}
	if err := s.storeSecrets(ctx, in.Name, pending); err != nil {
		return err
	}
	if err := s.write(edited); err != nil {
		return err
	}
	if len(pending) > 0 && s.OnSecretsChanged != nil {
		s.OnSecretsChanged(in.Name)
	}
	return nil
}

// DeleteAccount removes an account no mount uses and, if asked, its
// keychain items.
func (s *Service) DeleteAccount(ctx context.Context, name string, deleteSecrets bool) error {
	src, err := s.read()
	if err != nil {
		return err
	}
	doc, err := config.Parse(s.Path, []byte(src))
	if err != nil {
		return err
	}
	var users []string
	for _, m := range doc.File.Mounts {
		if m.Account == name {
			users = append(users, m.Name)
		}
	}
	if len(users) > 0 {
		return invalid("name", fmt.Sprintf("still used by %s; remove or move those storages first", strings.Join(users, ", ")))
	}
	edited := tomledit.Remove(src, tomledit.Account, name)
	if edited == src {
		return fmt.Errorf("no account named %q", name)
	}
	if err := s.validate(edited); err != nil {
		return err
	}
	// The file first: if writing fails, the account keeps its credentials.
	if err := s.write(edited); err != nil {
		return err
	}
	if deleteSecrets && s.Keychain != nil {
		ctx, cancel := context.WithTimeout(ctx, secrets.Timeout)
		defer cancel()
		for _, a := range doc.File.Accounts {
			if a.Name != name {
				continue
			}
			for opt, sec := range a.Secrets {
				if sec.Kind() == "keychain" {
					_ = s.Keychain.Delete(ctx, secrets.Service, secrets.Account(name, opt))
				}
			}
		}
	}
	return nil
}

// SaveMount validates and writes one mount, and with in.NewAccount the
// account it uses, in one edit. oldName is the mount being edited ("" for a
// new one); a different Name renames it in place.
func (s *Service) SaveMount(ctx context.Context, oldName string, in MountInput) error {
	in.Name = strings.TrimSpace(in.Name)
	src, err := s.read()
	if err != nil {
		return err
	}
	if err := checkName("mount", in.Name, oldName, tomledit.Names(src, tomledit.Mount)); err != nil {
		return err
	}
	m, err := toMount(in)
	if err != nil {
		return err
	}
	edited := src
	var pending map[string]string
	if in.NewAccount != nil {
		in.NewAccount.Name = strings.TrimSpace(in.NewAccount.Name)
		if err := checkName("account", in.NewAccount.Name, "", tomledit.Names(src, tomledit.Account)); err != nil {
			return prefixed(err, "account.")
		}
		a, p, err := toAccount(*in.NewAccount)
		if err != nil {
			return prefixed(err, "account.")
		}
		a.Public = withPublic(a.Public, in.Public, m)
		edited = tomledit.Insert(edited, tomledit.Account, tomledit.RenderAccount(a))
		m.Account, pending = a.Name, p
	} else if len(in.Public) > 0 {
		doc, err := config.Parse(s.Path, []byte(src))
		if err != nil {
			return err
		}
		for _, a := range doc.File.Accounts {
			if pub := withPublic(a.Public, in.Public, m); a.Name == m.Account && len(pub) > len(a.Public) {
				edited = tomledit.SetBlockKey(edited, tomledit.Account, a.Name, "public", tomledit.QuoteList(pub))
			}
		}
	}
	key := oldName
	if key == "" {
		key = in.Name
	}
	edited = tomledit.Replace(edited, tomledit.Mount, key, tomledit.RenderMount(m))
	if err := s.validate(edited); err != nil {
		return err
	}
	if err := s.storeSecrets(ctx, m.Account, pending); err != nil {
		return err
	}
	return s.write(edited)
}

// withPublic adds to an account's public list the options of m listed in
// add, those the mount actually sets.
func withPublic(public, add []string, m config.Mount) []string {
	out := slices.Clone(public)
	for _, k := range add {
		if _, set := m.Options[k]; set && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// DeleteMount removes a mount. Its account stays.
func (s *Service) DeleteMount(ctx context.Context, name string) error {
	src, err := s.read()
	if err != nil {
		return err
	}
	edited := tomledit.Remove(src, tomledit.Mount, name)
	if edited == src {
		return fmt.Errorf("no mount named %q", name)
	}
	if err := s.validate(edited); err != nil {
		return err
	}
	return s.write(edited)
}

// SaveGeneral writes [general] keys.
func (s *Service) SaveGeneral(in GeneralInput) error {
	src, err := s.read()
	if err != nil {
		return err
	}
	edited := src
	for _, k := range slices.Sorted(maps.Keys(in)) {
		edited = tomledit.SetKey(edited, "general", k, renderGeneral(k, in[k]))
	}
	if err := s.validate(edited); err != nil {
		return err
	}
	return s.write(edited)
}

// SaveDefaults writes [defaults] and [defaults.vfs] keys.
func (s *Service) SaveDefaults(in DefaultsInput) error {
	src, err := s.read()
	if err != nil {
		return err
	}
	edited := src
	for _, k := range slices.Sorted(maps.Keys(in.Fields)) {
		edited = tomledit.SetKey(edited, "defaults", k, renderGeneral(k, in.Fields[k]))
	}
	for _, k := range slices.Sorted(maps.Keys(in.VFS)) {
		edited = tomledit.SetKey(edited, "defaults.vfs", k, renderOption(in.VFS[k]))
	}
	if err := s.validate(edited); err != nil {
		return err
	}
	return s.write(edited)
}

// ---- internals ----

// checkName rejects an empty name and one that another block of the same
// kind already has (unless it is the block being edited).
func checkName(kind, name, oldName string, existing []string) error {
	if name == "" {
		return invalid("name", "name is required")
	}
	if name == oldName {
		return nil
	}
	for _, n := range existing {
		if n == name {
			return invalid("name", fmt.Sprintf("an %s named %q already exists", kind, name))
		}
	}
	return nil
}

// prefixed puts prefix in front of every issue path of a ValidationError, so
// the UI can tell account fields from mount fields.
func prefixed(err error, prefix string) error {
	var ve *ValidationError
	if !errors.As(err, &ve) {
		return err
	}
	for i := range ve.Issues {
		ve.Issues[i].Path = prefix + ve.Issues[i].Path
	}
	return ve
}

func toAccount(in AccountInput) (config.Account, map[string]string, error) {
	a := config.Account{
		Name: in.Name, Public: in.Public,
		Options: map[string]any{}, Secrets: map[string]config.SecretSource{},
	}
	for k, v := range in.Options {
		if strings.TrimSpace(v) != "" {
			a.Options[k] = v
		}
	}
	pending := map[string]string{}
	for opt, sec := range in.Secrets {
		switch sec.Source {
		case "keychain":
			a.Secrets[opt] = config.SecretSource{Keychain: true}
			if sec.Value != "" {
				pending[opt] = sec.Value
			}
		case "command":
			var cmd []string
			for _, c := range sec.Command {
				if c != "" {
					cmd = append(cmd, c)
				}
			}
			if len(cmd) == 0 {
				return a, nil, invalid("secrets."+opt, "command must not be empty")
			}
			src := config.SecretSource{Command: cmd}
			var err error
			if src.Timeout, err = parseDur(sec.Timeout); err != nil {
				return a, nil, invalid("secrets."+opt+".timeout", err.Error())
			}
			a.Secrets[opt] = src
		case "", "none":
			// not a secret
		default:
			return a, nil, invalid("secrets."+opt, "unknown source "+sec.Source)
		}
	}
	return a, pending, nil
}

func toMount(in MountInput) (config.Mount, error) {
	m := config.Mount{
		Name: in.Name, Account: strings.TrimSpace(in.Account), Type: strings.TrimSpace(in.Type), Path: strings.TrimSpace(in.Path), MountPoint: strings.TrimSpace(in.MountPoint),
		ReadOnly: in.ReadOnly, Enabled: in.Enabled, HideAppleFiles: in.HideAppleFiles, Options: map[string]any{}, VFS: map[string]any{},
	}
	for k, v := range in.Options {
		if v = strings.TrimSpace(v); v != "" {
			m.Options[k] = v
		}
	}
	var err error
	if m.MountProbeInterval, err = parseDur(in.MountProbeInterval); err != nil {
		return m, invalid("mount_probe_interval", err.Error())
	}
	if m.BackendProbeInterval, err = parseDur(in.BackendProbeInterval); err != nil {
		return m, invalid("backend_probe_interval", err.Error())
	}
	for k, v := range in.VFS {
		if strings.TrimSpace(v) != "" {
			m.VFS[k] = v
		}
	}
	return m, nil
}

func (s *Service) validate(edited string) error {
	doc, err := config.Parse(s.Path, []byte(edited))
	if err != nil {
		var is *config.Issue
		if errors.As(err, &is) {
			return &ValidationError{Issues: config.Issues{is}}
		}
		return err
	}
	env, err := s.env()
	if err != nil {
		return err
	}
	_, issues := config.Validate(doc, s.Registry, env)
	if !issues.HasErrors() {
		return nil
	}
	ve := &ValidationError{}
	for _, is := range issues {
		if !is.Warning {
			ve.Issues = append(ve.Issues, is)
		}
	}
	return ve
}

func (s *Service) setSecret(ctx context.Context, account, opt, value string) error {
	if s.Keychain == nil {
		return errors.New("no keychain available")
	}
	cctx, cancel := context.WithTimeout(ctx, secrets.Timeout)
	defer cancel()
	return s.Keychain.Set(cctx, secrets.Service, secrets.Account(account, opt), value)
}

func (s *Service) storeSecrets(ctx context.Context, account string, values map[string]string) error {
	for _, opt := range slices.Sorted(maps.Keys(values)) {
		if err := s.setSecret(ctx, account, opt, values[opt]); err != nil {
			return fmt.Errorf("store secret %s: %w", opt, err)
		}
	}
	return nil
}

func (s *Service) renameSecrets(ctx context.Context, oldName, newName string, a config.Account) {
	ctx, cancel := context.WithTimeout(ctx, secrets.Timeout)
	defer cancel()
	for opt, sec := range a.Secrets {
		if sec.Kind() != "keychain" {
			continue
		}
		v, err := s.Keychain.Get(ctx, secrets.Service, secrets.Account(oldName, opt))
		if err != nil {
			continue
		}
		if err := s.Keychain.Set(ctx, secrets.Service, secrets.Account(newName, opt), v); err == nil {
			_ = s.Keychain.Delete(ctx, secrets.Service, secrets.Account(oldName, opt))
		}
	}
}

func (s *Service) read() (string, error) {
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("config file %s does not exist yet", s.Path)
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// writeTarget is the real file behind Path: following symlinks, or the link's
// target when it dangles, or Path itself.
func (s *Service) writeTarget() string {
	if real, err := filepath.EvalSymlinks(s.Path); err == nil {
		return real
	}
	if target, err := os.Readlink(s.Path); err == nil {
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(s.Path), target)
		}
		return target
	}
	return s.Path
}

// write replaces the file atomically (temp file + rename in the target's
// directory) without ever replacing a symlink with a regular file.
func (s *Service) write(content string) error {
	target := s.writeTarget()
	mode := os.FileMode(0o644)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}
	return atomicfile.Write(target, []byte(content), mode)
}

func renderGeneral(key, value string) string {
	if value == "" {
		return ""
	}
	switch key {
	case "launch_at_login", "read_only", "enabled", "hide_apple_files":
		if value == "true" || value == "false" {
			return value
		}
		return ""
	}
	return tomledit.Quote(value)
}

func renderOption(value string) string {
	if value == "" {
		return ""
	}
	return tomledit.Quote(value)
}

func parseDur(s string) (*config.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return nil, fmt.Errorf("invalid duration %q", s)
	}
	cd := config.Duration(d)
	return &cd, nil
}

func durStr(d *config.Duration) string {
	if d == nil {
		return ""
	}
	return d.Std().String()
}

// stringMap shows option values the way rclone gets them.
func stringMap(m map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		s, err := config.Stringify(v)
		if err != nil {
			s = fmt.Sprint(v)
		}
		out[k] = s
	}
	return out
}
