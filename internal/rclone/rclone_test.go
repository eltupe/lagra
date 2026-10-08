package rclone

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudsoda/go-smb2"
	webdavapi "github.com/rclone/rclone/backend/webdav/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/obscure"

	"github.com/eltupe/lagra/internal/config"
)

func TestRegistryS3Metadata(t *testing.T) {
	reg := Registry{}
	s3, ok := reg.Backend("s3")
	if !ok {
		t.Fatal("s3 backend missing")
	}
	for _, name := range []string{"access_key_id", "secret_access_key", "session_token", "sse_customer_key"} {
		info, ok := s3.Lookup(name)
		if !ok || !info.Sensitive {
			t.Errorf("%s must be sensitive, got %+v", name, info)
		}
	}
	if info, _ := s3.Lookup("region"); info.Sensitive {
		t.Error("region must not be sensitive")
	}
	if _, ok := s3.Lookup("bucket"); ok {
		t.Error("s3 has no bucket option; bucket is part of the path")
	}
	if err := s3.Check("upload_concurrency", "abc"); err == nil {
		t.Error("int option must reject text")
	}
	if err := s3.Check("upload_concurrency", "8"); err != nil {
		t.Error(err)
	}
	if err := s3.Check("chunk_size", "10M"); err != nil {
		t.Error(err)
	}
	if _, ok := reg.Backend("nope"); ok {
		t.Error("unknown backend must not resolve")
	}
	sftp, _ := reg.Backend("sftp")
	if info, _ := sftp.Lookup("pass"); !info.Sensitive {
		t.Error("sftp pass must be sensitive (IsPassword)")
	}
}

func TestRegistryVFS(t *testing.T) {
	v := Registry{}.VFS()
	if _, ok := v.Lookup("vfs_cache_mode"); !ok {
		t.Fatal("vfs_cache_mode missing")
	}
	if err := v.Check("vfs_cache_mode", "full"); err != nil {
		t.Error(err)
	}
	if err := v.Check("vfs_cache_mode", "turbo"); err == nil {
		t.Error("invalid cache mode accepted")
	}
	if err := v.Check("dir_cache_time", "1m"); err != nil {
		t.Error(err)
	}
	if err := v.Check("vfs_cache_max_size", "10G"); err != nil {
		t.Error(err)
	}
	if err := v.Check("dir_cache_time", "soon"); err == nil {
		t.Error("invalid duration accepted")
	}
}

// The registry must satisfy the config package's interface and validate a
// realistic S3 mount end to end without a network.
func TestValidateWithRealRegistry(t *testing.T) {
	src := `
[[account]]
name = "aws"
options = { provider = "AWS", region = "eu-central-1" }
[account.secrets]
access_key_id = { keychain = true }
secret_access_key = { keychain = true }

[[mount]]
name = "archiv"
account = "aws"
type = "s3"
path = "bucket/prefix"
`
	doc, err := config.Parse("t.toml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	eff, issues := config.Validate(doc, Registry{}, config.Env{Home: "/Users/t", LookupEnv: os.LookupEnv})
	if issues.HasErrors() {
		t.Fatalf("issues: %v", issues)
	}
	if eff.Mounts[0].VFS["vfs_cache_mode"] != "full" {
		t.Errorf("vfs = %v", eff.Mounts[0].VFS)
	}
}

// An account belongs to no backend, so validation looks its options up
// across all of them, with the strictest flags.
func TestRegistryOptionAcrossBackends(t *testing.T) {
	reg := Registry{}
	if o, ok := reg.Option("pass"); !ok || !o.Password {
		t.Errorf("pass = %+v %v, want a password", o, ok)
	}
	if o, ok := reg.Option("domain"); !ok || !o.Sensitive || o.Password {
		t.Errorf("domain (smb) = %+v %v, want sensitive", o, ok)
	}
	if o, ok := reg.Option("region"); !ok || o.Sensitive {
		t.Errorf("region = %+v %v", o, ok)
	}
	if _, ok := reg.Option("regoin"); ok {
		t.Error("no backend has regoin")
	}
}

// Serves a local directory over NFS on a random localhost port. Nothing is
// mounted; this only proves the library path works end to end.
func TestStartLocalServer(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetCacheDir(filepath.Join(t.TempDir(), "cache")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv, err := InProcess{}.Start(ctx, Spec{
		Name:           "test",
		Type:           "local",
		Path:           root,
		VFS:            map[string]string{"vfs_cache_mode": "full", "dir_cache_time": "1s"},
		HandleCacheDir: filepath.Join(t.TempDir(), "handles"),
		HideAppleFiles: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if srv.Port() <= 0 {
		t.Errorf("port = %d", srv.Port())
	}
	if err := srv.Probe(ctx); err != nil {
		t.Errorf("probe: %v", err)
	}
	if err := srv.ProbeServer(ctx); err != nil {
		t.Errorf("ping: %v", err)
	}
	if n := srv.Uploads(); n != 0 {
		t.Errorf("uploads = %d", n)
	}
	if n, err := srv.Sync(ctx); err != nil || n != 0 {
		t.Errorf("sync = %d %v", n, err)
	}
	if err := srv.Stop(time.Second); err != nil {
		t.Errorf("stop: %v", err)
	}
	if err := srv.Err(); err != nil {
		t.Errorf("serve error: %v", err)
	}
	if err := srv.ProbeServer(ctx); err == nil {
		t.Error("ping must fail once the server stopped")
	}
}

// A file saved through the volume counts as a pending upload until the
// write-back delay has passed or Sync sends it.
func TestUploadsCountsWriteBack(t *testing.T) {
	if err := SetCacheDir(filepath.Join(t.TempDir(), "cache")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv, err := Start(ctx, Spec{
		Name: "uploads", Type: "local", Path: t.TempDir(),
		VFS: map[string]string{"vfs_cache_mode": "full", "vfs_write_back": "1h"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop(time.Second)
	f, err := srv.vfs.OpenFile("new.txt", os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("pending")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if n := srv.Uploads(); n != 1 {
		t.Fatalf("uploads after save = %d, want 1", n)
	}
	if n, err := srv.Sync(ctx); err != nil || n != 1 {
		t.Fatalf("sync = %d %v, want 1", n, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for srv.Uploads() != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := srv.Uploads(); n != 0 {
		t.Errorf("uploads after sync = %d, want 0", n)
	}
}

// A path that does not exist must not come up as an empty folder.
func TestStartMissingPathFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Start(ctx, Spec{Name: "gone", Type: "local", Path: filepath.Join(t.TempDir(), "nope"),
		VFS: map[string]string{"vfs_cache_mode": "off"}})
	var nf *PathNotFoundError
	if !errors.As(err, &nf) || nf.Path == "" {
		t.Fatalf("want PathNotFoundError, got %v", err)
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("message = %q", err)
	}
}

// Creating an S3 backend for a bucket root needs no network at all, so a dead
// endpoint is only discovered by Probe. The engine relies on that split.
func TestStartS3RootNeedsNoNetworkButProbeFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv, err := Start(ctx, Spec{Name: "dead", Type: "s3", Path: "bucket",
		VFS:     map[string]string{"vfs_cache_mode": "full"},
		Options: map[string]string{"provider": "Other", "endpoint": "http://127.0.0.1:9", "access_key_id": "x", "secret_access_key": "y"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Stop(0)
	pctx, pcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer pcancel()
	if err := srv.Probe(pctx); err == nil {
		t.Error("probe against a dead endpoint must fail")
	}
}

func TestRecommendedOptions(t *testing.T) {
	if recommended["s3"]["directory_markers"] != "true" {
		t.Error("s3 should default to directory markers")
	}
	if len(recommended["sftp"]) != 0 {
		t.Error("sftp has no recommendations")
	}
	// the user's explicit value wins (checked through Start's merge order)
	if _, ok := (Registry{}).Backend("s3"); !ok {
		t.Fatal("s3 missing")
	}
}

// People paste the address macOS or Windows shows for a share; rclone would
// only fail at connect time with "too many colons in address".
func TestServerHostMustBeAName(t *testing.T) {
	for _, tc := range []struct {
		typ, host string
		want      string // substring of the error; empty = valid
	}{
		{"smb", "files.storage.example.org", ""},
		{"smb", "smb://files.storage.example.org", `enter "files.storage.example.org"`},
		{"smb", "smb://files.storage.example.org/Projects$", `and "Projects$" as the mount's path`},
		{"smb", "smb://CORP;alice@server/Share%20Name/dir/", `enter "server", and "Share Name/dir" as the mount's path`},
		{"smb", `\\server\share\dir`, `enter "server", and "share/dir" as the mount's path`},
		{"smb", "server:1445", `enter "server", with 1445 as option "port"`},
		{"sftp", "alice@server", `enter "server"`},
		{"sftp", "sftp://server:2222/srv/data", `enter "server", with 2222 as option "port", and "/srv/data" as the mount's path`},
		{"sftp", "fe80::1", ""},
		{"ftp", "ftp://ftp.example.org", `enter "ftp.example.org"`},
		{"netstorage", "example-nsu.akamaihd.net/123456", ""}, // its host carries a path
	} {
		b, ok := (Registry{}).Backend(tc.typ)
		if !ok {
			t.Fatalf("backend %q missing", tc.typ)
		}
		err := b.Check("host", tc.host)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s host %q: %v", tc.typ, tc.host, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s host %q: got %v, want %q", tc.typ, tc.host, err, tc.want)
		}
	}
	// A host from the keychain never passes validation, so Start checks too,
	// before it opens anything.
	_, err := Start(context.Background(), Spec{Name: "x", Type: "smb", Options: map[string]string{"host": "smb://server/share"}})
	if err == nil || !strings.Contains(err.Error(), "not a server name") {
		t.Errorf("Start with an address as host: %v", err)
	}
}

// go-smb2 reports a refused session with its NTSTATUS code; rclone wraps it.
func TestLoginRejected(t *testing.T) {
	refused := fmt.Errorf("couldn't connect SMB: %w", &smb2.ResponseError{Code: 0xC000006D}) // STATUS_LOGON_FAILURE
	var lr *LoginRejectedError
	if err := loginRejected(refused); !errors.As(err, &lr) || !strings.Contains(err.Error(), "rejected the login") {
		t.Errorf("logon failure: %v", err)
	}
	locked := fmt.Errorf("couldn't connect SMB: %w", &smb2.ResponseError{Code: 0xC0000234}) // STATUS_ACCOUNT_LOCKED_OUT
	if err := loginRejected(locked); !errors.As(err, &lr) {
		t.Errorf("locked account: %v", err)
	}
	davRefused := fmt.Errorf("read metadata failed: %w", &webdavapi.Error{Status: "401 Unauthorized", StatusCode: 401})
	if err := loginRejected(davRefused); !errors.As(err, &lr) {
		t.Errorf("webdav 401: %v", err)
	}
	for _, err := range []error{
		nil,
		errors.New("dial tcp 127.0.0.1:445: connection refused"),
		fmt.Errorf("couldn't initialize SMB: %w", &smb2.ResponseError{Code: 0xC00000CC}), // STATUS_BAD_NETWORK_NAME: no such share
		&webdavapi.Error{Status: "403 Forbidden", StatusCode: 403},                       // logged in, but not allowed
	} {
		if got := loginRejected(err); got != err {
			t.Errorf("loginRejected(%v) = %v, want it unchanged", err, got)
		}
	}
}

// A share the server does not have is permanent, like a missing bucket.
func TestSMBShareMissing(t *testing.T) {
	if !smbShareMissing(fmt.Errorf("couldn't initialize SMB: %w", &smb2.ResponseError{Code: 0xC00000CC})) {
		t.Error("STATUS_BAD_NETWORK_NAME is a missing share")
	}
	if smbShareMissing(fmt.Errorf("couldn't connect SMB: %w", &smb2.ResponseError{Code: 0xC000006D})) || smbShareMissing(nil) {
		t.Error("only STATUS_BAD_NETWORK_NAME is")
	}
}

// rclone's SMB backend would try to connect low_level_retries (10) times,
// sleeping up to 2 s in between, and repeat a rejected login each time.
func TestSMBConnectsOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv, err := Start(ctx, Spec{Name: "x", Type: "smb", Path: "share/dir", VFS: map[string]string{"vfs_cache_mode": "off"},
		Options: map[string]string{"host": "127.0.0.1", "port": "9", "user": "u", "pass": "p"}})
	if err != nil {
		t.Fatalf("a closed port leaves the volume offline, not failed: %v", err)
	}
	defer srv.Stop(0)
	start := time.Now()
	err = srv.Probe(ctx)
	if err == nil || !strings.Contains(err.Error(), "couldn't connect SMB") {
		t.Fatalf("want a connect error, got %v", err)
	}
	var lr *LoginRejectedError
	if errors.As(err, &lr) {
		t.Errorf("a refused connection is not a rejected login: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Probe took %s: the backend retried the connection", d)
	}
}

// A server that does not answer at all (VPN down) must not hold up the
// start: SMB's NewFs would wait for the connection to check whether a path
// with a folder is a file. The volume comes up and the probe reports it
// offline.
func TestSMBStartsWithoutTheServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	// 192.0.2.0/24 is reserved for documentation: nothing answers there.
	srv, err := Start(ctx, Spec{Name: "x", Type: "smb", Path: "share/dir", VFS: map[string]string{"vfs_cache_mode": "off"},
		Options: map[string]string{"host": "192.0.2.1", "user": "u", "pass": "p"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Stop(0)
	if d := time.Since(start); d > rootCheckTimeout+2*time.Second {
		t.Errorf("Start took %s waiting for the server", d)
	}
}

func TestStartUnknownBackend(t *testing.T) {
	_, err := Start(context.Background(), Spec{Name: "x", Type: "nope"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestStartRespectsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	// A path below the bucket makes NewFs HEAD the object, which is where
	// the AWS SDK retries would otherwise block for minutes.
	_, err := Start(ctx, Spec{Name: "dead", Type: "s3", Path: "bucket/prefix",
		Options: map[string]string{"provider": "Other", "endpoint": "http://127.0.0.1:9", "access_key_id": "x", "secret_access_key": "y"}})
	if err == nil {
		t.Fatal("expected error against a dead endpoint")
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("Start ignored the deadline: took %s", time.Since(start))
	}
}

// The shipped example must validate against real rclone metadata.
func TestExampleValidates(t *testing.T) {
	doc, err := config.Parse("example.toml", config.Example)
	if err != nil {
		t.Fatal(err)
	}
	eff, issues := config.Validate(doc, Registry{}, config.Env{Home: "/Users/t", LookupEnv: os.LookupEnv})
	if issues.HasErrors() {
		t.Fatalf("example config invalid:\n%v", issues)
	}
	if len(eff.Mounts) != 5 {
		t.Errorf("mounts = %d", len(eff.Mounts))
	}
	a, _ := eff.Mount("archiv")
	if a.VFS["vfs_cache_max_size"] != "2G" || a.VFS["vfs_cache_mode"] != "full" || !a.ReadOnly {
		t.Errorf("archiv = %+v", a)
	}
	if team, _ := eff.Mount("team"); team.Options["host"] != "files.example.org" || team.Options["domain"] != "CORP" {
		t.Errorf("team options = %v", team.Options)
	}
	if web, _ := eff.Mount("intranet"); web.Type != "webdav" || web.Options["user"] != "alice" || web.Secrets["pass"].Kind() != "keychain" {
		t.Errorf("intranet = %s %v %v", web.Type, web.Options, web.Secrets)
	}
	if issues.HasErrors() || len(issues.Warnings()) != 0 {
		t.Errorf("example issues: %v", issues)
	}
}

func TestNeedsBucket(t *testing.T) {
	for typ, want := range map[string]bool{"s3": true, "b2": true, "azureblob": true, "sftp": false, "local": false, "nope": false} {
		if got := (Registry{}).NeedsBucket(typ); got != want {
			t.Errorf("NeedsBucket(%q) = %v, want %v", typ, got, want)
		}
	}
	for name := range bucketBased {
		if ri, err := fs.Find(name); err != nil || ri.Name != name {
			t.Errorf("bucketBased lists %q, which is not an rclone backend name", name)
		}
		if ri, err := fs.Find(name); err == nil && !offered(ri) {
			t.Errorf("bucketBased lists %q, which lagra does not offer", name)
		}
	}
	// rclone agrees, where asking it needs no network: an S3 backend at its
	// root lists buckets.
	ri, _ := fs.Find("s3")
	f, err := ri.NewFs(context.Background(), "t", "", fs.ConfigMap(ri.Prefix, ri.Options, "", configmap.Simple{"provider": "Other", "endpoint": "http://127.0.0.1:9"}))
	if err != nil {
		t.Fatal(err)
	}
	if !f.Features().BucketBased {
		t.Error("rclone no longer reports s3 as bucket based")
	}
}

// The backends imported in backends.go are exactly the ones offered, so
// that one rclone registers on its own (as it does crypt) cannot slip into
// the settings window after an update.
func TestOfferedBackends(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "backends.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	var imported, listed []string
	for _, is := range f.Imports {
		p, _ := strconv.Unquote(is.Path.Value)
		if name, ok := strings.CutPrefix(p, "github.com/rclone/rclone/backend/"); ok && name != "local" {
			imported = append(imported, name)
		}
	}
	for _, b := range Backends() {
		listed = append(listed, b.Name)
	}
	slices.Sort(imported)
	if !slices.Equal(imported, listed) {
		t.Errorf("Backends() = %v\nimported   = %v", listed, imported)
	}
	for _, typ := range []string{"s3", "sftp", "webdav", "smb", "ftp"} {
		if _, ok := (Registry{}).Backend(typ); !ok {
			t.Errorf("Backend(%q) is not offered", typ)
		}
	}
	for _, typ := range []string{"drive", "onedrive", "crypt", "local", "memory", "gcs", "tardigrade"} {
		if _, ok := (Registry{}).Backend(typ); ok {
			t.Errorf("Backend(%q) is offered but lagra cannot open it", typ)
		}
	}
}

// rclone reveals password options when it opens a backend, so lagra must
// hand them over obscured.
func TestPasswordsAreObscured(t *testing.T) {
	ri, _ := fs.Find("webdav")
	values := configmap.Simple{"url": "http://127.0.0.1:9/", "user": "u", "pass": "hunter2"}
	if err := obscurePasswords(ri.Options, values); err != nil {
		t.Fatal(err)
	}
	if values["user"] != "u" {
		t.Errorf("user = %q, must stay as it is", values["user"])
	}
	if got, err := obscure.Reveal(values["pass"]); err != nil || got != "hunter2" {
		t.Errorf("pass reveals as %q, %v", got, err)
	}
	if _, err := ri.NewFs(context.Background(), "t", "", fs.ConfigMap(ri.Prefix, ri.Options, "", values)); err != nil && strings.Contains(err.Error(), "password") {
		t.Errorf("webdav rejects the password: %v", err)
	}
}
