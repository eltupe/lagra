// Package tomledit performs surgical edits on lagra's config file text so that
// the settings window can write the file while everything the user wrote by
// hand — comments, ordering, formatting — survives outside the edited part.
//
// Granularity: a whole [[account]] or [[mount]] block (header up to the next
// header that is not one of its own [account.*] / [mount.*] sub-tables) is
// replaced, removed or inserted as a unit. Keys inside [general], [defaults]
// and [defaults.vfs] are edited individually.
package tomledit

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/eltupe/lagra/internal/config"
)

// Block kinds.
const (
	Account = "account"
	Mount   = "mount"
)

var (
	reArrayHeader = regexp.MustCompile(`^\s*\[\[\s*(account|mount)\s*\]\]\s*(#.*)?$`)
	reAnyHeader   = regexp.MustCompile(`^\s*\[`)
	reName        = regexp.MustCompile(`^\s*name\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	reComment     = regexp.MustCompile(`^\s*(#.*)?$`)
)

// block is one [[account]] or [[mount]] block's line range [start, end).
type block struct {
	kind, name string
	start, end int
}

func lines(src string) []string {
	ls := strings.Split(src, "\n")
	// Split leaves a trailing "" for a file ending in \n; keep it so Join
	// reproduces the original ending.
	return ls
}

// kindOf returns "account" or "mount" for an array-of-tables header line.
func kindOf(l string) string {
	if m := reArrayHeader.FindStringSubmatch(l); m != nil {
		return m[1]
	}
	return ""
}

// blocks lists the blocks of one kind in file order. A block ends at the
// next header that is not one of its own sub-tables.
func blocks(ls []string, kind string) []block {
	var out []block
	own := regexp.MustCompile(`^\s*\[\s*` + kind + `\.`)
	for i, l := range ls {
		if kindOf(l) != kind {
			continue
		}
		b := block{kind: kind, start: i, end: len(ls)}
		for j := i + 1; j < len(ls); j++ {
			if reAnyHeader.MatchString(ls[j]) && !own.MatchString(ls[j]) {
				b.end = j
				break
			}
		}
		for j := i + 1; j < b.end; j++ {
			if reAnyHeader.MatchString(ls[j]) {
				break // name must precede the sub-tables
			}
			if m := reName.FindStringSubmatch(ls[j]); m != nil {
				b.name = m[1] + m[2] // basic or literal string
				break
			}
		}
		// trim trailing blank and comment lines: they belong to whatever
		// follows, and a replacement keeps the same spacing
		for b.end > b.start+1 && reComment.MatchString(ls[b.end-1]) {
			b.end--
		}
		out = append(out, b)
	}
	return out
}

// Names lists the names of all blocks of one kind in file order.
func Names(src, kind string) []string {
	var out []string
	for _, b := range blocks(lines(src), kind) {
		out = append(out, b.name)
	}
	return out
}

// Replace replaces the block of kind with that name by rendered. When no
// such block exists, rendered is inserted as a new one.
func Replace(src, kind, name, rendered string) string {
	ls := lines(src)
	for _, b := range blocks(ls, kind) {
		if b.name != name {
			continue
		}
		newLines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
		out := append([]string{}, ls[:b.start]...)
		out = append(out, newLines...)
		out = append(out, ls[b.end:]...)
		return strings.Join(out, "\n")
	}
	return Insert(src, kind, rendered)
}

// Insert adds a new block. Mounts go to the end of the file. Accounts go
// before the mounts: after the last line of content above the first
// [[mount]], so comments that introduce the mounts stay with them.
func Insert(src, kind, rendered string) string {
	ls := lines(src)
	firstMount := -1
	for i, l := range ls {
		if kindOf(l) == Mount {
			firstMount = i
			break
		}
	}
	if kind == Mount || firstMount < 0 {
		src = strings.TrimRight(src, "\n")
		if src != "" {
			src += "\n\n"
		}
		return src + strings.TrimRight(rendered, "\n") + "\n"
	}
	at := firstMount
	for at > 0 && reComment.MatchString(ls[at-1]) {
		at--
	}
	newLines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	if at > 0 {
		newLines = append([]string{""}, newLines...)
	}
	out := append([]string{}, ls[:at]...)
	out = append(out, newLines...)
	if at < len(ls) && strings.TrimSpace(ls[at]) != "" {
		out = append(out, "")
	}
	out = append(out, ls[at:]...)
	return strings.Join(out, "\n")
}

// Remove deletes the block of kind with that name. Where the two remaining
// parts meet, one blank line stays; the rest of the file is untouched.
// Unknown names leave src unchanged.
func Remove(src, kind, name string) string {
	ls := lines(src)
	blank := func(l string) bool { return strings.TrimSpace(l) == "" }
	for _, b := range blocks(ls, kind) {
		if b.name != name {
			continue
		}
		out := append([]string{}, ls[:b.start]...)
		rest := ls[b.end:]
		for len(rest) > 0 && blank(rest[0]) && (len(out) == 0 || blank(out[len(out)-1])) {
			rest = rest[1:]
		}
		if !slices.ContainsFunc(rest, func(l string) bool { return !blank(l) }) {
			// the block was the last one: end the file right after what is left
			for len(out) > 0 && blank(out[len(out)-1]) {
				out = out[:len(out)-1]
			}
			if len(out) == 0 {
				return ""
			}
			return strings.Join(out, "\n") + "\n"
		}
		return strings.Join(append(out, rest...), "\n")
	}
	return src
}

// SetBlockKey sets key = value (value already rendered as TOML) among the
// top-level keys of the block of kind with that name, keeping a trailing
// comment. A missing key is added after the name line. Unknown names leave
// src unchanged.
func SetBlockKey(src, kind, name, key, value string) string {
	ls := lines(src)
	keyRe := keyLine(key)
	for _, b := range blocks(ls, kind) {
		if b.name != name {
			continue
		}
		nameLine := -1
		for i := b.start + 1; i < b.end; i++ {
			if reAnyHeader.MatchString(ls[i]) {
				break
			}
			if m := keyRe.FindStringSubmatch(ls[i]); m != nil {
				ls[i] = m[1] + value + trailingComment(m[2])
				return strings.Join(ls, "\n")
			}
			if nameLine < 0 && reName.MatchString(ls[i]) {
				nameLine = i
			}
		}
		out := append([]string{}, ls[:nameLine+1]...)
		out = append(out, key+" = "+value)
		out = append(out, ls[nameLine+1:]...)
		return strings.Join(out, "\n")
	}
	return src
}

// SetKey sets key = value (value already rendered as TOML) inside the
// top-level table (e.g. "general", "defaults", "defaults.vfs"), creating the
// table before the first [[account]] or [[mount]] when it is missing. An existing line keeps
// its trailing comment. value "" removes the key.
func SetKey(src, table, key, value string) string {
	ls := lines(src)
	hdr := regexp.MustCompile(`^\s*\[\s*` + regexp.QuoteMeta(table) + `\s*\]`)
	keyRe := keyLine(key)
	tStart := -1
	for i, l := range ls {
		if hdr.MatchString(l) {
			tStart = i
			break
		}
	}
	if tStart < 0 {
		if value == "" {
			return src
		}
		// create the table before the first [[account]] or [[mount]] (or at
		// the end)
		insertAt := len(ls)
		for i, l := range ls {
			if kindOf(l) != "" {
				insertAt = i
				break
			}
		}
		// place [defaults.vfs] after [defaults] if that exists
		if parent, _, ok := strings.Cut(table, "."); ok {
			pHdr := regexp.MustCompile(`^\s*\[\s*` + regexp.QuoteMeta(parent) + `\s*\]`)
			for i, l := range ls {
				if pHdr.MatchString(l) {
					j := tableEnd(ls, i)
					if j <= insertAt {
						insertAt = j
					}
					break
				}
			}
		}
		newLines := []string{"[" + table + "]", key + " = " + value, ""}
		if insertAt > 0 && strings.TrimSpace(ls[insertAt-1]) != "" {
			newLines = append([]string{""}, newLines...)
		}
		out := append([]string{}, ls[:insertAt]...)
		out = append(out, newLines...)
		out = append(out, ls[insertAt:]...)
		return strings.Join(out, "\n")
	}
	tEnd := tableEnd(ls, tStart)
	for i := tStart + 1; i < tEnd; i++ {
		if m := keyRe.FindStringSubmatch(ls[i]); m != nil {
			if value == "" {
				out := append([]string{}, ls[:i]...)
				out = append(out, ls[i+1:]...)
				return strings.Join(out, "\n")
			}
			ls[i] = m[1] + value + trailingComment(m[2])
			return strings.Join(ls, "\n")
		}
	}
	if value == "" {
		return src
	}
	// insert after the last non-blank line of the table
	at := tEnd
	for at > tStart+1 && strings.TrimSpace(ls[at-1]) == "" {
		at--
	}
	out := append([]string{}, ls[:at]...)
	out = append(out, key+" = "+value)
	out = append(out, ls[at:]...)
	return strings.Join(out, "\n")
}

// keyLine matches a key = value line: group 1 is everything up to the
// value, group 2 the value with any trailing comment.
func keyLine(key string) *regexp.Regexp {
	return regexp.MustCompile(`^(\s*"?` + regexp.QuoteMeta(key) + `"?\s*=\s*)(.*)$`)
}

// trailingComment returns the comment after a TOML value, blanks before it
// included; a # inside a quoted string belongs to the value.
func trailingComment(rest string) string {
	var quote byte
	for i := 0; i < len(rest); i++ {
		switch c := rest[i]; {
		case quote == '"' && c == '\\':
			i++ // skip the escaped character
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			return rest[len(strings.TrimRight(rest[:i], " \t")):]
		}
	}
	return ""
}

// tableEnd returns the index of the next header after the table at start.
func tableEnd(ls []string, start int) int {
	for i := start + 1; i < len(ls); i++ {
		if reAnyHeader.MatchString(ls[i]) {
			return i
		}
	}
	return len(ls)
}

// Quote renders a TOML basic string.
func Quote(s string) string { return strconv.Quote(s) }

// QuoteList renders a TOML array of basic strings.
func QuoteList(list []string) string {
	qs := make([]string, len(list))
	for i, s := range list {
		qs[i] = Quote(s)
	}
	return "[" + strings.Join(qs, ", ") + "]"
}

// RenderAccount renders one [[account]] block. Option values are written as
// strings unless they are plain integers or booleans, so that rclone's typed
// options read naturally.
func RenderAccount(a config.Account) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("[[account]]")
	w("name = %s", Quote(a.Name))
	if len(a.Public) > 0 {
		w("public = %s", QuoteList(a.Public))
	}
	if len(a.Options) > 0 {
		w("[account.options]")
		for _, k := range slices.Sorted(maps.Keys(a.Options)) {
			w("%s = %s", k, renderValue(a.Options[k]))
		}
	}
	if len(a.Secrets) > 0 {
		w("[account.secrets]")
		for _, k := range slices.Sorted(maps.Keys(a.Secrets)) {
			w("%s = %s", k, renderSecret(a.Secrets[k]))
		}
	}
	return b.String()
}

// RenderMount renders one [[mount]] block.
func RenderMount(m config.Mount) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("[[mount]]")
	w("name = %s", Quote(m.Name))
	w("account = %s", Quote(m.Account))
	if m.Type != "" {
		w("type = %s", Quote(m.Type))
	}
	w("path = %s", Quote(m.Path))
	if m.MountPoint != "" {
		w("mount_point = %s", Quote(m.MountPoint))
	}
	if m.ReadOnly != nil {
		w("read_only = %v", *m.ReadOnly)
	}
	if m.Enabled != nil {
		w("enabled = %v", *m.Enabled)
	}
	if m.HideAppleFiles != nil {
		w("hide_apple_files = %v", *m.HideAppleFiles)
	}
	if m.MountProbeInterval != nil {
		w("mount_probe_interval = %s", Quote(m.MountProbeInterval.Std().String()))
	}
	if m.BackendProbeInterval != nil {
		w("backend_probe_interval = %s", Quote(m.BackendProbeInterval.Std().String()))
	}
	if len(m.Options) > 0 {
		w("[mount.options]")
		for _, k := range slices.Sorted(maps.Keys(m.Options)) {
			w("%s = %s", k, renderValue(m.Options[k]))
		}
	}
	if len(m.VFS) > 0 {
		w("[mount.vfs]")
		for _, k := range slices.Sorted(maps.Keys(m.VFS)) {
			w("%s = %s", k, renderValue(m.VFS[k]))
		}
	}
	return b.String()
}

// renderSecret renders an inline secret source table.
func renderSecret(s config.SecretSource) string {
	switch s.Kind() {
	case "keychain":
		return "{ keychain = true }"
	case "command":
		qs := make([]string, len(s.Command))
		for i, c := range s.Command {
			qs[i] = Quote(c)
		}
		out := "{ command = [" + strings.Join(qs, ", ") + "]"
		if s.Timeout != nil {
			out += ", timeout = " + Quote(s.Timeout.Std().String())
		}
		return out + " }"
	}
	return "{ }"
}

func renderValue(v any) string {
	switch x := v.(type) {
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		if x == "true" || x == "false" {
			return x
		}
		// only canonical integers: TOML forbids leading zeros ("0777")
		if n, err := strconv.ParseInt(x, 10, 64); err == nil && strconv.FormatInt(n, 10) == x {
			return x
		}
		return Quote(x)
	default:
		return Quote(fmt.Sprint(v))
	}
}
