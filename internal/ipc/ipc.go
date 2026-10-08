// Package ipc connects the CLI to the running daemon over a Unix socket with
// a tiny HTTP/JSON API. The socket lives next to the state file and is
// created with mode 0600.
package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/eltupe/lagra/internal/engine"
)

// Status is what the daemon reports.
type Status struct {
	SettingsURL string    `json:"settings_url,omitempty"`
	Version     string    `json:"version"`
	PID         int       `json:"pid"`
	StartedAt   time.Time `json:"started_at"`
	ConfigPath  string    `json:"config_path"`
	ConfigReal  string    `json:"config_real_path"`
	ConfigOK    bool      `json:"config_ok"`
	ConfigError string    `json:"config_error,omitempty"`
	// ConfigMissing: no config file yet, Settings creates one.
	ConfigMissing bool              `json:"config_missing,omitempty"`
	LoadedAt      time.Time         `json:"config_loaded_at"`
	Overall       engine.Overall    `json:"overall"`
	Mounts        []engine.Snapshot `json:"mounts"`
}

// SyncResult reports a manual sync.
type SyncResult struct {
	Expedited int `json:"expedited"`
}

// RestartResult lists the mounts an account restart reconnects.
type RestartResult struct {
	Restarted []string `json:"restarted"`
}

// ReloadResult reports one reload attempt.
type ReloadResult struct {
	OK       bool     `json:"ok"`
	Issues   []string `json:"issues,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Added    []string `json:"added,omitempty"`
	Removed  []string `json:"removed,omitempty"`
	Restart  []string `json:"restart,omitempty"`
}

// Controller is what the daemon exposes to the API.
type Controller interface {
	Status() Status
	Connect(name string) error
	Disconnect(name string, force bool) error
	// RestartAccount reconnects the running mounts of an account so new
	// secrets take effect, and returns their names. Pending-upload rules
	// apply.
	RestartAccount(account string) []string
	// Sync expedites pending uploads of one mount and refreshes its listing.
	Sync(name string) (SyncResult, error)
	Reload() ReloadResult
	// Shutdown returns an error when uploads are pending and force is false.
	Shutdown(force bool) error
}

// ErrPendingUploads is returned by Shutdown when uploads are still queued.
var ErrPendingUploads = errors.New("uploads pending")

// ErrNoMount is returned for unknown mount names.
var ErrNoMount = errors.New("no such mount")

// DefaultSocket is ~/Library/Application Support/lagra/lagra.sock.
func DefaultSocket() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "lagra", "lagra.sock"), nil
}

// Server serves the API.
type Server struct {
	Socket string
	Ctl    Controller
}

// ErrAlreadyRunning means another daemon answers on the socket.
var ErrAlreadyRunning = errors.New("another lagra daemon is already running")

// Listen claims the socket. The daemon calls it before it touches any
// mount, so a second daemon gives up instead of fighting the first one.
func (s *Server) Listen() (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(s.Socket), 0o700); err != nil {
		return nil, err
	}
	// A stale socket from a crashed daemon: if nobody answers, take it over.
	if _, err := os.Stat(s.Socket); err == nil {
		if Ping(s.Socket) {
			return nil, fmt.Errorf("%w on %s", ErrAlreadyRunning, s.Socket)
		}
		_ = os.Remove(s.Socket)
	}
	ln, err := net.Listen("unix", s.Socket)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(s.Socket, 0o600)
	return ln, nil
}

// ListenAndServe claims the socket and serves until ctx is done.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := s.Listen()
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve answers requests on ln until ctx is done or the listener fails.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Ctl.Status())
	})
	mux.HandleFunc("POST /v1/mounts/{name}/connect", func(w http.ResponseWriter, r *http.Request) {
		s.handleErr(w, s.Ctl.Connect(r.PathValue("name")))
	})
	mux.HandleFunc("POST /v1/mounts/{name}/disconnect", func(w http.ResponseWriter, r *http.Request) {
		s.handleErr(w, s.Ctl.Disconnect(r.PathValue("name"), r.URL.Query().Get("force") == "1"))
	})
	mux.HandleFunc("POST /v1/mounts/{name}/sync", func(w http.ResponseWriter, r *http.Request) {
		res, err := s.Ctl.Sync(r.PathValue("name"))
		if err != nil {
			s.handleErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("POST /v1/accounts/{name}/restart", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, RestartResult{Restarted: s.Ctl.RestartAccount(r.PathValue("name"))})
	})
	mux.HandleFunc("POST /v1/reload", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Ctl.Reload())
	})
	mux.HandleFunc("POST /v1/shutdown", func(w http.ResponseWriter, r *http.Request) {
		s.handleErr(w, s.Ctl.Shutdown(r.URL.Query().Get("force") == "1"))
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		_ = os.Remove(s.Socket)
		return nil
	case err := <-errc:
		_ = os.Remove(s.Socket)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) handleErr(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case errors.Is(err, ErrNoMount):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrPendingUploads):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Client talks to the daemon.
type Client struct {
	Socket string
	http   *http.Client
}

// NewClient returns a client for the socket.
func NewClient(socket string) *Client {
	return &Client{Socket: socket, http: &http.Client{
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}},
		Timeout: 30 * time.Second,
	}}
}

// Ping reports whether a daemon answers on the socket.
func Ping(socket string) bool {
	c := NewClient(socket)
	c.http.Timeout = 2 * time.Second
	_, err := c.Status(context.Background())
	return err == nil
}

// ErrNoDaemon means nothing is listening.
var ErrNoDaemon = errors.New("lagra is not running")

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, "http://lagra"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// no socket, or nobody behind it
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return ErrNoDaemon
		}
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		msg := e.Error
		if msg == "" {
			msg = resp.Status
		}
		switch resp.StatusCode {
		case http.StatusNotFound:
			return &wrapped{ErrNoMount, msg}
		case http.StatusConflict:
			return &wrapped{ErrPendingUploads, msg}
		}
		return errors.New(msg)
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

// wrapped keeps the server's message verbatim while matching a sentinel.
type wrapped struct {
	sentinel error
	msg      string
}

func (w *wrapped) Error() string        { return w.msg }
func (w *wrapped) Is(target error) bool { return target == w.sentinel }

// Status fetches the daemon status.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	var st Status
	if err := c.do(ctx, http.MethodGet, "/v1/status", &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Connect asks the daemon to connect a mount.
func (c *Client) Connect(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPost, "/v1/mounts/"+name+"/connect", nil)
}

// Disconnect asks the daemon to disconnect a mount.
func (c *Client) Disconnect(ctx context.Context, name string, force bool) error {
	q := ""
	if force {
		q = "?force=1"
	}
	return c.do(ctx, http.MethodPost, "/v1/mounts/"+name+"/disconnect"+q, nil)
}

// Sync asks the daemon to push pending uploads and refresh a mount now.
func (c *Client) Sync(ctx context.Context, name string) (*SyncResult, error) {
	var r SyncResult
	if err := c.do(ctx, http.MethodPost, "/v1/mounts/"+name+"/sync", &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// RestartAccount asks the daemon to reconnect the mounts of an account
// (after a secret change) and returns their names.
func (c *Client) RestartAccount(ctx context.Context, account string) ([]string, error) {
	var r RestartResult
	if err := c.do(ctx, http.MethodPost, "/v1/accounts/"+account+"/restart", &r); err != nil {
		return nil, err
	}
	return r.Restarted, nil
}

// Reload asks the daemon to reload its config.
func (c *Client) Reload(ctx context.Context) (*ReloadResult, error) {
	var r ReloadResult
	if err := c.do(ctx, http.MethodPost, "/v1/reload", &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Shutdown asks the daemon to quit.
func (c *Client) Shutdown(ctx context.Context, force bool) error {
	q := ""
	if force {
		q = "?force=1"
	}
	return c.do(ctx, http.MethodPost, "/v1/shutdown"+q, nil)
}
