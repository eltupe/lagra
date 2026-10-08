package config

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// NameRE is the pattern account and mount names must match. A mount name
// doubles as directory name, volume name and diff key; an account name as
// the prefix of its keychain items.
var NameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Env abstracts the process environment for tests.
type Env struct {
	LookupEnv func(string) (string, bool)
	Home      string
	// EvalSymlinks resolves symlinks in mount point directories; nil leaves
	// paths as written.
	EvalSymlinks func(string) (string, error)
}

// OSEnv returns the real environment.
func OSEnv() (Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Env{}, err
	}
	return Env{LookupEnv: os.LookupEnv, Home: home, EvalSymlinks: filepath.EvalSymlinks}, nil
}

var logLevels = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}

// Validate checks the whole document, merges defaults and returns the
// effective configuration. All problems are reported together; the effective
// config is only usable when Issues.HasErrors() is false.
func Validate(doc *Document, reg Registry, env Env) (*Effective, Issues) {
	v := &validator{doc: doc, reg: reg, env: env}
	eff := v.run()
	if v.issues.HasErrors() {
		eff = nil
	}
	return eff, v.issues.sorted()
}

type validator struct {
	doc    *Document
	reg    Registry
	env    Env
	issues Issues
}

func (v *validator) errf(path string, line int, format string, args ...any) {
	v.issues = append(v.issues, &Issue{Path: path, Line: line, Msg: fmt.Sprintf(format, args...)})
}

func (v *validator) warnf(path string, line int, format string, args ...any) {
	v.issues = append(v.issues, &Issue{Path: path, Line: line, Msg: fmt.Sprintf(format, args...), Warning: true})
}

// resolveParent resolves symlinks in the directories above a mount point,
// because the mount table lists the real path (/tmp/x is /private/tmp/x)
// and lagra recognises its volumes by comparing the two. The mount point
// itself is left alone: it may be a mounted, even hung, volume. Directories
// that do not exist yet are kept as written.
func (v *validator) resolveParent(mp string) string {
	if mp == "" || v.env.EvalSymlinks == nil {
		return mp
	}
	dir, rest := filepath.Dir(mp), []string{filepath.Base(mp)}
	for {
		if real, err := v.env.EvalSymlinks(dir); err == nil {
			return filepath.Join(append([]string{real}, rest...)...)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return mp
		}
		rest = append([]string{filepath.Base(dir)}, rest...)
		dir = parent
	}
}

func (v *validator) expand(p string) (string, error) {
	return ExpandPath(p, v.env.LookupEnv, v.env.Home)
}

// line finds keys for issue messages; top is for [general] and [defaults],
// block for the i-th [[account]] or [[mount]].
type line func(key string) int

func (v *validator) top(table string) line {
	return func(k string) int { return v.doc.lines.topKeyLine(table + "." + k) }
}

func (v *validator) block(kind string, i int) line {
	return func(k string) int { return v.doc.lines.blockKeyLine(kind, i, k) }
}

func (v *validator) run() *Effective {
	f := &v.doc.File
	loc := v.doc.lines

	moved := map[string]bool{}
	for _, k := range v.doc.undecoded {
		// A key in the wrong kind of block: point to the right one instead
		// of calling it unknown.
		seg := strings.SplitN(k, ".", 3)
		if len(seg) < 2 || misplaced[seg[0]+"."+seg[1]] == "" {
			v.errf(k, loc.anyKeyLine(k), "unknown key")
			continue
		}
		key := seg[0] + "." + seg[1]
		if moved[key] {
			continue
		}
		moved[key] = true
		line := loc.headerLine(key)
		blocks := len(f.Mounts)
		if seg[0] == "account" {
			blocks = len(f.Accounts)
		}
		for j := 0; line == 0 && j < blocks; j++ {
			line = loc.blockKeyLine(seg[0], j, seg[1])
		}
		v.errf(key, line, "%s", misplaced[key])
	}

	eff := &Effective{}
	eff.General = v.general(f.General)

	// Defaults
	defaults := v.top("defaults")
	mountRoot, err := v.expand(cmp.Or(f.Defaults.MountRoot, DefaultMountRoot))
	if err != nil {
		v.errf("defaults.mount_root", defaults("mount_root"), "%v", err)
	}
	defaultVFS := v.stringMap("defaults.vfs", defaults, f.Defaults.VFS)
	v.checkVFS("defaults.vfs", defaults, defaultVFS)
	v.checkInterval("defaults.mount_probe_interval", defaults, f.Defaults.MountProbeInterval)
	v.checkInterval("defaults.backend_probe_interval", defaults, f.Defaults.BackendProbeInterval)

	// Accounts. Invalid ones are still known by name, so that their mounts
	// are not also reported as pointing nowhere.
	accounts := map[string]*EffectiveAccount{}
	seenAccounts := map[string]int{}
	used := map[string]map[string]bool{} // account -> options its mounts' backends know
	for i, a := range f.Accounts {
		ea, ok := v.account(i, a)
		if a.Name == "" {
			continue
		}
		if j, dup := seenAccounts[a.Name]; dup {
			v.errf(fmt.Sprintf("account[%d].name", i), loc.blockKeyLine("account", i, "name"), "duplicate account name %q (also account[%d])", a.Name, j)
			continue
		}
		seenAccounts[a.Name] = i
		if ok {
			accounts[a.Name] = &ea
			eff.Accounts = append(eff.Accounts, ea)
		} else {
			accounts[a.Name] = nil
		}
	}

	// Mounts
	seenNames := map[string]int{}
	seenPoints := map[string]string{}
	for i, m := range f.Mounts {
		if used[m.Account] == nil {
			used[m.Account] = map[string]bool{}
		}
		em, ok := v.mount(i, m, accounts, used[m.Account], f.Defaults, eff.General, mountRoot, defaultVFS)
		if !ok {
			continue
		}
		if j, dup := seenNames[em.Name]; dup {
			v.errf(fmt.Sprintf("mount[%d].name", i), loc.blockKeyLine("mount", i, "name"), "duplicate mount name %q (also mount[%d])", em.Name, j)
		}
		seenNames[em.Name] = i
		if other, dup := seenPoints[em.MountPoint]; dup {
			v.errf(fmt.Sprintf("mount[%d].mount_point", i), loc.blockKeyLine("mount", i, "mount_point"), "mount point %s is also used by mount %q", em.MountPoint, other)
		}
		seenPoints[em.MountPoint] = em.Name
		eff.Mounts = append(eff.Mounts, em)
	}

	// An option none of an account's mounts can use is most likely a typo
	// or left over from a mount that is gone.
	for _, a := range eff.Accounts {
		u, i := used[a.Name], seenAccounts[a.Name]
		if u == nil {
			continue // no mounts yet
		}
		for _, kind := range []string{"options", "secrets"} {
			keys := slices.Collect(maps.Keys(a.Options))
			if kind == "secrets" {
				keys = slices.Collect(maps.Keys(a.Secrets))
			}
			for _, k := range slices.Sorted(slices.Values(keys)) {
				if !u[k] {
					v.warnf(fmt.Sprintf("account %q.%s.%s", a.Name, kind, k), loc.blockKeyLine("account", i, kind+"."+k), "%q is not an option of the backend of any mount that uses this account", k)
				}
			}
		}
	}
	return eff
}

// misplaced are keys in the wrong kind of block: the login is the
// account's, the backend and the server the mount's.
var misplaced = map[string]string{
	"mount.secrets": "secrets belong to the account: move them into the [[account]] block the mount names",
	"mount.public":  "public belongs to the account: move it into the [[account]] block the mount names",
	"account.type":  "type belongs to the mount: set it in each [[mount]] that uses this account",
}

func (v *validator) general(g General) EffectiveGeneral {
	line := v.top("general")
	out := EffectiveGeneral{
		LogLevel:             DefaultLogLevel,
		MountProbeInterval:   DefaultMountProbeInterval,
		BackendProbeInterval: DefaultBackendProbeInterval,
		UploadStuckAfter:     DefaultUploadStuckAfter,
	}
	if g.LogLevel != "" {
		if !logLevels[strings.ToLower(g.LogLevel)] {
			v.errf("general.log_level", line("log_level"), "must be one of debug, info, warn, error; got %q", g.LogLevel)
		} else {
			out.LogLevel = strings.ToLower(g.LogLevel)
		}
	}
	if p, err := v.expand(cmp.Or(g.CacheDir, DefaultCacheDir)); err != nil {
		v.errf("general.cache_dir", line("cache_dir"), "%v", err)
	} else {
		out.CacheDir = p
	}
	if v.checkInterval("general.mount_probe_interval", line, g.MountProbeInterval) && g.MountProbeInterval != nil {
		out.MountProbeInterval = g.MountProbeInterval.Std()
	}
	if v.checkInterval("general.backend_probe_interval", line, g.BackendProbeInterval) && g.BackendProbeInterval != nil {
		out.BackendProbeInterval = g.BackendProbeInterval.Std()
	}
	if v.checkInterval("general.upload_stuck_after", line, g.UploadStuckAfter) && g.UploadStuckAfter != nil {
		out.UploadStuckAfter = g.UploadStuckAfter.Std()
	}
	if g.LaunchAtLogin != nil {
		out.LaunchAtLogin = *g.LaunchAtLogin
	}
	return out
}

// checkInterval reports zero durations; nil (unset) is fine. Returns true when usable.
func (v *validator) checkInterval(path string, line line, d *Duration) bool {
	if d == nil {
		return true
	}
	if d.Std() <= 0 {
		v.errf(path, line(lastSegment(path)), "must be greater than 0")
		return false
	}
	return true
}

func lastSegment(path string) string {
	return path[strings.LastIndex(path, ".")+1:]
}

// stringMap converts a TOML table of scalars into rclone-style strings.
func (v *validator) stringMap(path string, line line, in map[string]any) map[string]string {
	out := make(map[string]string, len(in))
	for k, raw := range in {
		s, err := Stringify(raw)
		if err != nil {
			v.errf(path+"."+k, line(lastSegment(path)+"."+k), "%v", err)
			continue
		}
		out[k] = s
	}
	return out
}

func (v *validator) checkVFS(path string, line line, m map[string]string) {
	set := v.reg.VFS()
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if k == "read_only" {
			v.errf(path+"."+k, line("vfs."+k), "use read_only on the mount instead of the VFS option")
			continue
		}
		if _, ok := set.Lookup(k); !ok {
			v.errf(path+"."+k, line("vfs."+k), "unknown VFS option %q", k)
			continue
		}
		if err := set.Check(k, m[k]); err != nil {
			v.errf(path+"."+k, line("vfs."+k), "%v", err)
		}
	}
}

func (v *validator) account(i int, a Account) (EffectiveAccount, bool) {
	loc := v.doc.lines
	prefix := fmt.Sprintf("account[%d]", i)
	if a.Name != "" {
		prefix = fmt.Sprintf("account %q", a.Name)
	}
	pathOf := func(k string) string { return prefix + "." + k }
	line := v.block("account", i)
	before := len(v.issues)

	ea := EffectiveAccount{
		Name:    a.Name,
		Public:  a.Public,
		Secrets: map[string]SecretSource{},
	}

	switch {
	case a.Name == "":
		v.errf(pathOf("name"), loc.accountLine(i), "name is required")
	case !NameRE.MatchString(a.Name):
		v.errf(pathOf("name"), line("name"), "name %q must match %s", a.Name, NameRE)
	}

	// An account belongs to no backend: here an option only has to exist
	// in one, with the strictest flags any of them sets. Each mount checks
	// the values against its own backend.
	public := map[string]bool{}
	for _, k := range a.Public {
		public[k] = true
		info, ok := v.reg.Option(k)
		switch {
		case !ok:
			v.errf(pathOf("public"), line("public"), "unknown option %q: no backend has it", k)
		case info.Password:
			v.errf(pathOf("public"), line("public"), "%q is a password and can never be public", k)
		case !info.Sensitive:
			v.warnf(pathOf("public"), line("public"), "%q is not sensitive; listing it in public has no effect", k)
		}
	}
	ea.Options = v.stringMap(pathOf("options"), line, a.Options)
	v.checkOptions(pathOf, line, v.reg.Option, nil, "no backend has it", ea.Options, public)

	// Secrets
	for _, k := range slices.Sorted(maps.Keys(a.Secrets)) {
		src := a.Secrets[k]
		p := pathOf("secrets." + k)
		if _, ok := v.reg.Option(k); !ok {
			v.errf(p, line("secrets."+k), "unknown option %q: no backend has it", k)
			continue
		}
		if _, dup := ea.Options[k]; dup {
			v.errf(p, line("secrets."+k), "%q is set both in options and in secrets", k)
			continue
		}
		switch src.Kind() {
		case "":
			v.errf(p, line("secrets."+k), "set exactly one of keychain = true or command = [...]")
			continue
		case "command":
			if src.Command[0] == "" {
				v.errf(p, line("secrets."+k), "command must not be empty")
				continue
			}
		}
		if src.Timeout != nil && src.Timeout.Std() <= 0 {
			v.errf(p+".timeout", line("secrets."+k), "timeout must be greater than 0")
		}
		ea.Secrets[k] = src
	}

	// Required options are checked per mount: the account leaves the
	// server, and the backend, to them.
	return ea, !v.issues[before:].HasErrors()
}

// checkOptions checks backend options kept in the file, an account's or a
// mount's: known (to lookup), no password, sensitive ones only when the
// account lists them as public, and values check accepts (nil for an
// account, whose values each mount checks).
func (v *validator) checkOptions(pathOf func(string) string, line line, lookup func(string) (OptionInfo, bool), check func(name, value string) error, unknown string, opts map[string]string, public map[string]bool) {
	for _, k := range slices.Sorted(maps.Keys(opts)) {
		p, l := pathOf("options."+k), line("options."+k)
		info, ok := lookup(k)
		switch {
		case !ok:
			v.errf(p, l, "unknown option %q: %s", k, unknown)
		case info.Password:
			v.errf(p, l, "%q is a password and must come from [account.secrets], never from the config file", k)
		case info.Sensitive && !public[k]:
			v.errf(p, l, "%q is marked sensitive by rclone; move it to [account.secrets] or list it in the account's public = [%q] to keep it in the file", k, k)
		case check != nil:
			if err := check(k, opts[k]); err != nil {
				v.errf(p, l, "%v", err)
			}
		}
	}
}

func (v *validator) mount(i int, m Mount, accounts map[string]*EffectiveAccount, used map[string]bool, d MountDefaults, g EffectiveGeneral, mountRoot string, defaultVFS map[string]string) (EffectiveMount, bool) {
	loc := v.doc.lines
	prefix := fmt.Sprintf("mount[%d]", i)
	if m.Name != "" {
		prefix = fmt.Sprintf("mount %q", m.Name)
	}
	pathOf := func(k string) string { return prefix + "." + k }
	line := v.block("mount", i)
	before := len(v.issues)

	em := EffectiveMount{
		Name:                 m.Name,
		Account:              m.Account,
		Path:                 m.Path,
		Enabled:              true,
		MountProbeInterval:   g.MountProbeInterval,
		BackendProbeInterval: g.BackendProbeInterval,
		Options:              map[string]string{},
		VFS:                  map[string]string{},
		Secrets:              map[string]SecretSource{},
	}

	switch {
	case m.Name == "":
		v.errf(pathOf("name"), loc.mountLine(i), "name is required")
	case !NameRE.MatchString(m.Name):
		v.errf(pathOf("name"), line("name"), "name %q must match %s", m.Name, NameRE)
	}

	// Backend: what this mount speaks.
	var backend OptionSet
	if m.Type == "" {
		v.errf(pathOf("type"), loc.mountLine(i), "type is required (a backend such as \"smb\", see `lagra backends`)")
	} else if b, ok := v.reg.Backend(m.Type); !ok {
		v.errf(pathOf("type"), line("type"), "unsupported backend type %q (see `lagra backends`)", m.Type)
	} else {
		backend, em.Type = b, m.Type
		// An object store's root is the list of its buckets; a mount shows
		// one bucket (or a folder in it).
		if v.reg.NeedsBucket(m.Type) && strings.Trim(m.Path, "/ ") == "" {
			v.errf(pathOf("path"), cmp.Or(line("path"), loc.mountLine(i)), "name the bucket to mount, e.g. path = \"my-bucket\" or \"my-bucket/folder\"")
		}
	}

	// Account: the login, and options the account's mounts share.
	acct, known := accounts[m.Account]
	switch {
	case m.Account == "":
		v.errf(pathOf("account"), loc.mountLine(i), "account is required (the name of an [[account]] block)")
	case !known:
		v.errf(pathOf("account"), line("account"), "unknown account %q", m.Account)
	case acct != nil && backend != nil:
		v.mountOptions(pathOf, line, loc.mountLine(i), backend, m, acct, used, &em)
	}

	// Mount point
	mp := m.MountPoint
	if mp == "" {
		if m.Name != "" && mountRoot != "" {
			mp = filepath.Join(mountRoot, m.Name)
		}
	} else if p, err := v.expand(mp); err != nil {
		v.errf(pathOf("mount_point"), line("mount_point"), "%v", err)
		mp = ""
	} else {
		mp = p
	}
	if mp != "" {
		if !filepath.IsAbs(mp) {
			v.errf(pathOf("mount_point"), line("mount_point"), "must be an absolute path after expansion; got %s", mp)
		} else if mp == "/Volumes" || strings.HasPrefix(mp, "/Volumes/") {
			v.warnf(pathOf("mount_point"), line("mount_point"), "mount points under /Volumes need root; prefer a directory under your home")
		}
	}
	em.MountPoint = v.resolveParent(mp)

	// Booleans and intervals: mount overrides defaults overrides built-in.
	em.ReadOnly = pick(m.ReadOnly, d.ReadOnly, false)
	em.Enabled = pick(m.Enabled, d.Enabled, true)
	em.HideAppleFiles = pick(m.HideAppleFiles, d.HideAppleFiles, true)
	if v.checkInterval(pathOf("mount_probe_interval"), line, m.MountProbeInterval) {
		if m.MountProbeInterval != nil {
			em.MountProbeInterval = m.MountProbeInterval.Std()
		} else if d.MountProbeInterval != nil && d.MountProbeInterval.Std() > 0 {
			em.MountProbeInterval = d.MountProbeInterval.Std()
		}
	}
	if v.checkInterval(pathOf("backend_probe_interval"), line, m.BackendProbeInterval) {
		if m.BackendProbeInterval != nil {
			em.BackendProbeInterval = m.BackendProbeInterval.Std()
		} else if d.BackendProbeInterval != nil && d.BackendProbeInterval.Std() > 0 {
			em.BackendProbeInterval = d.BackendProbeInterval.Std()
		}
	}

	// VFS: built-in < defaults < mount
	mountVFS := v.stringMap(pathOf("vfs"), line, m.VFS)
	v.checkVFS(pathOf("vfs"), line, mountVFS)
	maps.Copy(em.VFS, DefaultVFS)
	maps.Copy(em.VFS, defaultVFS)
	maps.Copy(em.VFS, mountVFS)

	return em, !v.issues[before:].HasErrors()
}

// mountOptions gives the mount the account's options and secret sources
// its backend knows (the others serve the account's mounts of other
// types, and are marked in used), adds the mount's own options on top and
// checks that the backend gets every option it requires.
func (v *validator) mountOptions(pathOf func(string) string, line line, blockLine int, backend OptionSet, m Mount, acct *EffectiveAccount, used map[string]bool, em *EffectiveMount) {
	for _, k := range slices.Sorted(maps.Keys(acct.Options)) {
		if _, ok := backend.Lookup(k); !ok {
			continue
		}
		used[k] = true
		if err := backend.Check(k, acct.Options[k]); err != nil {
			v.errf(pathOf("options."+k), blockLine, "from account %q: %v", acct.Name, err)
			continue
		}
		em.Options[k] = acct.Options[k]
	}
	for k, src := range acct.Secrets {
		if _, ok := backend.Lookup(k); ok {
			used[k] = true
			em.Secrets[k] = src
		}
	}

	own := v.stringMap(pathOf("options"), line, m.Options)
	for _, k := range slices.Sorted(maps.Keys(own)) {
		if _, secret := em.Secrets[k]; secret {
			v.errf(pathOf("options."+k), line("options."+k), "%q comes from the secrets of account %q; remove it here", k, acct.Name)
			delete(own, k)
		}
	}
	public := map[string]bool{}
	for _, k := range acct.Public {
		public[k] = true
	}
	v.checkOptions(pathOf, line, backend.Lookup, backend.Check, fmt.Sprintf("backend %q has no such option", m.Type), own, public)
	maps.Copy(em.Options, own)

	for _, info := range backend.Options() {
		if !info.Required || info.Default != "" || !MatchProvider(info.Provider, em.Options["provider"]) {
			continue
		}
		if _, ok := em.Options[info.Name]; ok {
			continue
		}
		if _, ok := em.Secrets[info.Name]; ok {
			continue
		}
		v.errf(pathOf("options."+info.Name), blockLine, "backend %q requires option %q: set it in [mount.options] or in account %q", m.Type, info.Name, acct.Name)
	}
}

func pick(mount, def *bool, builtin bool) bool {
	if mount != nil {
		return *mount
	}
	if def != nil {
		return *def
	}
	return builtin
}

// MatchProvider mirrors rclone's provider filter: "" matches everything,
// "A,B" matches A or B, "!A,B" matches anything but A or B.
func MatchProvider(filter, provider string) bool {
	if filter == "" {
		return true
	}
	list, negate := strings.CutPrefix(filter, "!")
	found := slices.ContainsFunc(strings.Split(list, ","), func(p string) bool { return strings.TrimSpace(p) == provider })
	return found != negate
}

// SecretTimeout returns the timeout for a secret command.
func (s SecretSource) SecretTimeout() time.Duration {
	if s.Timeout != nil && s.Timeout.Std() > 0 {
		return s.Timeout.Std()
	}
	return DefaultSecretTimeout
}
