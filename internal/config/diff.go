package config

import (
	"reflect"
	"sort"
)

// Diff describes what changed between two effective configurations, per
// mount, identified by name. A renamed mount shows up as removed plus added.
type Diff struct {
	Added     []string // connect
	Removed   []string // disconnect (gone from file or enabled = false)
	Restart   []string // disconnect, then connect with the new definition
	Unchanged []string // do not touch
}

// Compute compares old and new. A nil old means "first load": every enabled
// mount is Added.
func Compute(oldCfg, newCfg *Effective) Diff {
	var d Diff
	oldM := map[string]EffectiveMount{}
	if oldCfg != nil {
		for _, m := range oldCfg.Mounts {
			oldM[m.Name] = m
		}
	}
	newM := map[string]EffectiveMount{}
	for _, m := range newCfg.Mounts {
		newM[m.Name] = m
	}

	for name, nm := range newM {
		om, existed := oldM[name]
		switch {
		case !existed || !om.Enabled:
			if nm.Enabled {
				d.Added = append(d.Added, name)
			} else {
				d.Unchanged = append(d.Unchanged, name)
			}
		case !nm.Enabled:
			d.Removed = append(d.Removed, name)
		case SameDefinition(om, nm):
			d.Unchanged = append(d.Unchanged, name)
		default:
			d.Restart = append(d.Restart, name)
		}
	}
	for name, om := range oldM {
		if _, still := newM[name]; !still && om.Enabled {
			d.Removed = append(d.Removed, name)
		}
	}
	for _, s := range []*[]string{&d.Added, &d.Removed, &d.Restart, &d.Unchanged} {
		sort.Strings(*s)
	}
	return d
}

// SameDefinition reports whether a and b mount the same thing the same way.
// It ignores Enabled (handled by the caller) and the probe intervals, which
// are live changes that the coordinator picks up without a restart.
func SameDefinition(a, b EffectiveMount) bool {
	a.Enabled, b.Enabled = true, true
	a.MountProbeInterval, b.MountProbeInterval = 0, 0
	a.BackendProbeInterval, b.BackendProbeInterval = 0, 0
	return reflect.DeepEqual(a, b)
}
