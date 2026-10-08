package config

import (
	"reflect"
	"testing"
	"time"
)

func mnt(name string, enabled bool, opts map[string]string) EffectiveMount {
	return EffectiveMount{Name: name, Type: "s3", Path: "b", MountPoint: "/m/" + name, Enabled: enabled,
		MountProbeInterval: 15 * time.Second, Options: opts, VFS: map[string]string{}, Secrets: map[string]SecretSource{}}
}

func eff(g EffectiveGeneral, ms ...EffectiveMount) *Effective {
	return &Effective{General: g, Mounts: ms}
}

func TestComputeFirstLoad(t *testing.T) {
	d := Compute(nil, eff(EffectiveGeneral{}, mnt("a", true, nil), mnt("b", false, nil)))
	if !reflect.DeepEqual(d.Added, []string{"a"}) || !reflect.DeepEqual(d.Unchanged, []string{"b"}) {
		t.Errorf("first load diff = %+v", d)
	}
}

func TestComputeCases(t *testing.T) {
	g := EffectiveGeneral{LogLevel: "info"}
	old := eff(g,
		mnt("same", true, map[string]string{"region": "eu"}),
		mnt("changed", true, map[string]string{"region": "eu"}),
		mnt("gone", true, nil),
		mnt("disabled-now", true, nil),
		mnt("was-disabled", false, nil),
		mnt("still-disabled", false, nil),
		mnt("live-only", true, nil),
	)
	live := mnt("live-only", true, nil)
	live.MountProbeInterval = time.Minute
	g2 := g
	g2.LogLevel = "debug"
	nw := eff(g2,
		mnt("same", true, map[string]string{"region": "eu"}),
		mnt("changed", true, map[string]string{"region": "us"}),
		mnt("disabled-now", false, nil),
		mnt("was-disabled", true, nil),
		mnt("still-disabled", false, nil),
		mnt("new", true, nil),
		live,
	)
	d := Compute(old, nw)
	want := Diff{
		Added:     []string{"new", "was-disabled"},
		Removed:   []string{"disabled-now", "gone"},
		Restart:   []string{"changed"},
		Unchanged: []string{"live-only", "same", "still-disabled"},
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("diff =\n%+v\nwant\n%+v", d, want)
	}
	if same := Compute(old, old); len(same.Added)+len(same.Removed)+len(same.Restart) != 0 {
		t.Errorf("identical configs must change nothing: %+v", same)
	}
}

func TestRenameIsRemovePlusAdd(t *testing.T) {
	old := eff(EffectiveGeneral{}, mnt("alt", true, nil))
	nw := eff(EffectiveGeneral{}, mnt("neu", true, nil))
	d := Compute(old, nw)
	if !reflect.DeepEqual(d.Removed, []string{"alt"}) || !reflect.DeepEqual(d.Added, []string{"neu"}) {
		t.Errorf("rename diff = %+v", d)
	}
}

func accountsConfig(account, region string) string {
	return `[[account]]
name = "` + account + `"
options = { region = "` + region + `" }
[[account]]
name = "b"
[[mount]]
name = "m1"
account = "` + account + `"
type = "s3"
path = "x"
[[mount]]
name = "m2"
account = "` + account + `"
type = "s3"
path = "y"
[[mount]]
name = "other"
account = "b"
type = "s3"
path = "z"
`
}

func TestAccountChangeRestartsItsMounts(t *testing.T) {
	old, _ := load(t, accountsConfig("a", "eu"))
	for name, src := range map[string]string{"option": accountsConfig("a", "us"), "rename": accountsConfig("c", "eu")} {
		nw, issues := load(t, src)
		if issues.HasErrors() {
			t.Fatal(issues)
		}
		d := Compute(old, nw)
		if !reflect.DeepEqual(d.Restart, []string{"m1", "m2"}) || !reflect.DeepEqual(d.Unchanged, []string{"other"}) {
			t.Errorf("%s: diff = %+v", name, d)
		}
	}
}
