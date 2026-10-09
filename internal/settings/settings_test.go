package settings

import (
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eltupe/lagra/internal/config"
	"github.com/eltupe/lagra/internal/secrets"
)

type memKeychain struct{ items map[string]string }

func (m *memKeychain) Get(ctx context.Context, service, account string) (string, error) {
	v, ok := m.items[account]
	if !ok {
		return "", secrets.ErrNotFound
	}
	return v, nil
}
func (m *memKeychain) Exists(ctx context.Context, service, account string) (bool, error) {
	_, ok := m.items[account]
	return ok, nil
}
func (m *memKeychain) Set(ctx context.Context, service, account, value string) error {
	m.items[account] = value
	return nil
}
func (m *memKeychain) Delete(ctx context.Context, service, account string) error {
	delete(m.items, account)
	return nil
}

func registry() *config.StaticRegistry {
	return &config.StaticRegistry{
		Backends: map[string][]config.OptionInfo{
			"s3": {
				{Name: "provider", Type: "string"}, {Name: "region", Type: "string"}, {Name: "endpoint", Type: "string"},
				{Name: "access_key_id", Type: "string", Sensitive: true}, {Name: "secret_access_key", Type: "string", Sensitive: true},
			},
			"smb": {
				{Name: "host", Type: "string", Required: true, Sensitive: true}, {Name: "user", Type: "string", Sensitive: true},
				{Name: "domain", Type: "string", Sensitive: true}, {Name: "pass", Type: "string", Sensitive: true, Password: true},
			},
			"webdav": {
				{Name: "url", Type: "string", Required: true}, {Name: "user", Type: "string", Sensitive: true},
				{Name: "pass", Type: "string", Sensitive: true, Password: true},
			},
		},
		VFSOpts:     []config.OptionInfo{{Name: "vfs_cache_mode", Type: "string"}, {Name: "dir_cache_time", Type: "Duration"}},
		BucketBased: map[string]bool{"s3": true},
	}
}

func newService(t *testing.T, content string) (*Service, *memKeychain, string) {
	t.Helper()
	repo := t.TempDir()
	cfgDir := t.TempDir()
	target := filepath.Join(repo, "config.toml")
	if content != "" {
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(cfgDir, "config.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	kc := &memKeychain{items: map[string]string{}}
	s := &Service{Path: link, Registry: registry(), Keychain: kc, Env: config.Env{Home: "/Users/t", LookupEnv: func(string) (string, bool) { return "", false }}}
	return s, kc, target
}

const base = `# hand-written header
[general]
log_level = "info"  # note

[[account]]
name = "aws"
# my comment
[account.options]
provider = "AWS"
[account.secrets]
access_key_id = { keychain = true }
secret_access_key = { keychain = true }

[[mount]]
name = "archiv"
account = "aws"
type = "s3"
path = "bucket"  # main bucket

[[mount]]
name = "photos"
account = "aws"
type = "s3"
path = "photos"
`

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestLoad(t *testing.T) {
	s, kc, _ := newService(t, base)
	kc.items["aws/access_key_id"] = "x"
	v, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !v.Exists || !v.Valid || len(v.Accounts) != 1 || len(v.Mounts) != 2 || v.RealPath == "" {
		t.Fatalf("view = %+v", v)
	}
	a := v.Accounts[0]
	if a.Options["provider"] != "AWS" || strings.Join(a.UsedBy, ",") != "archiv,photos" {
		t.Errorf("account = %+v", a)
	}
	if !*a.Secrets["access_key_id"].Stored || *a.Secrets["secret_access_key"].Stored {
		t.Errorf("stored flags = %+v", a.Secrets)
	}
	if m := v.Mounts[0]; m.Account != "aws" || m.Path != "bucket" {
		t.Errorf("mount = %+v", m)
	}
}

func TestSaveAccountWritesThroughSymlinkAndKeepsComments(t *testing.T) {
	s, kc, target := newService(t, base)
	var changed []string
	s.OnSecretsChanged = func(a string) { changed = append(changed, a) }
	err := s.SaveAccount(context.Background(), "aws", AccountInput{
		Name:    "aws",
		Options: map[string]string{"provider": "Other", "endpoint": "https://e", "region": ""},
		Secrets: map[string]SecretInput{
			"access_key_id":     {Source: "keychain", Value: "AKIA"},
			"secret_access_key": {Source: "command", Command: []string{"op", "read", "x"}, Timeout: "20s"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(s.Path); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced by a regular file")
	}
	out := read(t, target)
	for _, want := range []string{"# hand-written header", `log_level = "info"  # note`, `endpoint = "https://e"`, `secret_access_key = { command = ["op", "read", "x"], timeout = "20s" }`, `access_key_id = { keychain = true }`, `path = "bucket"  # main bucket`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "region") || strings.Contains(out, "# my comment") {
		t.Errorf("empty option or old block comment survived:\n%s", out)
	}
	if kc.items["aws/access_key_id"] != "AKIA" || strings.Contains(out, "AKIA") {
		t.Errorf("secret not stored or leaked: %v", kc.items)
	}
	if strings.Join(changed, ",") != "aws" {
		t.Errorf("OnSecretsChanged = %v", changed)
	}
}

func TestSaveAccountRejectsInvalidWithoutWriting(t *testing.T) {
	s, _, target := newService(t, base)
	before := read(t, target)
	err := s.SaveAccount(context.Background(), "aws", AccountInput{Name: "aws",
		Options: map[string]string{"secret_access_key": "plain"}})
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Issues) == 0 {
		t.Fatalf("want ValidationError, got %v", err)
	}
	if read(t, target) != before {
		t.Error("file changed despite validation error")
	}
	err = s.SaveAccount(context.Background(), "", AccountInput{Name: "aws"})
	if !errors.As(err, &ve) || !strings.Contains(ve.Issues[0].Msg, "already exists") {
		t.Errorf("duplicate name: %v", err)
	}
}

// Renaming an account moves its keychain items and points its mounts at the
// new name, in place.
func TestRenameAccount(t *testing.T) {
	s, kc, target := newService(t, base)
	kc.items["aws/access_key_id"] = "id"
	kc.items["aws/secret_access_key"] = "sec"
	err := s.SaveAccount(context.Background(), "aws", AccountInput{Name: "amazon",
		Options: map[string]string{"provider": "AWS"},
		Secrets: map[string]SecretInput{"access_key_id": {Source: "keychain"}, "secret_access_key": {Source: "keychain"}}})
	if err != nil {
		t.Fatal(err)
	}
	if kc.items["amazon/access_key_id"] != "id" || kc.items["amazon/secret_access_key"] != "sec" || len(kc.items) != 2 {
		t.Errorf("keychain after rename: %v", kc.items)
	}
	out := read(t, target)
	if strings.Contains(out, `"aws"`) || strings.Count(out, `account = "amazon"`) != 2 || !strings.Contains(out, "log_level = \"info\"  # note\n\n[[account]]\nname = \"amazon\"") {
		t.Errorf("file after rename:\n%s", out)
	}
}

func TestDeleteAccount(t *testing.T) {
	s, kc, target := newService(t, base+"\n[[account]]\nname = \"spare\"\n[account.secrets]\naccess_key_id = { keychain = true }\n")
	kc.items["spare/access_key_id"] = "id"
	kc.items["aws/access_key_id"] = "keep"
	err := s.DeleteAccount(context.Background(), "aws", true)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Issues[0].Msg, "still used by archiv, photos") {
		t.Fatalf("deleting a used account must be refused, got %v", err)
	}
	if err := s.DeleteAccount(context.Background(), "spare", true); err != nil {
		t.Fatal(err)
	}
	out := read(t, target)
	if strings.Contains(out, "spare") || !strings.Contains(out, `name = "aws"`) {
		t.Errorf("after delete:\n%s", out)
	}
	if _, ok := kc.items["spare/access_key_id"]; ok || kc.items["aws/access_key_id"] != "keep" {
		t.Errorf("keychain after delete: %v", kc.items)
	}
	if err := s.DeleteAccount(context.Background(), "nope", false); err == nil {
		t.Error("unknown account must fail")
	}
}

func TestSaveMount(t *testing.T) {
	s, _, target := newService(t, base)
	ro := true
	if err := s.SaveMount(context.Background(), "archiv", MountInput{Name: "archiv", Account: "aws", Type: "s3", Path: "bucket/sub", ReadOnly: &ro,
		VFS: map[string]string{"dir_cache_time": "5m"}}); err != nil {
		t.Fatal(err)
	}
	out := read(t, target)
	if !strings.Contains(out, "[[mount]]\nname = \"archiv\"\naccount = \"aws\"\ntype = \"s3\"\npath = \"bucket/sub\"\nread_only = true\n[mount.vfs]\ndir_cache_time = \"5m\"\n\n[[mount]]\nname = \"photos\"") {
		t.Errorf("mount not replaced in place:\n%s", out)
	}
	// rename in place
	if err := s.SaveMount(context.Background(), "archiv", MountInput{Name: "archive", Account: "aws", Type: "s3", Path: "bucket"}); err != nil {
		t.Fatal(err)
	}
	if out := read(t, target); strings.Contains(out, `"archiv"`) || strings.Index(out, `"archive"`) > strings.Index(out, `"photos"`) {
		t.Errorf("rename:\n%s", out)
	}
	before := read(t, target)
	var ve *ValidationError
	for _, in := range []MountInput{
		{Name: "photos", Account: "aws", Type: "s3", Path: "x"}, // duplicate
		{Name: "c", Account: "nope", Type: "s3", Path: "x"},     // unknown account
		{Name: "c", Account: "aws", Type: "s3"},                 // no bucket (s3 needs one)
		{Name: "c", Account: "aws", Path: "x"},                  // no type
	} {
		if err := s.SaveMount(context.Background(), "", in); !errors.As(err, &ve) {
			t.Errorf("%+v: want ValidationError, got %v", in, err)
		}
	}
	if read(t, target) != before {
		t.Error("file changed despite validation errors")
	}
}

// A form filled from an older version of the file must not write over a
// change made in an editor since.
func TestEditRefusesAStaleVersion(t *testing.T) {
	s, _, target := newService(t, base)
	v, err := s.Load(context.Background())
	if err != nil || v.FileVersion == "" {
		t.Fatalf("load: %v, version %q", err, v.FileVersion)
	}
	edited := strings.Replace(base, `path = "photos"`, `path = "photos/2026"`, 1)
	if err := os.WriteFile(target, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	ran := false
	if _, err := s.Edit(v.FileVersion, func() error { ran = true; return nil }); !errors.Is(err, ErrConflict) || ran {
		t.Fatalf("stale version: err %v, ran %v", err, ran)
	}
	v, _ = s.Load(context.Background())
	got, err := s.Edit(v.FileVersion, func() error {
		return s.SaveMount(context.Background(), "photos", MountInput{Name: "photos", Account: "aws", Type: "s3", Path: "photos/2026", Enabled: new(false)})
	})
	if err != nil {
		t.Fatal(err)
	}
	if now, _ := s.Load(context.Background()); got != now.FileVersion || got == v.FileVersion {
		t.Errorf("Edit returned %q, file is at %q, was %q", got, now.FileVersion, v.FileVersion)
	}
	if _, err := s.Edit("", func() error { return nil }); err != nil {
		t.Errorf("no version, no check: %v", err)
	}
}

// Add storage with "New account…": both blocks in one edit, the account
// before the mounts, its secret in the keychain.
func TestSaveMountWithNewAccount(t *testing.T) {
	s, kc, target := newService(t, base)
	err := s.SaveMount(context.Background(), "", MountInput{Name: "backup", Type: "s3", Path: "backup",
		NewAccount: &AccountInput{Name: "hetzner", Options: map[string]string{"provider": "Other", "endpoint": "https://h"},
			Secrets: map[string]SecretInput{"access_key_id": {Source: "keychain", Value: "HK"}, "secret_access_key": {Source: "keychain", Value: "HS"}}}})
	if err != nil {
		t.Fatal(err)
	}
	out := read(t, target)
	if !strings.Contains(out, "secret_access_key = { keychain = true }\n\n[[account]]\nname = \"hetzner\"") || !strings.HasSuffix(out, "[[mount]]\nname = \"backup\"\naccount = \"hetzner\"\ntype = \"s3\"\npath = \"backup\"\n") {
		t.Errorf("file:\n%s", out)
	}
	if kc.items["hetzner/access_key_id"] != "HK" || kc.items["hetzner/secret_access_key"] != "HS" {
		t.Errorf("keychain: %v", kc.items)
	}
	// an existing account name is refused, with the field marked as the account's
	err = s.SaveMount(context.Background(), "", MountInput{Name: "x", Type: "s3", Path: "x", NewAccount: &AccountInput{Name: "aws"}})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Issues[0].Path != "account.name" {
		t.Errorf("duplicate new account: %v", err)
	}
}

// One login for several servers and protocols: the account holds user,
// domain and password, each storage its backend and server. A server typed
// into a storage is added to the account's public list.
func TestSaveMountsWithTheirServer(t *testing.T) {
	s, kc, target := newService(t, base)
	login := &AccountInput{Name: "corp", Public: []string{"user", "domain"},
		Options: map[string]string{"user": "alice", "domain": "CORP"}, Secrets: map[string]SecretInput{"pass": {Source: "keychain", Value: "pw"}}}
	if err := s.SaveMount(context.Background(), "", MountInput{Name: "team", Type: "smb", Path: "Team$", NewAccount: login,
		Options: map[string]string{"host": "fs1.example.org", "domain": " "}, Public: []string{"host"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMount(context.Background(), "", MountInput{Name: "home", Account: "corp", Type: "smb", Path: "home/alice",
		Options: map[string]string{"host": "fs2.example.org"}, Public: []string{"host"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMount(context.Background(), "", MountInput{Name: "web", Account: "corp", Type: "webdav",
		Options: map[string]string{"url": "https://dav.example.org/files"}}); err != nil {
		t.Fatal(err)
	}
	out := read(t, target)
	if !strings.Contains(out, "[[account]]\nname = \"corp\"\npublic = [\"user\", \"domain\", \"host\"]\n[account.options]\ndomain = \"CORP\"\nuser = \"alice\"\n[account.secrets]\npass = { keychain = true }\n") {
		t.Errorf("account:\n%s", out)
	}
	if !strings.Contains(out, "[[mount]]\nname = \"team\"\naccount = \"corp\"\ntype = \"smb\"\npath = \"Team$\"\n[mount.options]\nhost = \"fs1.example.org\"\n") ||
		!strings.Contains(out, "[[mount]]\nname = \"home\"\naccount = \"corp\"\ntype = \"smb\"\npath = \"home/alice\"\n[mount.options]\nhost = \"fs2.example.org\"\n") ||
		!strings.Contains(out, "[[mount]]\nname = \"web\"\naccount = \"corp\"\ntype = \"webdav\"\npath = \"\"\n[mount.options]\nurl = \"https://dav.example.org/files\"\n") {
		t.Errorf("mounts:\n%s", out)
	}
	if len(kc.items) != 1 || kc.items["corp/pass"] != "pw" {
		t.Errorf("one password for both servers and both protocols, got %v", kc.items)
	}
	v, err := s.Load(context.Background())
	if err != nil || !v.Valid {
		t.Fatalf("load: %v %v", err, v.Issues)
	}
	for _, m := range v.Mounts {
		if m.Name == "home" && m.Options["host"] != "fs2.example.org" {
			t.Errorf("view of home = %+v", m)
		}
	}
	// A storage without a server is refused at its own field.
	err = s.SaveMount(context.Background(), "", MountInput{Name: "x", Account: "corp", Type: "smb", Path: "x"})
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.HasSuffix(ve.Issues[0].Path, ".options.host") {
		t.Errorf("missing server: %v", err)
	}
}

func TestDeleteMountKeepsAccount(t *testing.T) {
	s, kc, target := newService(t, base)
	kc.items["aws/access_key_id"] = "id"
	if err := s.DeleteMount(context.Background(), "archiv"); err != nil {
		t.Fatal(err)
	}
	out := read(t, target)
	if strings.Contains(out, `name = "archiv"`) || !strings.Contains(out, `name = "aws"`) || !strings.Contains(out, "# hand-written header") {
		t.Errorf("after delete:\n%s", out)
	}
	if kc.items["aws/access_key_id"] != "id" {
		t.Errorf("account secrets must stay: %v", kc.items)
	}
	if err := s.DeleteMount(context.Background(), "nope"); err == nil {
		t.Error("unknown mount must fail")
	}
}

func TestSaveGeneralAndDefaults(t *testing.T) {
	s, _, target := newService(t, base)
	if err := s.SaveGeneral(GeneralInput{"log_level": "debug", "launch_at_login": "true", "cache_dir": ""}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveDefaults(DefaultsInput{Fields: map[string]string{"mount_root": "~/mnt", "read_only": "true"}, VFS: map[string]string{"vfs_cache_mode": "writes"}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(target)
	out := string(raw)
	for _, want := range []string{`log_level = "debug"  # note`, "launch_at_login = true", "[defaults]\nmount_root = \"~/mnt\"\nread_only = true", "[defaults.vfs]\nvfs_cache_mode = \"writes\""} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if err := s.SaveGeneral(GeneralInput{"log_level": "loud"}); err == nil {
		t.Error("invalid log level must be rejected")
	}
}

func TestCreateThroughDanglingSymlink(t *testing.T) {
	s, _, target := newService(t, "")
	if err := s.Create(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target not created: %v", err)
	}
	v, err := s.Load(context.Background())
	if err != nil || !v.Exists || !v.Valid {
		t.Errorf("view after create: %+v %v", v, err)
	}
}

// The settings page reads [general] by its TOML key names, durations as
// text. Anything else shows the page empty, and saving it would then
// remove the keys.
func TestLoadGeneralForThePage(t *testing.T) {
	s, _, _ := newService(t, "[general]\nlog_level = \"debug\"\nlaunch_at_login = true\nmount_probe_interval = \"5s\"\ncache_dir = \"~/c\"\n")
	v, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	for _, want := range []string{`"log_level":"debug"`, `"launch_at_login":true`, `"mount_probe_interval":"5s"`, `"cache_dir":"~/c"`, `"upload_stuck_after":""`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("view lacks %s:\n%s", want, raw)
		}
	}
}
