// Package rclone is the only place in lagra that imports rclone. It exposes
// backend/VFS option metadata to the config package and runs one NFS server
// per mount on top of rclone's VFS.
package rclone

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"

	"github.com/rclone/rclone/fs"
	rconfig "github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/vfs/vfscommon"

	"github.com/eltupe/lagra/internal/config"
)

// Registry implements config.Registry from rclone's live registry.
type Registry struct{}

// Backend implements config.Registry. Backends lagra does not offer count
// as unknown.
func (Registry) Backend(typ string) (config.OptionSet, bool) {
	ri, err := fs.Find(typ)
	if err != nil || !offered(ri) {
		return nil, false
	}
	return optionSet{typ: ri.Name, opts: ri.Options}, true
}

// VFS implements config.Registry.
func (Registry) VFS() config.OptionSet {
	return optionSet{opts: vfscommon.OptionsInfo}
}

// bucketBased are the object stores whose root lists buckets. rclone knows
// this as Features().BucketBased, but only from a live backend, which for
// most types means connecting; validation must not. See ADR 0001.
var bucketBased = map[string]bool{
	"s3": true, "azureblob": true, "b2": true, "swift": true,
	"oracleobjectstorage": true, "qingstor": true, "storj": true,
}

// NeedsBucket implements config.Registry.
func (Registry) NeedsBucket(typ string) bool {
	ri, err := fs.Find(typ)
	return err == nil && bucketBased[ri.Name]
}

// Option implements config.Registry across the offered backends.
func (Registry) Option(name string) (config.OptionInfo, bool) {
	var out config.OptionInfo
	found := false
	for _, ri := range fs.Registry {
		if !offered(ri) {
			continue
		}
		o := optionSet{typ: ri.Name, opts: ri.Options}.find(name)
		if o == nil {
			continue
		}
		info := toInfo(o)
		if !found {
			out = info
		}
		found = true
		out.Sensitive = out.Sensitive || info.Sensitive
		out.Password = out.Password || info.Password
	}
	return out, found
}

// serverHost are the backends whose host option is a bare server name:
// rclone appends ":port" and dials it. What macOS's "Connect to Server" and
// Windows show, smb://server/share or \\server\share, fails only when
// rclone connects, as "too many colons in address". See ADR 0001.
var serverHost = map[string]bool{"ftp": true, "sftp": true, "smb": true}

// checkHost rejects a host option of typ that is an address rather than a
// server name, and says which parts go where.
func checkHost(typ, value string) error {
	if !serverHost[typ] {
		return nil
	}
	rest, address := value, false
	if r, ok := strings.CutPrefix(rest, `\\`); ok {
		rest, address = strings.ReplaceAll(r, `\`, "/"), true
	} else if _, r, ok := strings.Cut(rest, "://"); ok {
		rest, address = r, true
	}
	host, path, hasPath := strings.Cut(rest, "/")
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host, address = host[i+1:], true
	}
	var port string
	// One colon followed by digits is a port; IPv6 addresses have several.
	if h, p, ok := strings.Cut(host, ":"); ok && p != "" && strings.Trim(p, "0123456789") == "" {
		host, port = h, p
	}
	if !address && !hasPath && port == "" {
		return nil
	}
	msg := fmt.Sprintf("option %q: %q is not a server name: enter %q", "host", value, host)
	if port != "" {
		msg += fmt.Sprintf(", with %s as option %q", port, "port")
	}
	if path = strings.Trim(path, "/"); path != "" {
		if p, err := url.PathUnescape(path); err == nil {
			path = p
		}
		if typ == "sftp" {
			path = "/" + path // sftp://server/srv is the absolute /srv
		}
		msg += fmt.Sprintf(", and %q as the mount's path", path)
	}
	return errors.New(msg)
}

type optionSet struct {
	typ  string // backend name; empty for the VFS layer
	opts fs.Options
}

func (s optionSet) find(name string) *fs.Option {
	for i := range s.opts {
		if s.opts[i].Name == name {
			return &s.opts[i]
		}
	}
	return nil
}

func (s optionSet) Lookup(name string) (config.OptionInfo, bool) {
	o := s.find(name)
	if o == nil {
		return config.OptionInfo{}, false
	}
	return toInfo(o), true
}

func (s optionSet) Options() []config.OptionInfo {
	out := make([]config.OptionInfo, 0, len(s.opts))
	for i := range s.opts {
		out = append(out, toInfo(&s.opts[i]))
	}
	return out
}

// Check parses value with the same code rclone uses to read config files, so
// what validate accepts, the backend accepts.
func (s optionSet) Check(name, value string) error {
	o := s.find(name)
	if o == nil {
		return fmt.Errorf("unknown option %q", name)
	}
	if _, err := configstruct.StringToInterface(o.GetValue(), value); err != nil {
		return fmt.Errorf("option %q: %q is not a valid %s", name, value, o.Type())
	}
	if o.Exclusive && len(o.Examples) > 0 && !slices.ContainsFunc(o.Examples, func(e fs.OptionExample) bool { return e.Value == value }) {
		return fmt.Errorf("option %q: %q is not one of the allowed values", name, value)
	}
	if name == "host" {
		return checkHost(s.typ, value)
	}
	return nil
}

func toInfo(o *fs.Option) config.OptionInfo {
	def, _ := configstruct.InterfaceToString(o.Default)
	info := config.OptionInfo{
		Name:      o.Name,
		Required:  o.Required,
		Sensitive: o.Sensitive || o.IsPassword,
		Password:  o.IsPassword,
		Advanced:  o.Advanced,
		Default:   def,
		Type:      o.Type(),
		Provider:  o.Provider,
		Exclusive: o.Exclusive,
		Help:      firstParagraph(o.Help),
	}
	for _, e := range o.Examples {
		info.Examples = append(info.Examples, e.Value)
		info.Choices = append(info.Choices, config.Choice{Value: e.Value, Help: firstParagraph(e.Help), Provider: e.Provider})
	}
	return info
}

func firstParagraph(help string) string {
	help, _, _ = strings.Cut(strings.TrimSpace(help), "\n\n")
	return strings.Join(strings.Fields(help), " ")
}

// BackendInfo names a backend for the UI.
type BackendInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Backends lists the offered backends with their descriptions, sorted.
func Backends() []BackendInfo {
	var out []BackendInfo
	for _, ri := range fs.Registry {
		if !offered(ri) {
			continue
		}
		out = append(out, BackendInfo{Name: ri.Name, Description: ri.Description})
	}
	slices.SortFunc(out, func(a, b BackendInfo) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// RouteLogs sends rclone's own log output, go-nfs's included, through the
// given slog handler instead of stderr.
func RouteLogs(h slog.Handler) {
	fs.SetLogger(expectedNFS{h})
}

// expectedNFS moves two go-nfs errors that describe normal events to
// debug, so that they do not bury real errors. Copying a file in Finder
// produces both: macOS creates with O_EXCL, which go-nfs does not support
// and answers NOTSUPP so that macOS retries without it; and the retry for
// the file's ._ sidecar is refused by the Apple filter.
type expectedNFS struct{ slog.Handler }

func (e expectedNFS) Handle(ctx context.Context, r slog.Record) error {
	if r.Level > slog.LevelDebug && isExpectedNFS(r.Message) {
		if !e.Handler.Enabled(ctx, slog.LevelDebug) {
			return nil
		}
		r.Level = slog.LevelDebug
	}
	return e.Handler.Handle(ctx, r)
}

func (e expectedNFS) WithAttrs(as []slog.Attr) slog.Handler {
	return expectedNFS{e.Handler.WithAttrs(as)}
}

func (e expectedNFS) WithGroup(name string) slog.Handler {
	return expectedNFS{e.Handler.WithGroup(name)}
}

func isExpectedNFS(msg string) bool {
	return msg == "failing create to indicate lack of support for 'exclusive' mode." ||
		strings.HasPrefix(msg, "Error Creating: "+refusedOp+" ")
}

// SetCacheDir sets the process-wide VFS cache root. rclone keeps one cache
// root per process and separates remotes beneath it.
func SetCacheDir(dir string) error {
	return rconfig.SetCacheDir(dir)
}
