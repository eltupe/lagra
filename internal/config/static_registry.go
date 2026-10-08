package config

import (
	"fmt"
	"strconv"
	"time"
)

// StaticRegistry is a Registry backed by plain data, for tests; the real
// registry lives in the rclone adapter.
type StaticRegistry struct {
	Backends    map[string][]OptionInfo
	VFSOpts     []OptionInfo
	BucketBased map[string]bool
}

// Backend implements Registry.
func (r *StaticRegistry) Backend(typ string) (OptionSet, bool) {
	opts, ok := r.Backends[typ]
	if !ok {
		return nil, false
	}
	return staticSet(opts), true
}

// VFS implements Registry.
func (r *StaticRegistry) VFS() OptionSet { return staticSet(r.VFSOpts) }

// NeedsBucket implements Registry.
func (r *StaticRegistry) NeedsBucket(typ string) bool { return r.BucketBased[typ] }

// Option implements Registry.
func (r *StaticRegistry) Option(name string) (OptionInfo, bool) {
	var out OptionInfo
	found := false
	for _, opts := range r.Backends {
		if o, ok := staticSet(opts).Lookup(name); ok {
			if !found {
				out = o
			}
			found = true
			out.Sensitive = out.Sensitive || o.Sensitive
			out.Password = out.Password || o.Password
		}
	}
	return out, found
}

type staticSet []OptionInfo

func (s staticSet) Lookup(name string) (OptionInfo, bool) {
	for _, o := range s {
		if o.Name == name {
			return o, true
		}
	}
	return OptionInfo{}, false
}

func (s staticSet) Options() []OptionInfo { return []OptionInfo(s) }

// Check does a light type check based on OptionInfo.Type names as rclone
// reports them ("int", "bool", "Duration", "SizeSuffix", "string", ...).
func (s staticSet) Check(name, value string) error {
	o, ok := s.Lookup(name)
	if !ok {
		return fmt.Errorf("unknown option %q", name)
	}
	var err error
	switch o.Type {
	case "int", "int64":
		_, err = strconv.ParseInt(value, 10, 64)
	case "bool":
		_, err = strconv.ParseBool(value)
	case "Duration":
		_, err = time.ParseDuration(value)
	}
	if err != nil {
		return fmt.Errorf("option %q: %q is not a valid %s", name, value, o.Type)
	}
	if o.Exclusive && len(o.Examples) > 0 {
		for _, e := range o.Examples {
			if e == value {
				return nil
			}
		}
		return fmt.Errorf("option %q: %q is not one of %v", name, value, o.Examples)
	}
	return nil
}
