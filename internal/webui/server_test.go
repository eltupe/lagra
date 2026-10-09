package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eltupe/lagra/internal/config"
	"github.com/eltupe/lagra/internal/engine"
	"github.com/eltupe/lagra/internal/ipc"
	"github.com/eltupe/lagra/internal/settings"
)

type fakeCtl struct{ connected []string }

func (f *fakeCtl) Status() ipc.Status {
	return ipc.Status{Version: "t", ConfigOK: true, Overall: engine.OverallOK, Mounts: []engine.Snapshot{{Name: "archiv", State: engine.Connected}}}
}
func (f *fakeCtl) Connect(name string) error      { f.connected = append(f.connected, name); return nil }
func (f *fakeCtl) Disconnect(string, bool) error  { return nil }
func (f *fakeCtl) RestartAccount(string) []string { return nil }
func (f *fakeCtl) Sync(context.Context, string) (ipc.SyncResult, error) {
	return ipc.SyncResult{Expedited: 1}, nil
}
func (f *fakeCtl) Reload() ipc.ReloadResult { return ipc.ReloadResult{OK: true} }
func (f *fakeCtl) Shutdown(bool) error      { return nil }

func reg() *config.StaticRegistry {
	return &config.StaticRegistry{
		Backends: map[string][]config.OptionInfo{"s3": {{Name: "provider", Type: "string"}, {Name: "region", Type: "string"}, {Name: "secret_access_key", Type: "string", Sensitive: true}}},
		VFSOpts:  []config.OptionInfo{{Name: "vfs_cache_mode", Type: "string"}},
	}
}

func startServer(t *testing.T, opts ...func(*Deps)) (*Server, string, *fakeCtl) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "config.toml")
	_ = os.WriteFile(cfg, []byte("[general]\nlog_level = \"info\"\n\n[[account]]\nname = \"aws\"\n\n[[mount]]\nname = \"archiv\"\naccount = \"aws\"\ntype = \"s3\"\npath = \"b\"\n"), 0o644)
	ctl := &fakeCtl{}
	svc := &settings.Service{Path: cfg, Registry: reg(), Env: config.Env{Home: "/Users/t", LookupEnv: func(string) (string, bool) { return "", false }}}
	deps := Deps{Settings: svc, Ctl: ctl, Registry: reg(), Backends: func() []Backend { return []Backend{{Name: "s3", Description: "S3"}} }, Version: "t"}
	for _, o := range opts {
		o(&deps)
	}
	srv, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx) }()
	return srv, cfg, ctl
}

func req(t *testing.T, srv *Server, method, path, token string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r, _ := http.NewRequest(method, "http://"+srv.addr+path, rd)
	if token != "" {
		r.Header.Set("X-Lagra-Token", token)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	c := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func TestAuth(t *testing.T) {
	srv, _, _ := startServer(t)
	if resp, _ := req(t, srv, "GET", "/api/view", "", nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("no token: %d", resp.StatusCode)
	}
	if resp, _ := req(t, srv, "GET", "/api/view", "wrong", nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("wrong token: %d", resp.StatusCode)
	}
	if resp, _ := req(t, srv, "GET", "/", "", nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("page without cookie: %d", resp.StatusCode)
	}
	// token in URL sets the cookie and redirects
	resp, _ := req(t, srv, "GET", "/?t="+srv.token, "", nil)
	if resp.StatusCode != http.StatusFound || len(resp.Cookies()) != 1 || resp.Cookies()[0].Value != srv.token {
		t.Errorf("token exchange: %d cookies=%v", resp.StatusCode, resp.Cookies())
	}
	// bad host (DNS rebinding) is refused even with a token
	r, _ := http.NewRequest("GET", "http://"+srv.addr+"/api/view", nil)
	r.Host = "evil.example:80"
	r.Header.Set("X-Lagra-Token", srv.token)
	resp2, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
	if err != nil || resp2.StatusCode != http.StatusForbidden {
		t.Errorf("bad host: %v %v", err, resp2)
	}
	if !strings.Contains(srv.URL(), srv.token) {
		t.Error("URL must carry the token")
	}
}

func TestAPIFlow(t *testing.T) {
	srv, cfg, ctl := startServer(t)
	tok := srv.token
	resp, body := req(t, srv, "GET", "/api/meta", tok, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"s3"`) {
		t.Fatalf("meta: %d %s", resp.StatusCode, body)
	}
	resp, body = req(t, srv, "GET", "/api/view", tok, nil)
	var view settings.View
	_ = json.Unmarshal(body, &view)
	if resp.StatusCode != 200 || len(view.Mounts) != 1 || !view.Valid {
		t.Fatalf("view: %d %+v", resp.StatusCode, view)
	}
	if len(view.Accounts) != 1 || view.Accounts[0].UsedBy[0] != "archiv" {
		t.Fatalf("accounts: %+v", view.Accounts)
	}
	// invalid save is refused with issues
	resp, body = req(t, srv, "PUT", "/api/accounts/aws", tok, settings.AccountInput{Name: "aws", Options: map[string]string{"secret_access_key": "x"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "issues") {
		t.Errorf("invalid save: %d %s", resp.StatusCode, body)
	}
	// valid saves write the file
	resp, body = req(t, srv, "PUT", "/api/accounts/aws", tok, settings.AccountInput{Name: "aws", Options: map[string]string{"region": "eu"}})
	if resp.StatusCode != 200 {
		t.Fatalf("save account: %d %s", resp.StatusCode, body)
	}
	resp, body = req(t, srv, "PUT", "/api/mounts/archiv", tok, settings.MountInput{Name: "archiv", Account: "aws", Type: "s3", Path: "b/sub"})
	if resp.StatusCode != 200 {
		t.Fatalf("save mount: %d %s", resp.StatusCode, body)
	}
	raw, _ := os.ReadFile(cfg)
	if !strings.Contains(string(raw), `path = "b/sub"`) || !strings.Contains(string(raw), `region = "eu"`) || !strings.Contains(string(raw), `log_level = "info"`) {
		t.Errorf("file after save:\n%s", raw)
	}
	// new mount with a new account, connect, delete
	resp, body = req(t, srv, "POST", "/api/mounts", tok, settings.MountInput{Name: "zwei", Type: "s3", Path: "c",
		NewAccount: &settings.AccountInput{Name: "other", Options: map[string]string{"provider": "Other"}}})
	if resp.StatusCode != 200 {
		t.Fatalf("new: %d %s", resp.StatusCode, body)
	}
	if resp, _ = req(t, srv, "POST", "/api/mounts/zwei/connect", tok, nil); resp.StatusCode != 200 || len(ctl.connected) != 1 {
		t.Errorf("connect: %d %v", resp.StatusCode, ctl.connected)
	}
	// the account is in use, so it cannot go first
	if resp, _ = req(t, srv, "DELETE", "/api/accounts/other", tok, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("delete used account: %d", resp.StatusCode)
	}
	if resp, _ = req(t, srv, "DELETE", "/api/mounts/zwei", tok, nil); resp.StatusCode != 200 {
		t.Errorf("delete mount: %d", resp.StatusCode)
	}
	if resp, _ = req(t, srv, "DELETE", "/api/accounts/other", tok, nil); resp.StatusCode != 200 {
		t.Errorf("delete account: %d", resp.StatusCode)
	}
	raw, _ = os.ReadFile(cfg)
	if strings.Contains(string(raw), "zwei") || strings.Contains(string(raw), "other") {
		t.Errorf("zwei or other survived delete:\n%s", raw)
	}
	resp, body = req(t, srv, "PUT", "/api/general", tok, settings.GeneralInput{"log_level": "debug"})
	if resp.StatusCode != 200 {
		t.Errorf("general: %d %s", resp.StatusCode, body)
	}
	resp, _ = req(t, srv, "GET", "/app.js", tok, nil)
	if resp.StatusCode != 200 {
		t.Errorf("static: %d", resp.StatusCode)
	}
	resp, body = req(t, srv, "GET", "/", tok, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<title>Lagra</title>") {
		t.Errorf("index page: %d %.80s", resp.StatusCode, body)
	}
}

// A body the server cannot read is the page's fault (400, not logged); any
// other failure is ours (500, logged).
func TestErrorResponses(t *testing.T) {
	var logged bytes.Buffer
	srv, _, _ := startServer(t, func(d *Deps) { d.Log = slog.New(slog.NewTextHandler(&logged, nil)) })
	r, _ := http.NewRequest("PUT", "http://"+srv.addr+"/api/accounts/aws", strings.NewReader(`{"name": `))
	r.Header.Set("X-Lagra-Token", srv.token)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || logged.Len() != 0 {
		t.Errorf("broken body: %d, log %q", resp.StatusCode, logged.String())
	}
	resp, _ = req(t, srv, "GET", "/api/meta/options/nope", srv.token, nil)
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(logged.String(), "path=/api/meta/options/nope") {
		t.Errorf("server error: %d, log %q", resp.StatusCode, logged.String())
	}
}

// Another page on localhost must not be able to change anything, even when
// the browser sends our cookie along.
func TestCrossOriginWritesRefused(t *testing.T) {
	srv, _, ctl := startServer(t)
	post := func(header, value string) int {
		r, _ := http.NewRequest("POST", "http://"+srv.addr+"/api/mounts/archiv/connect", nil)
		r.AddCookie(&http.Cookie{Name: "lagra", Value: srv.token})
		if header != "" {
			r.Header.Set(header, value)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("Origin", "http://127.0.0.1:1"); code != http.StatusForbidden {
		t.Errorf("other origin: %d", code)
	}
	if code := post("Sec-Fetch-Site", "same-site"); code != http.StatusForbidden {
		t.Errorf("same-site page: %d", code)
	}
	if len(ctl.connected) != 0 {
		t.Fatalf("connected despite refusal: %v", ctl.connected)
	}
	if code := post("Origin", "http://"+srv.addr); code != http.StatusOK {
		t.Errorf("own page: %d", code)
	}
	if code := post("Sec-Fetch-Site", "same-origin"); code != http.StatusOK {
		t.Errorf("own page via fetch metadata: %d", code)
	}
}

func TestTailLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lagra.log")
	var b strings.Builder
	for i := range 5000 {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	_ = os.WriteFile(p, []byte(b.String()), 0o644)
	if got := tailLines(p, 3); len(got) != 3 || got[0] != "line 4997" || got[2] != "line 4999" {
		t.Errorf("tail of a long log = %q", got)
	}
	_ = os.WriteFile(p, []byte("a\nb\n"), 0o644)
	if got := tailLines(p, 3); len(got) != 2 || got[0] != "a" {
		t.Errorf("tail of a short log = %q", got)
	}
}
