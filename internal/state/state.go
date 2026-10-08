// Package state persists what lagra must remember between runs: the mounts it
// created itself (managed mounts), so that orphans can be cleaned up after a
// crash and foreign mounts are never touched. It holds no user configuration
// and no secrets.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/eltupe/lagra/internal/atomicfile"
)

// Version of the on-disk format.
const Version = 1

// Managed describes one mount lagra created.
type Managed struct {
	Name       string    `json:"name"`
	MountPoint string    `json:"mount_point"`
	Port       int       `json:"port"`
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
}

// File is the persisted document.
type File struct {
	Version int                `json:"version"`
	Mounts  map[string]Managed `json:"mounts"`
}

// Store reads and writes one state file atomically. It is safe for
// concurrent use within one process; coordinators update it independently.
type Store struct {
	Path string
	mu   sync.Mutex
}

// DefaultPath is ~/Library/Application Support/lagra/state.json.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "lagra", "state.json"), nil
}

// Load returns the state, or an empty one if the file does not exist yet.
func (s *Store) Load() (*File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) load() (*File, error) {
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return &File{Version: Version, Mounts: map[string]Managed{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", s.Path, err)
	}
	if f.Mounts == nil {
		f.Mounts = map[string]Managed{}
	}
	f.Version = Version
	return &f, nil
}

// Save writes the state via a temp file and rename so a crash never leaves a
// half-written document.
func (s *Store) Save(f *File) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(f)
}

func (s *Store) save(f *File) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("state dir: %w", err)
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.Write(s.Path, raw, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// Update loads, applies fn and saves in one step.
func (s *Store) Update(fn func(*File)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	fn(f)
	return s.save(f)
}
