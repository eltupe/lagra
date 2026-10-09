package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	r := &Rotating{Path: filepath.Join(dir, "lagra.log"), MaxBytes: 100, Keep: 2}
	defer r.Close()
	line := strings.Repeat("x", 40) + "\n"
	for range 8 { // 8*41 bytes → several rotations
		if _, err := r.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 3 {
		t.Errorf("want lagra.log, .1, .2; got %v", names)
	}
	if _, err := os.Stat(filepath.Join(dir, "lagra.log.3")); !os.IsNotExist(err) {
		t.Error("keep limit not enforced")
	}
}
