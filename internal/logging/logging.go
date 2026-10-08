// Package logging sets up structured logs under ~/Library/Logs/lagra with
// simple size-based rotation. Secrets never reach the logger by design: the
// only place that holds them (rclone.Spec) is never logged as a whole.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DefaultDir is where log files go.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Logs", "lagra"), nil
}

// Level parses lagra's log level names.
func Level(name string) slog.Level {
	switch strings.ToLower(name) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// Rotating is an io.Writer that starts a new file once the current one
// exceeds MaxBytes, keeping Keep old files (lagra.log.1 ... lagra.log.N).
type Rotating struct {
	Path     string
	MaxBytes int64
	Keep     int

	mu   sync.Mutex
	f    *os.File
	size int64
}

// Write implements io.Writer.
func (r *Rotating) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.MaxBytes > 0 && r.size+int64(len(p)) > r.MaxBytes {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *Rotating) open() error {
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(r.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, info.Size()
	return nil
}

func (r *Rotating) rotate() error {
	_ = r.f.Close()
	r.f = nil
	keep := max(r.Keep, 1)
	_ = os.Remove(fmt.Sprintf("%s.%d", r.Path, keep))
	for i := keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.Path, i), fmt.Sprintf("%s.%d", r.Path, i+1))
	}
	_ = os.Rename(r.Path, r.Path+".1")
	return r.open()
}

// Close closes the current file.
func (r *Rotating) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// LevelVar lets the log level change at runtime (a live config change).
var LevelVar = new(slog.LevelVar)

// New builds the logger writing to w at the current LevelVar level.
func New(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: LevelVar}))
}
