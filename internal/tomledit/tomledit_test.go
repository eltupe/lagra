package tomledit

import (
	"strings"
	"testing"

	"github.com/eltupe/lagra/internal/config"
)

const sample = `# my mounts
[general]
log_level = "info"   # keep it quiet

[defaults]
read_only = false

[[account]]
name = "aws"
# comment inside aws
[account.options]
provider = "AWS"

[[account]]  # second
name = "srv"
public = ["host"]
[account.options]
host = "h"
[account.secrets]
pass = { keychain = true }

# ---- volumes ----

[[mount]]
name = "a"
account = "aws"
path = "bucket-a"
# comment inside a
[mount.vfs]
dir_cache_time = "2m"

[[mount]]  # second
name = "b"
account = "srv"
path = "/srv"
`

func TestNames(t *testing.T) {
	if got := strings.Join(Names(sample, Account), ","); got != "aws,srv" {
		t.Errorf("accounts = %s", got)
	}
	if got := strings.Join(Names(sample, Mount), ","); got != "a,b" {
		t.Errorf("mounts = %s", got)
	}
}

func TestReplaceMountKeepsRest(t *testing.T) {
	ro := true
	out := Replace(sample, Mount, "a", RenderMount(config.Mount{Name: "a", Account: "aws", Path: "bucket-new", ReadOnly: &ro}))
	for _, want := range []string{"# my mounts", `log_level = "info"   # keep it quiet`, "[[mount]]  # second", `pass = { keychain = true }`, `path = "bucket-new"`, "read_only = true", "# ---- volumes ----"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "# comment inside a\n") || strings.Contains(out, `path = "bucket-a"`) || strings.Contains(out, `dir_cache_time`) {
		t.Errorf("old block content survived:\n%s", out)
	}
	if strings.Count(out, "[[mount]]") != 2 || strings.Count(out, "[[account]]") != 2 {
		t.Errorf("block count changed:\n%s", out)
	}
	if !strings.Contains(out, "read_only = true\n\n[[mount]]  # second") {
		t.Errorf("spacing between blocks lost:\n%s", out)
	}
}

// An account block ends at the next foreign header, not only at the next
// [[account]]: the mount comment and the mounts stay untouched.
func TestReplaceAccountKeepsMounts(t *testing.T) {
	out := Replace(sample, Account, "srv", RenderAccount(config.Account{Name: "srv", Public: []string{"host"},
		Options: map[string]any{"host": "new"}, Secrets: map[string]config.SecretSource{"pass": {Keychain: true}}}))
	for _, want := range []string{`host = "new"`, "# ---- volumes ----", `name = "a"`, `name = "b"`, "# comment inside aws"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, `host = "h"`) {
		t.Errorf("old account content survived:\n%s", out)
	}
	if !strings.Contains(out, "pass = { keychain = true }\n\n# ---- volumes ----\n\n[[mount]]\nname = \"a\"") {
		t.Errorf("layout around the mounts changed:\n%s", out)
	}
}

func TestInsertMountAppends(t *testing.T) {
	out := Replace(sample, Mount, "c", RenderMount(config.Mount{Name: "c", Account: "aws", Path: "p"}))
	if strings.Count(out, "[[mount]]") != 3 || !strings.HasSuffix(out, "path = \"p\"\n") {
		t.Errorf("append failed:\n%s", out)
	}
	if got := strings.Join(Names(out, Mount), ","); got != "a,b,c" {
		t.Errorf("names = %s", got)
	}
}

// New accounts go after the last account, before the comment that
// introduces the mounts.
func TestInsertAccountBeforeMounts(t *testing.T) {
	out := Insert(sample, Account, RenderAccount(config.Account{Name: "new"}))
	if !strings.Contains(out, "pass = { keychain = true }\n\n[[account]]\nname = \"new\"\n\n# ---- volumes ----") {
		t.Errorf("insert position:\n%s", out)
	}
	if got := strings.Join(Names(out, Account), ","); got != "aws,srv,new" {
		t.Errorf("names = %s", got)
	}
	doc, err := config.Parse("t", []byte(out))
	if err != nil || len(doc.File.Accounts) != 3 || len(doc.File.Mounts) != 2 {
		t.Fatalf("result does not parse as 3 accounts + 2 mounts: %v\n%s", err, out)
	}
	// no mounts yet: at the end
	out = Insert("[general]\nlog_level = \"info\"\n", Account, RenderAccount(config.Account{Name: "x"}))
	if out != "[general]\nlog_level = \"info\"\n\n[[account]]\nname = \"x\"\n" {
		t.Errorf("insert without mounts:\n%q", out)
	}
	// mounts but no accounts: directly above them
	out = Insert("[[mount]]\nname = \"m\"\naccount = \"x\"\npath = \"p\"\n", Account, RenderAccount(config.Account{Name: "x"}))
	if !strings.HasPrefix(out, "[[account]]\nname = \"x\"\n\n[[mount]]") {
		t.Errorf("insert above first mount:\n%q", out)
	}
}

func TestRemove(t *testing.T) {
	out := Remove(sample, Mount, "a")
	if strings.Contains(out, `name = "a"`) || !strings.Contains(out, `name = "b"`) || !strings.Contains(out, "# my mounts") {
		t.Errorf("remove a:\n%s", out)
	}
	if strings.Contains(out, "\n\n\n") {
		t.Errorf("double blank line:\n%s", out)
	}
	out = Remove(sample, Mount, "b")
	if strings.Contains(out, `name = "b"`) || !strings.HasSuffix(out, "dir_cache_time = \"2m\"\n") {
		t.Errorf("remove last:\n%q", out)
	}
	out = Remove(sample, Account, "aws")
	if strings.Contains(out, `name = "aws"`) || !strings.Contains(out, `name = "srv"`) || strings.Count(out, "[[mount]]") != 2 {
		t.Errorf("remove account:\n%s", out)
	}
	if Remove(sample, Mount, "zzz") != sample || Remove(sample, Account, "a") != sample {
		t.Error("unknown name must not change the file")
	}
}

func TestSetBlockKey(t *testing.T) {
	out := SetBlockKey(sample, Mount, "a", "account", Quote("new"))
	if !strings.Contains(out, "name = \"a\"\naccount = \"new\"\npath = \"bucket-a\"") || !strings.Contains(out, "name = \"b\"\naccount = \"srv\"") {
		t.Errorf("set existing key:\n%s", out)
	}
	out = SetBlockKey("[[mount]]\nname = \"m\"  # mine\npath = \"p\"\n", Mount, "m", "account", Quote("x"))
	if out != "[[mount]]\nname = \"m\"  # mine\naccount = \"x\"\npath = \"p\"\n" {
		t.Errorf("add missing key:\n%q", out)
	}
	if SetBlockKey(sample, Mount, "zzz", "account", Quote("x")) != sample {
		t.Error("unknown name must not change the file")
	}
}

func TestSetKey(t *testing.T) {
	out := SetKey(sample, "general", "log_level", `"debug"`)
	if !strings.Contains(out, `log_level = "debug"   # keep it quiet`) {
		t.Errorf("existing key with comment:\n%s", out)
	}
	out = SetKey(out, "general", "launch_at_login", "true")
	if !strings.Contains(out, "log_level = \"debug\"   # keep it quiet\nlaunch_at_login = true\n\n[defaults]") {
		t.Errorf("new key appended to table:\n%s", out)
	}
	out = SetKey(out, "defaults.vfs", "vfs_cache_mode", `"full"`)
	if !strings.Contains(out, "read_only = false\n\n[defaults.vfs]\nvfs_cache_mode = \"full\"\n\n[[account]]") {
		t.Errorf("new sub-table after parent:\n%s", out)
	}
	out = SetKey(out, "general", "launch_at_login", "")
	if strings.Contains(out, "launch_at_login") {
		t.Errorf("remove key:\n%s", out)
	}
	// table missing entirely
	out = SetKey("[[account]]\nname = \"x\"\ntype = \"s3\"\n", "general", "log_level", `"warn"`)
	if !strings.HasPrefix(out, "[general]\nlog_level = \"warn\"\n\n[[account]]") {
		t.Errorf("create table before accounts:\n%s", out)
	}
	if !strings.Contains(out, `name = "x"`) {
		t.Errorf("account lost:\n%s", out)
	}
}

func TestRenderRoundTrip(t *testing.T) {
	to := config.Duration(3e9)
	a := config.Account{Name: "r", Public: []string{"host", "user"},
		Options: map[string]any{"user": "u", "port": int64(22), "key_use_agent": true},
		Secrets: map[string]config.SecretSource{"pass": {Command: []string{"op", "read", "x y"}, Timeout: &to}}}
	en := false
	m := config.Mount{Name: "m", Account: "r", Type: "sftp", Path: "/x", Enabled: &en, Options: map[string]any{"host": "h", "port": "2222"}, VFS: map[string]any{"dir_cache_time": "2m"}}
	src := RenderAccount(a) + "\n" + RenderMount(m)
	doc, err := config.Parse("t", []byte(src))
	if err != nil {
		t.Fatalf("rendered blocks do not parse: %v\n%s", err, src)
	}
	ga := doc.File.Accounts[0]
	if ga.Name != "r" || len(ga.Public) != 2 || ga.Options["port"] != int64(22) || ga.Options["key_use_agent"] != true {
		t.Errorf("account round trip = %+v", ga)
	}
	if s := ga.Secrets["pass"]; s.Kind() != "command" || s.Command[2] != "x y" || s.Timeout.Std() != 3e9 {
		t.Errorf("secret = %+v", s)
	}
	gm := doc.File.Mounts[0]
	if gm.Name != "m" || gm.Account != "r" || gm.Type != "sftp" || *gm.Enabled || gm.Options["host"] != "h" || gm.Options["port"] != int64(2222) || gm.VFS["dir_cache_time"] != "2m" {
		t.Errorf("mount round trip = %+v", gm)
	}
}

func TestRemoveKeepsBlankLinesElsewhere(t *testing.T) {
	src := "[general]\nlog_level = \"info\"\n\n\n# two blank lines above, on purpose\n[[mount]]\nname = \"a\"\npath = \"p\"\n\n[[mount]]\nname = \"b\"\npath = \"q\"\n"
	out := Remove(src, Mount, "a")
	want := "[general]\nlog_level = \"info\"\n\n\n# two blank lines above, on purpose\n\n[[mount]]\nname = \"b\"\npath = \"q\"\n"
	if out != want {
		t.Errorf("remove a:\n%q\nwant\n%q", out, want)
	}
}

func TestLiteralStringName(t *testing.T) {
	src := "[[mount]]\nname = 'x'\npath = \"p\"\n"
	if got := Names(src, Mount); len(got) != 1 || got[0] != "x" {
		t.Errorf("names = %q", got)
	}
	if out := Remove(src, Mount, "x"); out != "" {
		t.Errorf("remove = %q", out)
	}
}

// A # inside a quoted value is not a comment.
func TestSetKeyHashInsideString(t *testing.T) {
	out := SetKey("[general]\ncache_dir = \"~/a#b\"  # where\n", "general", "cache_dir", Quote("~/c"))
	if out != "[general]\ncache_dir = \"~/c\"  # where\n" {
		t.Errorf("got %q", out)
	}
	out = SetBlockKey("[[mount]]\nname = \"m\"\npath = 'a#b'\n", Mount, "m", "path", Quote("c"))
	if out != "[[mount]]\nname = \"m\"\npath = \"c\"\n" {
		t.Errorf("got %q", out)
	}
}

// rclone's permission options look like integers but are not valid TOML
// integers ("0777"); they must stay strings.
func TestRenderLeadingZeroStaysString(t *testing.T) {
	m := config.Mount{Name: "m", Account: "a", Path: "p", VFS: map[string]any{"dir_perms": "0777", "vfs_read_chunk_streams": "4"}}
	src := RenderMount(m)
	if !strings.Contains(src, `dir_perms = "0777"`) || !strings.Contains(src, "vfs_read_chunk_streams = 4") {
		t.Errorf("rendered:\n%s", src)
	}
	if _, err := config.Parse("t", []byte(src)); err != nil {
		t.Errorf("does not parse: %v\n%s", err, src)
	}
}
