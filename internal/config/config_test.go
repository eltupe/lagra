package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRegistry() *StaticRegistry {
	return &StaticRegistry{
		Backends: map[string][]OptionInfo{
			"s3": {
				{Name: "provider", Type: "string", Examples: []string{"AWS", "Other"}},
				{Name: "region", Type: "string"},
				{Name: "endpoint", Type: "string"},
				{Name: "env_auth", Type: "bool", Default: "false"},
				{Name: "access_key_id", Type: "string", Sensitive: true},
				{Name: "secret_access_key", Type: "string", Sensitive: true},
				{Name: "upload_concurrency", Type: "int", Default: "4"},
			},
			"sftp": {
				{Name: "host", Type: "string", Required: true, Sensitive: true},
				{Name: "user", Type: "string", Sensitive: true},
				{Name: "pass", Type: "string", Sensitive: true, Password: true},
				{Name: "port", Type: "int"},
			},
			"smb": {
				{Name: "host", Type: "string", Required: true, Sensitive: true},
				{Name: "user", Type: "string", Sensitive: true},
				{Name: "domain", Type: "string", Sensitive: true},
				{Name: "pass", Type: "string", Sensitive: true, Password: true},
			},
			"webdav": {
				{Name: "url", Type: "string", Required: true},
				{Name: "user", Type: "string", Sensitive: true},
				{Name: "pass", Type: "string", Sensitive: true, Password: true},
			},
		},
		BucketBased: map[string]bool{"s3": true},
		VFSOpts: []OptionInfo{
			{Name: "vfs_cache_mode", Type: "CacheMode", Exclusive: true, Examples: []string{"off", "minimal", "writes", "full"}},
			{Name: "dir_cache_time", Type: "Duration"},
			{Name: "vfs_cache_max_size", Type: "SizeSuffix"},
			{Name: "read_only", Type: "bool"},
		},
	}
}

func testEnv() Env {
	return Env{
		Home: "/Users/test",
		LookupEnv: func(k string) (string, bool) {
			switch k {
			case "BUCKET":
				return "env-bucket", true
			}
			return "", false
		},
	}
}

const goodConfig = `
[general]
log_level = "debug"
mount_probe_interval = "5s"

[defaults]
read_only = false
vfs = { dir_cache_time = "2m" }

[[account]]
name = "aws"
options = { provider = "AWS", region = "eu-central-1" }
[account.secrets]
access_key_id = { keychain = true }
secret_access_key = { command = ["op", "read", "op://x"] }

[[account]]
name = "other"
options = { provider = "Other", endpoint = "https://s3.example.org" }

[[mount]]
name = "archiv"
account = "aws"
type = "s3"
path = "${BUCKET}/archiv"
vfs = { vfs_cache_max_size = "5G" }
read_only = true

[[mount]]
name = "photos"
account = "aws"
type = "s3"
path = "photos"

[[mount]]
name = "scratch"
account = "other"
type = "s3"
path = "scratch"
mount_point = "~/mnt/scratch"
enabled = false
`

// s3Account is a minimal valid account the mount test cases point at; it
// takes lines 1–2, so the first [[mount]] header is line 3.
const s3Account = "[[account]]\nname = \"s3\"\n"

// s3Mount is a valid S3 mount on s3Account, lines 3–7.
const s3Mount = s3Account + "[[mount]]\nname = \"a\"\naccount = \"s3\"\ntype = \"s3\"\npath = \"b\"\n"

// sftpAccount is a login for any number of servers, lines 1–3: the user and
// the server may be kept in the file, the server comes from each mount.
const sftpAccount = "[[account]]\nname = \"srv\"\npublic = [\"host\", \"user\"]\n"

func load(t *testing.T, src string) (*Effective, Issues) {
	t.Helper()
	doc, err := Parse("test.toml", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return Validate(doc, testRegistry(), testEnv())
}

func TestValidateGood(t *testing.T) {
	eff, issues := load(t, goodConfig)
	if issues.HasErrors() {
		t.Fatalf("unexpected errors:\n%v", issues)
	}
	if eff.General.LogLevel != "debug" || eff.General.MountProbeInterval != 5*time.Second || eff.General.BackendProbeInterval != DefaultBackendProbeInterval {
		t.Errorf("general = %+v", eff.General)
	}
	if eff.General.CacheDir != "/Users/test/Library/Caches/lagra" {
		t.Errorf("cache dir = %s", eff.General.CacheDir)
	}
	if len(eff.Accounts) != 2 {
		t.Fatalf("accounts = %+v", eff.Accounts)
	}
	if acc, ok := eff.Account("aws"); !ok || acc.Options["region"] != "eu-central-1" || acc.Secrets["access_key_id"].Kind() != "keychain" {
		t.Errorf("account aws = %+v", acc)
	}
	a, ok := eff.Mount("archiv")
	if !ok {
		t.Fatal("archiv missing")
	}
	if a.Path != "${BUCKET}/archiv" {
		t.Errorf("remote path must not be env-expanded, got %s", a.Path)
	}
	if a.MountPoint != "/Users/test/Volumes/archiv" {
		t.Errorf("default mount point = %s", a.MountPoint)
	}
	if !a.ReadOnly || !a.Enabled {
		t.Errorf("read_only/enabled = %v/%v", a.ReadOnly, a.Enabled)
	}
	if a.VFS["vfs_cache_mode"] != "full" || a.VFS["dir_cache_time"] != "2m" || a.VFS["vfs_cache_max_size"] != "5G" {
		t.Errorf("vfs merge = %v", a.VFS)
	}
	// The account's backend settings are copied into every mount using it.
	if a.Account != "aws" || a.Type != "s3" || a.Options["region"] != "eu-central-1" || len(a.Options) != 2 {
		t.Errorf("account/type/options = %s/%s/%v", a.Account, a.Type, a.Options)
	}
	if a.Secrets["access_key_id"].Kind() != "keychain" || a.Secrets["secret_access_key"].Kind() != "command" {
		t.Errorf("secrets = %v", a.Secrets)
	}
	p, _ := eff.Mount("photos")
	if p.Options["region"] != "eu-central-1" || p.Secrets["access_key_id"].Kind() != "keychain" {
		t.Errorf("photos = %+v", p)
	}
	p.Options["region"] = "changed"
	if a.Options["region"] != "eu-central-1" {
		t.Error("mounts of one account must not share option maps")
	}
	s, _ := eff.Mount("scratch")
	if s.Enabled || s.MountPoint != "/Users/test/mnt/scratch" || s.Options["endpoint"] != "https://s3.example.org" {
		t.Errorf("scratch = %+v", s)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string // substring of the issue
		line int    // expected line, 0 = don't care
	}{
		{"unknown key", "[general]\nlog_levle = \"info\"\n", "unknown key", 2},
		{"bad log level", "[general]\nlog_level = \"loud\"\n", "must be one of", 2},
		{"zero interval", "[general]\nmount_probe_interval = \"0s\"\n", "greater than 0", 2},
		{"defaults vfs unknown", "[defaults]\nvfs = { nope = 1 }\n", "unknown VFS option", 2},

		// accounts
		{"account missing name", "[[account]]\noptions = { region = \"x\" }\n", "name is required", 1},
		{"account bad name", "[[account]]\nname = \"Hetzner Alt\"\n", "must match", 2},
		{"type in account", "[[account]]\nname = \"a\"\ntype = \"s3\"\n", "type belongs to the mount", 3},
		{"duplicate account", "[[account]]\nname = \"a\"\n[[account]]\nname = \"a\"\n", "duplicate account name", 4},
		{"unknown option", "[[account]]\nname = \"a\"\noptions = { regoin = \"x\" }\n", "unknown option \"regoin\": no backend has it", 3},
		{"plaintext secret", "[[account]]\nname = \"a\"\n[account.options]\nsecret_access_key = \"abc\"\n", "marked sensitive", 4},
		{"plaintext password", "[[account]]\nname = \"a\"\npublic = [\"pass\"]\n[account.options]\npass = \"abc\"\n", "is a password and must come", 5},
		{"public password", "[[account]]\nname = \"a\"\npublic = [\"pass\"]\n", "can never be public", 3},
		{"public unknown", "[[account]]\nname = \"a\"\npublic = [\"hostt\", \"host\"]\noptions = { host = \"h\" }\n", "unknown option \"hostt\"", 3},
		{"sensitive host without public", "[[account]]\nname = \"a\"\noptions = { host = \"h\" }\n", "list it in the account's public", 3},
		{"bad option type", "[[account]]\nname = \"s3\"\noptions = { upload_concurrency = \"many\" }\n[[mount]]\nname = \"a\"\naccount = \"s3\"\ntype = \"s3\"\npath = \"b\"\n", "from account \"s3\": option \"upload_concurrency\": \"many\" is not a valid int", 4},
		{"option array", "[[account]]\nname = \"a\"\noptions = { region = [\"a\"] }\n", "must be a string, number or boolean", 3},
		{"secret for unknown option", "[[account]]\nname = \"a\"\n[account.secrets]\nnope = { keychain = true }\n", "unknown option \"nope\"", 4},
		{"secret both sources", "[[account]]\nname = \"a\"\n[account.secrets]\naccess_key_id = { keychain = true, command = [\"x\"] }\n", "exactly one of", 4},
		{"secret no source", "[[account]]\nname = \"a\"\n[account.secrets]\naccess_key_id = { }\n", "exactly one of", 4},
		{"secret and option", "[[account]]\nname = \"a\"\noptions = { region = \"x\" }\n[account.secrets]\nregion = { keychain = true }\n", "both in options and in secrets", 5},

		// mounts
		{"missing name", s3Account + "[[mount]]\naccount = \"s3\"\ntype = \"s3\"\npath = \"b\"\n", "name is required", 3},
		{"bad name", s3Account + "[[mount]]\nname = \"Archiv Alt\"\naccount = \"s3\"\ntype = \"s3\"\npath = \"b\"\n", "must match", 4},
		{"missing account", s3Account + "[[mount]]\nname = \"a\"\ntype = \"s3\"\npath = \"b\"\n", "account is required", 3},
		{"unknown account", s3Account + "[[mount]]\nname = \"a\"\naccount = \"nope\"\ntype = \"s3\"\npath = \"b\"\n", "unknown account \"nope\"", 5},
		{"missing type", s3Account + "[[mount]]\nname = \"a\"\naccount = \"s3\"\npath = \"b\"\n", "type is required", 3},
		{"unknown type", s3Account + "[[mount]]\nname = \"a\"\naccount = \"s3\"\ntype = \"ftpx\"\npath = \"b\"\n", "unsupported backend type", 6},
		{"required option", sftpAccount + "[[mount]]\nname = \"a\"\naccount = \"srv\"\ntype = \"sftp\"\n", "requires option \"host\": set it in [mount.options]", 4},
		{"unknown mount option", s3Mount + "[mount.options]\nregoin = \"x\"\n", "unknown option \"regoin\": backend \"s3\" has no such option", 9},
		{"bad mount option", s3Mount + "options = { upload_concurrency = \"many\" }\n", "not a valid int", 8},
		{"password in mount", sftpAccount + "[[mount]]\nname = \"a\"\naccount = \"srv\"\ntype = \"sftp\"\noptions = { host = \"h\", pass = \"x\" }\n", "is a password and must come", 8},
		{"sensitive mount option without public", s3Mount + "options = { access_key_id = \"x\" }\n", "list it in the account's public", 8},
		{"mount option is an account secret", sftpAccount + "[account.secrets]\nhost = { keychain = true }\n[[mount]]\nname = \"a\"\naccount = \"srv\"\ntype = \"sftp\"\noptions = { host = \"h\" }\n", "comes from the secrets of account \"srv\"", 10},
		{"secrets in mount", s3Mount + "secrets = { access_key_id = { keychain = true } }\n", "secrets belong to the account", 8},
		{"public in mount", s3Mount + "public = [\"host\"]\n", "public belongs to the account", 8},
		{"duplicate name", s3Account + "[[mount]]\nname = \"a\"\naccount = \"s3\"\ntype = \"s3\"\npath = \"b\"\nmount_point = \"/x\"\n[[mount]]\nname = \"a\"\naccount = \"s3\"\ntype = \"s3\"\npath = \"b\"\nmount_point = \"/y\"\n", "duplicate mount name", 10},
		{"duplicate mount point", s3Account + "[[mount]]\nname = \"a\"\naccount = \"s3\"\ntype = \"s3\"\npath = \"b\"\nmount_point = \"/x\"\n[[mount]]\nname = \"b\"\naccount = \"s3\"\ntype = \"s3\"\npath = \"b\"\nmount_point = \"/x\"\n", "also used by mount", 14},
		{"relative mount point", s3Mount + "mount_point = \"mnt/a\"\n", "absolute", 8},
		{"undefined env", s3Mount + "mount_point = \"${NOPE}/a\"\n", "undefined environment variable NOPE", 8},
		{"unknown vfs", s3Mount + "vfs = { cache = \"full\" }\n", "unknown VFS option", 8},
		{"vfs read_only", s3Mount + "vfs = { read_only = true }\n", "use read_only on the mount", 8},
		{"bad vfs value", s3Mount + "vfs = { vfs_cache_mode = \"turbo\" }\n", "not one of", 8},
		{"bucket missing", s3Account + "[[mount]]\nname = \"a\"\naccount = \"s3\"\ntype = \"s3\"\n", "name the bucket", 3},
		{"bucket empty", s3Account + "[[mount]]\nname = \"a\"\naccount = \"s3\"\ntype = \"s3\"\npath = \"\"\n", "name the bucket", 7},
		{"bucket only slash", s3Account + "[[mount]]\nname = \"a\"\naccount = \"s3\"\ntype = \"s3\"\npath = \"/\"\n", "name the bucket", 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, issues := load(t, tc.src)
			if !issues.HasErrors() {
				t.Fatalf("expected an error containing %q, got none", tc.want)
			}
			found := false
			for _, is := range issues {
				if strings.Contains(is.Msg, tc.want) {
					found = true
					if tc.line != 0 && is.Line != tc.line {
						t.Errorf("issue %q at line %d, want %d", is.Msg, is.Line, tc.line)
					}
				}
			}
			if !found {
				t.Errorf("no issue contains %q; got:\n%v", tc.want, issues)
			}
		})
	}
}

func TestValidateWarnsAboutVolumes(t *testing.T) {
	eff, issues := load(t, s3Mount+"mount_point = \"/Volumes/a\"\n")
	if issues.HasErrors() {
		t.Fatalf("unexpected errors: %v", issues)
	}
	if len(issues.Warnings()) != 1 || !strings.Contains(issues[0].Msg, "root") {
		t.Errorf("want one /Volumes warning, got %v", issues)
	}
	if eff == nil {
		t.Fatal("effective config must be returned when there are only warnings")
	}
}

// A broken account is reported once; its mounts are not also told that the
// account does not exist.
func TestBrokenAccountIsNotUnknown(t *testing.T) {
	_, issues := load(t, "[[account]]\nname = \"s3\"\noptions = { nope = \"x\" }\n[[mount]]\nname = \"a\"\naccount = \"s3\"\ntype = \"s3\"\npath = \"b\"\n")
	if !issues.HasErrors() {
		t.Fatal("expected the unknown option")
	}
	for _, is := range issues {
		if strings.Contains(is.Msg, "unknown account") {
			t.Errorf("unexpected %q", is.Msg)
		}
	}
}

func TestAccountsAndMountsHaveSeparateNames(t *testing.T) {
	_, issues := load(t, "[[account]]\nname = \"a\"\n[[mount]]\nname = \"a\"\naccount = \"a\"\ntype = \"s3\"\npath = \"b\"\n")
	if issues.HasErrors() {
		t.Fatalf("unexpected: %v", issues)
	}
}

func TestUnusedAccountIsFine(t *testing.T) {
	eff, issues := load(t, s3Account)
	if issues.HasErrors() || len(eff.Accounts) != 1 || len(eff.Mounts) != 0 {
		t.Fatalf("eff = %+v, issues = %v", eff, issues)
	}
}

func TestParseSyntaxErrorHasLine(t *testing.T) {
	_, err := Parse("x.toml", []byte("[general]\nlog_level = \n"))
	if err == nil {
		t.Fatal("expected error")
	}
	is, ok := err.(*Issue)
	if !ok || is.Line != 2 {
		t.Fatalf("want *Issue at line 2, got %#v", err)
	}
}

func TestPublicAllowsSensitiveIdentifiers(t *testing.T) {
	eff, issues := load(t, "[[account]]\nname = \"srv\"\npublic = [\"host\", \"user\", \"port\"]\noptions = { host = \"h\", user = \"u\", port = 22 }\n[account.secrets]\npass = { keychain = true }\n[[mount]]\nname = \"a\"\naccount = \"srv\"\ntype = \"sftp\"\n")
	if issues.HasErrors() {
		t.Fatalf("unexpected: %v", issues)
	}
	if len(issues.Warnings()) != 1 || !strings.Contains(issues.Warnings()[0].Msg, "not sensitive") {
		t.Errorf("want one warning about port, got %v", issues)
	}
	if eff.Mounts[0].Options["host"] != "h" {
		t.Errorf("options = %v", eff.Mounts[0].Options)
	}
}

func TestRequiredOptionSatisfiedBySecret(t *testing.T) {
	_, issues := load(t, "[[account]]\nname = \"a\"\n[account.secrets]\nhost = { keychain = true }\n[[mount]]\nname = \"m\"\naccount = \"a\"\ntype = \"sftp\"\n")
	if issues.HasErrors() {
		t.Fatalf("unexpected: %v", issues)
	}
}

// One login over two protocols, as with a directory account that reaches
// SMB shares and WebDAV servers: each mount takes the options its backend
// knows, and the password is one secret.
func TestOneAccountSeveralBackends(t *testing.T) {
	eff, issues := load(t, `[[account]]
name = "svc"
public = ["host", "user", "domain"]
[account.options]
user = "svc"
domain = "CORP"
[account.secrets]
pass = { keychain = true }
[[mount]]
name = "data"
account = "svc"
type = "smb"
path = "data$"
options = { host = "files.example.org" }
[[mount]]
name = "web"
account = "svc"
type = "webdav"
options = { url = "https://dav.example.org/files" }
`)
	if issues.HasErrors() || len(issues.Warnings()) != 0 {
		t.Fatalf("unexpected: %v", issues)
	}
	data, _ := eff.Mount("data")
	web, _ := eff.Mount("web")
	if data.Type != "smb" || data.Options["domain"] != "CORP" || data.Options["user"] != "svc" || data.Options["host"] != "files.example.org" || data.Secrets["pass"].Kind() != "keychain" {
		t.Errorf("data = %s %v %v", data.Type, data.Options, data.Secrets)
	}
	if _, ok := web.Options["domain"]; ok || web.Type != "webdav" || web.Options["user"] != "svc" || web.Secrets["pass"].Kind() != "keychain" {
		t.Errorf("WebDAV has no domain option: %s %v %v", web.Type, web.Options, web.Secrets)
	}

	// An option no mount of the account can use is most likely a typo.
	_, issues = load(t, "[[account]]\nname = \"svc\"\noptions = { region = \"x\" }\n[[mount]]\nname = \"web\"\naccount = \"svc\"\ntype = \"webdav\"\noptions = { url = \"https://h\" }\n")
	if w := issues.Warnings(); len(w) != 1 || !strings.Contains(w[0].Msg, "\"region\" is not an option of the backend of any mount") || w[0].Line != 3 {
		t.Errorf("want one warning about region at line 3, got %v", issues)
	}
}

// One login, several servers: the account holds who you are, each mount
// says where. A mount's options override the account's.
func TestMountOptionsSayWhere(t *testing.T) {
	eff, issues := load(t, sftpAccount+`[account.options]
user = "alice"
port = 22
[account.secrets]
pass = { keychain = true }
[[mount]]
name = "web"
account = "srv"
type = "sftp"
path = "/srv/www"
options = { host = "web.example.org" }
[[mount]]
name = "db"
account = "srv"
type = "sftp"
path = "/srv/db"
[mount.options]
host = "db.example.org"
port = 2222
`)
	if issues.HasErrors() {
		t.Fatalf("unexpected: %v", issues)
	}
	web, _ := eff.Mount("web")
	db, _ := eff.Mount("db")
	if web.Options["host"] != "web.example.org" || web.Options["user"] != "alice" || web.Options["port"] != "22" {
		t.Errorf("web options = %v", web.Options)
	}
	if db.Options["host"] != "db.example.org" || db.Options["port"] != "2222" || db.Secrets["pass"].Kind() != "keychain" {
		t.Errorf("db = %v %v", db.Options, db.Secrets)
	}
	if acc, _ := eff.Account("srv"); acc.Options["host"] != "" || acc.Options["port"] != "22" {
		t.Errorf("a mount's options must not leak into the account: %v", acc.Options)
	}
}

func TestExpandPath(t *testing.T) {
	env := func(k string) (string, bool) {
		if k == "USER" {
			return "alice", true
		}
		return "", false
	}
	cases := map[string]string{
		"~":                     "/home/x",
		"~/Volumes/a":           "/home/x/Volumes/a",
		"/tmp/${USER}/a":        "/tmp/alice/a",
		"~/${USER}":             "/home/x/alice",
		"/a/../b":               "/b",
		"relative/~notexpanded": "relative/~notexpanded",
	}
	for in, want := range cases {
		got, err := ExpandPath(in, env, "/home/x")
		if err != nil || got != want {
			t.Errorf("ExpandPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ExpandPath("${NOPE}", env, "/home/x"); err == nil {
		t.Error("undefined variable must fail")
	}
}

func TestMatchProvider(t *testing.T) {
	cases := []struct {
		filter, provider string
		want             bool
	}{
		{"", "AWS", true},
		{"AWS", "AWS", true},
		{"AWS,Alibaba", "Alibaba", true},
		{"AWS", "Other", false},
		{"!AWS,Alibaba", "Other", true},
		{"!AWS,Alibaba", "AWS", false},
	}
	for _, c := range cases {
		if got := MatchProvider(c.filter, c.provider); got != c.want {
			t.Errorf("MatchProvider(%q,%q) = %v", c.filter, c.provider, got)
		}
	}
}

func TestLocate(t *testing.T) {
	t.Setenv(EnvConfig, "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if p, _ := Locate("/explicit.toml"); p != "/explicit.toml" {
		t.Errorf("flag wins, got %s", p)
	}
	t.Setenv(EnvConfig, "/env.toml")
	if p, _ := Locate(""); p != "/env.toml" {
		t.Errorf("env, got %s", p)
	}
	t.Setenv(EnvConfig, "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if p, _ := Locate(""); p != "/xdg/lagra/config.toml" {
		t.Errorf("xdg, got %s", p)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	p, err := Locate("")
	if err != nil || !strings.HasSuffix(p, "/.config/lagra/config.toml") {
		t.Errorf("home, got %s %v", p, err)
	}
}

func TestPathNeedsBucketOnlyForObjectStores(t *testing.T) {
	_, issues := load(t, "[[account]]\nname = \"srv\"\npublic = [\"host\"]\noptions = { host = \"h\" }\n[[mount]]\nname = \"a\"\naccount = \"srv\"\ntype = \"sftp\"\n")
	if issues.HasErrors() {
		t.Fatalf("sftp with an empty path is the remote home directory, got %v", issues)
	}
	_, issues = load(t, s3Account+"[[mount]]\nname = \"a\"\naccount = \"s3\"\ntype = \"s3\"\npath = \"my-bucket/photos\"\n")
	if issues.HasErrors() {
		t.Fatalf("unexpected: %v", issues)
	}
}

// [general] and [defaults] share key names; an issue must point at the table
// it is about.
func TestIssueLineFindsTheRightTable(t *testing.T) {
	src := "[defaults]\nmount_probe_interval = \"10s\"\n\n[general]\nmount_probe_interval = \"0s\"\n"
	_, issues := load(t, src)
	if len(issues) != 1 || issues[0].Path != "general.mount_probe_interval" || issues[0].Line != 5 {
		t.Fatalf("issues = %v", issues)
	}
}

// The mount table lists real paths, so a mount point below a symlinked
// directory must come out resolved, or lagra would not recognise its own
// volume. Directories that do not exist yet stay as written.
func TestMountPointParentsResolved(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	realBase, err := filepath.EvalSymlinks(base) // t.TempDir is itself below /var -> /private/var
	if err != nil {
		t.Fatal(err)
	}
	env := testEnv()
	env.EvalSymlinks = filepath.EvalSymlinks
	src := goodConfig + fmt.Sprintf("\n[[mount]]\nname = \"linked\"\naccount = \"aws\"\ntype = \"s3\"\npath = \"b\"\nmount_point = %q\n\n[[mount]]\nname = \"later\"\naccount = \"aws\"\ntype = \"s3\"\npath = \"b\"\nmount_point = %q\n",
		filepath.Join(base, "link", "vol"), filepath.Join(base, "link", "new", "vol"))
	doc, err := Parse("test.toml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	eff, issues := Validate(doc, testRegistry(), env)
	if issues.HasErrors() {
		t.Fatalf("unexpected errors:\n%v", issues)
	}
	for name, want := range map[string]string{
		"linked": filepath.Join(realBase, "real", "vol"),
		"later":  filepath.Join(realBase, "real", "new", "vol"),
	} {
		if m, _ := eff.Mount(name); m.MountPoint != want {
			t.Errorf("%s: mount point = %s, want %s", name, m.MountPoint, want)
		}
	}
}
