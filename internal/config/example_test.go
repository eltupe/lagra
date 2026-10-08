package config

import (
	"testing"
)

// The shipped example must parse; semantic validation with real rclone
// metadata happens in the rclone package tests.
func TestExampleParses(t *testing.T) {
	doc, err := Parse("example.toml", Example)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.File.Mounts) != 5 || doc.File.Mounts[0].Name != "archiv" {
		t.Errorf("mounts = %+v", doc.File.Mounts)
	}
	if len(doc.File.Accounts) != 4 || doc.File.Accounts[0].Name != "aws" {
		t.Errorf("accounts = %+v", doc.File.Accounts)
	}
	if len(doc.undecoded) != 0 {
		t.Errorf("example has unknown keys: %v", doc.undecoded)
	}
}
