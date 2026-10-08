package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReplacesAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	for _, content := range []string{"one", "two"} {
		if err := Write(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(p)
	info, _ := os.Stat(p)
	if string(raw) != "two" || info.Mode().Perm() != 0o600 {
		t.Errorf("content %q, mode %v", raw, info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("leftovers: %v", entries)
	}
}

func TestWriteFailureLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sub") // a directory: the rename fails
	_ = os.Mkdir(target, 0o755)
	_ = os.WriteFile(filepath.Join(target, "keep"), nil, 0o644)
	if err := Write(target, []byte("x"), 0o644); err == nil {
		t.Fatal("renaming over a non-empty directory must fail")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("leftovers: %v", entries)
	}
}
