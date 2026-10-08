package config

import (
	"fmt"
	"slices"
	"strings"
)

// Issue is one validation finding with the field it concerns and, when known,
// the line in the file.
type Issue struct {
	Path    string `json:"path"` // e.g. mount[1].options.region or general.log_level
	Line    int    `json:"line"` // 1-based, 0 = unknown
	Msg     string `json:"msg"`
	Warning bool   `json:"warning"`
}

func (i *Issue) Error() string {
	var b strings.Builder
	if i.Line > 0 {
		fmt.Fprintf(&b, "line %d: ", i.Line)
	}
	if i.Path != "" {
		fmt.Fprintf(&b, "%s: ", i.Path)
	}
	b.WriteString(i.Msg)
	return b.String()
}

// Issues is a list of findings that is itself an error when it contains at
// least one non-warning issue.
type Issues []*Issue

func (is Issues) Error() string {
	var parts []string
	for _, i := range is {
		if !i.Warning {
			parts = append(parts, i.Error())
		}
	}
	return strings.Join(parts, "\n")
}

// HasErrors reports whether any issue is not a warning.
func (is Issues) HasErrors() bool {
	for _, i := range is {
		if !i.Warning {
			return true
		}
	}
	return false
}

// Warnings returns only the warnings.
func (is Issues) Warnings() Issues {
	var out Issues
	for _, i := range is {
		if i.Warning {
			out = append(out, i)
		}
	}
	return out
}

// sorted orders issues by line, unknown lines last.
func (is Issues) sorted() Issues {
	out := slices.Clone(is)
	key := func(i *Issue) int {
		if i.Line == 0 {
			return 1 << 30
		}
		return i.Line
	}
	slices.SortStableFunc(out, func(a, b *Issue) int { return key(a) - key(b) })
	return out
}
