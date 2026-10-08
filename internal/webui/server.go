// Package webui serves the settings page on 127.0.0.1 with a per-process
// token. The native window (internal/window) and `lagra settings` open it;
// everything it does goes through the settings service and the daemon's
// controller, so the file stays the single source of truth.
package webui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eltupe/lagra/internal/config"
	"github.com/eltupe/lagra/internal/ipc"
	"github.com/eltupe/lagra/internal/settings"
)

//go:embed static/*
var static embed.FS

// Meta lists what the UI can offer.
type Meta struct {
	Backends []Backend           `json:"backends"`
	VFS      []config.OptionInfo `json:"vfs"`
	Version  string              `json:"version"`
	Home     string              `json:"home"`
	Signing  Signing             `json:"signing"`
}

// Signing describes how the running app is signed. Privacy permissions are
// bound to the signing identity; an ad-hoc signature changes on every build,
// so macOS asks again after each update.
type Signing struct {
	Identity string `json:"identity"` // e.g. "lagra Developer", "" when ad hoc
	Stable   bool   `json:"stable"`
	Bundle   bool   `json:"bundle"` // running from a .app bundle
}

// Backend is one backend type.
type Backend struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Deps are what the server needs.
type Deps struct {
	Settings *settings.Service
	Ctl      ipc.Controller
	Registry config.Registry
	Backends func() []Backend
	LogPath  string
	Version  string
	// PickDirectory shows a native folder chooser; nil disables the button.
	PickDirectory func(start string) (string, bool)
	// Files replaces the embedded page files; hack/uipreview serves them
	// from disk so page edits show on reload.
	Files fs.FS
}

// Server is the settings HTTP server.
type Server struct {
	deps  Deps
	token string
	addr  string
	ln    net.Listener
}

// New creates a server bound to a random loopback port.
func New(deps Deps) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return &Server{deps: deps, token: hex.EncodeToString(b[:]), addr: ln.Addr().String(), ln: ln}, nil
}

// URL returns the address to open, token included.
func (s *Server) URL() string { return "http://" + s.addr + "/?t=" + s.token }

// Serve blocks until ctx is done.
func (s *Server) Serve(ctx context.Context) error {
	srv := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(s.ln) }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	if s.deps.Files != nil {
		sub = s.deps.Files
	}
	files := http.FileServer(http.FS(sub))

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if t := r.URL.Query().Get("t"); t != "" {
			if !s.isToken(t) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "lagra", Value: t, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		if !s.authed(r) {
			http.Error(w, "forbidden: open the settings from the Lagra menu", http.StatusForbidden)
			return
		}
		// Served directly: http.FileServer would 301 "/index.html" back to "/".
		page, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.Error(w, "missing page", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(page)
	})
	mux.Handle("GET /app.js", s.static(files))
	mux.Handle("GET /style.css", s.static(files))

	api := func(pattern string, fn func(r *http.Request) (any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if !s.authed(r) || (r.Method != http.MethodGet && !sameOrigin(r)) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
				return
			}
			out, err := fn(r)
			if err != nil {
				var ve *settings.ValidationError
				switch {
				case errors.As(err, &ve):
					writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": ve.Error(), "issues": ve.Issues})
				case errors.Is(err, ipc.ErrNoMount):
					writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
				default:
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				}
				return
			}
			if out == nil {
				out = map[string]bool{"ok": true}
			}
			writeJSON(w, http.StatusOK, out)
		})
	}

	api("GET /api/meta", func(r *http.Request) (any, error) {
		home, _ := os.UserHomeDir()
		var bes []Backend
		if s.deps.Backends != nil {
			bes = s.deps.Backends()
		}
		return Meta{Backends: bes, VFS: s.deps.Registry.VFS().Options(), Version: s.deps.Version, Home: home, Signing: signingInfo()}, nil
	})
	api("POST /api/open-privacy", func(r *http.Request) (any, error) {
		pane := "Privacy_FilesAndFolders"
		if r.URL.Query().Get("pane") == "network" {
			pane = "Privacy_NetworkVolumes"
		}
		return nil, exec.Command("/usr/bin/open", "x-apple.systempreferences:com.apple.preference.security?"+pane).Start()
	})
	api("GET /api/meta/options/{type}", func(r *http.Request) (any, error) {
		set, ok := s.deps.Registry.Backend(r.PathValue("type"))
		if !ok {
			return nil, fmt.Errorf("unsupported backend %q", r.PathValue("type"))
		}
		return set.Options(), nil
	})
	api("GET /api/view", func(r *http.Request) (any, error) {
		return s.deps.Settings.Load(r.Context())
	})
	api("GET /api/status", func(r *http.Request) (any, error) {
		return s.deps.Ctl.Status(), nil
	})
	api("POST /api/config/create", func(r *http.Request) (any, error) {
		return nil, s.deps.Settings.Create()
	})
	api("POST /api/accounts", func(r *http.Request) (any, error) {
		var in settings.AccountInput
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return nil, s.deps.Settings.SaveAccount(r.Context(), "", in)
	})
	api("PUT /api/accounts/{name}", func(r *http.Request) (any, error) {
		var in settings.AccountInput
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return nil, s.deps.Settings.SaveAccount(r.Context(), r.PathValue("name"), in)
	})
	api("DELETE /api/accounts/{name}", func(r *http.Request) (any, error) {
		return nil, s.deps.Settings.DeleteAccount(r.Context(), r.PathValue("name"), r.URL.Query().Get("secrets") == "1")
	})
	api("POST /api/mounts", func(r *http.Request) (any, error) {
		var in settings.MountInput
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return nil, s.deps.Settings.SaveMount(r.Context(), "", in)
	})
	api("PUT /api/mounts/{name}", func(r *http.Request) (any, error) {
		var in settings.MountInput
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return nil, s.deps.Settings.SaveMount(r.Context(), r.PathValue("name"), in)
	})
	api("DELETE /api/mounts/{name}", func(r *http.Request) (any, error) {
		return nil, s.deps.Settings.DeleteMount(r.Context(), r.PathValue("name"))
	})
	api("PUT /api/general", func(r *http.Request) (any, error) {
		var in settings.GeneralInput
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return nil, s.deps.Settings.SaveGeneral(in)
	})
	api("PUT /api/defaults", func(r *http.Request) (any, error) {
		var in settings.DefaultsInput
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return nil, s.deps.Settings.SaveDefaults(in)
	})
	api("POST /api/mounts/{name}/connect", func(r *http.Request) (any, error) {
		return nil, s.deps.Ctl.Connect(r.PathValue("name"))
	})
	api("POST /api/mounts/{name}/disconnect", func(r *http.Request) (any, error) {
		return nil, s.deps.Ctl.Disconnect(r.PathValue("name"), r.URL.Query().Get("force") == "1")
	})
	api("POST /api/mounts/{name}/sync", func(r *http.Request) (any, error) {
		return s.deps.Ctl.Sync(r.PathValue("name"))
	})
	api("POST /api/reload", func(r *http.Request) (any, error) {
		return s.deps.Ctl.Reload(), nil
	})
	api("POST /api/pick-dir", func(r *http.Request) (any, error) {
		if s.deps.PickDirectory == nil {
			return map[string]any{"available": false}, nil
		}
		p, ok := s.deps.PickDirectory(r.URL.Query().Get("start"))
		return map[string]any{"available": true, "ok": ok, "path": p}, nil
	})
	// Open things on the Mac: only the config file, the log folder or a mount
	// point of a configured mount — never an arbitrary path from the page.
	api("POST /api/open", func(r *http.Request) (any, error) {
		what := r.URL.Query().Get("what")
		var args []string
		switch what {
		case "config":
			p := s.deps.Settings.Path
			if real, err := filepath.EvalSymlinks(p); err == nil {
				p = real
			}
			args = []string{"-t", p}
		case "logs":
			args = []string{filepath.Dir(s.deps.LogPath)}
		case "mount":
			name := r.URL.Query().Get("name")
			for _, m := range s.deps.Ctl.Status().Mounts {
				if m.Name == name && m.MountPoint != "" {
					args = []string{m.MountPoint}
				}
			}
			if args == nil {
				return nil, fmt.Errorf("%w: %s", ipc.ErrNoMount, name)
			}
		default:
			return nil, fmt.Errorf("cannot open %q", what)
		}
		return nil, exec.Command("/usr/bin/open", args...).Start()
	})
	api("GET /api/logs", func(r *http.Request) (any, error) {
		n, _ := strconv.Atoi(r.URL.Query().Get("tail"))
		if n <= 0 {
			n = 200
		}
		return map[string]any{"path": s.deps.LogPath, "lines": tailLines(s.deps.LogPath, n)}, nil
	})
	return s.hostCheck(mux)
}

func (s *Server) static(files http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	})
}

// hostCheck defends against DNS rebinding: only our own loopback host.
func (s *Server) hostCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.addr && r.Host != strings.Replace(s.addr, "127.0.0.1", "localhost", 1) {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authed(r *http.Request) bool {
	if h := r.Header.Get("X-Lagra-Token"); h != "" {
		return s.isToken(h)
	}
	c, err := r.Cookie("lagra")
	return err == nil && s.isToken(c.Value)
}

func (s *Server) isToken(t string) bool {
	return subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1
}

// sameOrigin rejects changes that another local page made a browser send:
// cookies are per host, not per port, so a page on any other localhost port
// would carry ours. A request that says where it comes from must come from
// this server; one that says nothing (the CLI, tests) passes.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
		return false
	}
	return true
}

func decode(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("bad request: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// signingInfo asks codesign about the running executable's bundle, once.
var signingInfo = sync.OnceValue(func() Signing {
	var sg Signing
	exe, err := os.Executable()
	if err != nil {
		return sg
	}
	target := exe
	if i := strings.Index(exe, ".app/Contents/MacOS/"); i >= 0 {
		target = exe[:i+4]
		sg.Bundle = true
	}
	out, _ := exec.Command("/usr/bin/codesign", "-dvv", target).CombinedOutput()
	for line := range strings.Lines(string(out)) {
		if id, ok := strings.CutPrefix(strings.TrimSpace(line), "Authority="); ok {
			sg.Identity, sg.Stable = id, true
			break
		}
	}
	return sg
})

// tailLines returns the last n lines of the log. It reads only the end of
// the file, about a kilobyte per line.
func tailLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	off := max(0, st.Size()-int64(n)<<10)
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return nil
	}
	ls := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if off > 0 {
		ls = ls[1:] // starts mid-line
	}
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return ls
}
