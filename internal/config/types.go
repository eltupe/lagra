// Package config loads, validates, merges and diffs lagra's single TOML
// configuration file. It knows nothing about rclone; backend and VFS option
// metadata is supplied through the Registry interface.
package config

import (
	"fmt"
	"time"
)

// Duration is a time.Duration that unmarshals from TOML strings like "15s".
type Duration time.Duration

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", string(b), err)
	}
	if v < 0 {
		return fmt.Errorf("duration %q must not be negative", string(b))
	}
	*d = Duration(v)
	return nil
}

// Std returns the duration as time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// File is the raw shape of the TOML document. Optional fields are pointers so
// that "not set" can be told apart from a zero value when merging defaults.
type File struct {
	General  General       `toml:"general"`
	Defaults MountDefaults `toml:"defaults"`
	Accounts []Account     `toml:"account"`
	Mounts   []Mount       `toml:"mount"`
}

// General holds settings that are not tied to one mount. All of them apply
// on reload except cache_dir, which needs a restart.
type General struct {
	LogLevel             string    `toml:"log_level"`
	CacheDir             string    `toml:"cache_dir"`
	MountProbeInterval   *Duration `toml:"mount_probe_interval"`
	BackendProbeInterval *Duration `toml:"backend_probe_interval"`
	UploadStuckAfter     *Duration `toml:"upload_stuck_after"`
	LaunchAtLogin        *bool     `toml:"launch_at_login"`
}

// MountDefaults are merged into every mount. They cover lagra's own mount
// fields and VFS options only, never backend options.
type MountDefaults struct {
	MountRoot            string         `toml:"mount_root"`
	ReadOnly             *bool          `toml:"read_only"`
	Enabled              *bool          `toml:"enabled"`
	HideAppleFiles       *bool          `toml:"hide_apple_files"`
	MountProbeInterval   *Duration      `toml:"mount_probe_interval"`
	BackendProbeInterval *Duration      `toml:"backend_probe_interval"`
	VFS                  map[string]any `toml:"vfs"`
}

// Account is one [[account]] block: who you are, i.e. where the secrets
// come from and the options its mounts share, such as the user name. It is
// not tied to a backend: its mounts may use it over different protocols
// (SMB and WebDAV with one directory login), and each takes the options
// its own backend knows. Any number of mounts use one account.
type Account struct {
	Name    string                  `toml:"name"`
	Options map[string]any          `toml:"options"`
	Secrets map[string]SecretSource `toml:"secrets"`
	// Public lists options rclone marks as sensitive (host names, user
	// names) that you knowingly keep in the file, in the account or in its
	// mounts. Passwords and keys can never be made public.
	Public []string `toml:"public"`
}

// Mount is one [[mount]] block as written by the user: one Finder volume on
// top of an account, saying where and how (backend type, server, bucket or
// folder) the account's login is used.
type Mount struct {
	Name    string `toml:"name"`
	Account string `toml:"account"`
	Type    string `toml:"type"`
	Path    string `toml:"path"`
	// Options are backend options for this mount alone, typically the
	// server; they override the account's. Secrets stay in the account.
	Options    map[string]any `toml:"options"`
	MountPoint string         `toml:"mount_point"`
	ReadOnly   *bool          `toml:"read_only"`
	Enabled    *bool          `toml:"enabled"`
	// HideAppleFiles makes the NFS server refuse ._* AppleDouble sidecars,
	// .DS_Store and similar Finder litter, and hide existing ones. Default true.
	HideAppleFiles       *bool          `toml:"hide_apple_files"`
	MountProbeInterval   *Duration      `toml:"mount_probe_interval"`
	BackendProbeInterval *Duration      `toml:"backend_probe_interval"`
	VFS                  map[string]any `toml:"vfs"`
}

// SecretSource says where the value of one sensitive option comes from.
// Exactly one of Keychain or Command must be set.
type SecretSource struct {
	Keychain bool      `toml:"keychain"`
	Command  []string  `toml:"command"`
	Timeout  *Duration `toml:"timeout"`
}

// Kind returns "keychain", "command" or "" (unset / ambiguous is reported by validation).
func (s SecretSource) Kind() string {
	switch {
	case s.Keychain && len(s.Command) == 0:
		return "keychain"
	case !s.Keychain && len(s.Command) > 0:
		return "command"
	}
	return ""
}

// Built-in defaults. Anything the user does not set falls back to these.
const (
	DefaultLogLevel             = "info"
	DefaultCacheDir             = "~/Library/Caches/lagra"
	DefaultMountRoot            = "~/Volumes"
	DefaultMountProbeInterval   = 15 * time.Second
	DefaultBackendProbeInterval = 60 * time.Second
	DefaultUploadStuckAfter     = 10 * time.Minute
	DefaultSecretTimeout        = 10 * time.Second
)

// DefaultVFS are the VFS options lagra applies unless the user overrides them.
// Writing over NFS needs a write cache; "full" is what rclone recommends.
var DefaultVFS = map[string]string{
	"vfs_cache_mode": "full",
	"dir_cache_time": "1m",
}

// Effective is the fully merged, expanded, validated view of the config that
// the rest of lagra consumes. Values are concrete; nothing is optional.
type Effective struct {
	General  EffectiveGeneral
	Accounts []EffectiveAccount
	Mounts   []EffectiveMount
}

// EffectiveAccount is an Account with every option stringified the way
// rclone expects it.
type EffectiveAccount struct {
	Name    string
	Options map[string]string
	Secrets map[string]SecretSource
	Public  []string
}

// EffectiveGeneral is General with defaults applied and paths expanded.
type EffectiveGeneral struct {
	LogLevel             string
	CacheDir             string
	MountProbeInterval   time.Duration
	BackendProbeInterval time.Duration
	UploadStuckAfter     time.Duration
	LaunchAtLogin        bool
}

// EffectiveMount is a Mount with defaults merged and every value
// stringified the way rclone expects it. Options are the account's that
// its backend knows with the mount's own on top; Secrets are the account's
// secret sources its backend knows.
type EffectiveMount struct {
	Name                 string
	Account              string
	Type                 string
	Path                 string
	MountPoint           string
	ReadOnly             bool
	Enabled              bool
	HideAppleFiles       bool
	MountProbeInterval   time.Duration
	BackendProbeInterval time.Duration
	Options              map[string]string
	VFS                  map[string]string
	Secrets              map[string]SecretSource
}

// Account returns the account with the given name, if any.
func (e *Effective) Account(name string) (EffectiveAccount, bool) {
	for _, a := range e.Accounts {
		if a.Name == name {
			return a, true
		}
	}
	return EffectiveAccount{}, false
}

// Mount returns the mount with the given name, if any.
func (e *Effective) Mount(name string) (EffectiveMount, bool) {
	for _, m := range e.Mounts {
		if m.Name == name {
			return m, true
		}
	}
	return EffectiveMount{}, false
}
