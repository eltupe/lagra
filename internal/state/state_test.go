package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingIsEmpty(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "nested", "state.json")}
	f, err := s.Load()
	if err != nil || len(f.Mounts) != 0 || f.Version != Version {
		t.Fatalf("got %+v %v", f, err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "nested", "state.json")}
	err := s.Update(func(f *File) {
		f.Mounts["archiv"] = Managed{Name: "archiv", MountPoint: "/Users/x/Volumes/archiv", Port: 51234, PID: 42, StartedAt: time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)}
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if m := f.Mounts["archiv"]; m.Port != 51234 || m.PID != 42 || m.MountPoint != "/Users/x/Volumes/archiv" {
		t.Errorf("got %+v", m)
	}
	info, _ := os.Stat(s.Path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("state file must be 0600, got %v", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(s.Path))
	if len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
}

func TestConcurrentUpdates(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "state.json")}
	done := make(chan struct{})
	for i := range 20 {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			_ = s.Update(func(f *File) { f.Mounts[string(rune('a'+i))] = Managed{Name: "x"} })
		}(i)
	}
	for range 20 {
		<-done
	}
	f, err := s.Load()
	if err != nil || len(f.Mounts) != 20 {
		t.Fatalf("got %d mounts, %v", len(f.Mounts), err)
	}
}

func TestLoadCorruptFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	_ = os.WriteFile(p, []byte("{nope"), 0o600)
	if _, err := (&Store{Path: p}).Load(); err == nil {
		t.Fatal("expected parse error")
	}
}
