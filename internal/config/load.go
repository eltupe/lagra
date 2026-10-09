package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Document is a parsed but not yet validated config file together with what
// is needed to point at lines in error messages.
type Document struct {
	Path string // path as given (may be a symlink)
	File File
	// undecoded lists keys the file contains but File has no field for.
	undecoded []string
	lines     *locator
}

// ReadFile reads and parses the file at path. Parse errors carry line numbers.
func ReadFile(path string) (*Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(path, raw)
}

// Parse parses raw TOML. It fails only on syntax and type errors; semantic
// problems are left to Validate so that all of them can be reported at once.
func Parse(path string, raw []byte) (*Document, error) {
	d := &Document{Path: path, lines: newLocator(raw)}
	md, err := toml.Decode(string(raw), &d.File)
	if err != nil {
		if pe, ok := errors.AsType[toml.ParseError](err); ok {
			return nil, &Issue{Path: pe.LastKey, Line: pe.Position.Line, Msg: pe.Message}
		}
		return nil, fmt.Errorf("parse config: %w", err)
	}
	for _, k := range md.Undecoded() {
		d.undecoded = append(d.undecoded, k.String())
	}
	return d, nil
}

// Stringify turns a scalar TOML value into the string rclone expects.
func Stringify(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case bool:
		return strconv.FormatBool(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("must be a string, number or boolean, not %T", v)
	}
}

// locator finds line numbers for keys, best effort. TOML libraries do not
// expose positions for successfully decoded keys, so we scan the text.
type locator struct {
	lines         []string
	headers       []int // 0-based line index of every table header, in order
	accountStarts []int // 0-based line index of each [[account]] header
	mountStarts   []int // 0-based line index of each [[mount]] header
}

var (
	reHeader        = regexp.MustCompile(`^\s*\[`)
	reAccountHeader = regexp.MustCompile(`^\s*\[\[\s*account\s*\]\]`)
	reMountHeader   = regexp.MustCompile(`^\s*\[\[\s*mount\s*\]\]`)
)

func newLocator(raw []byte) *locator {
	l := &locator{lines: strings.Split(string(raw), "\n")}
	for i, line := range l.lines {
		if !reHeader.MatchString(line) {
			continue
		}
		l.headers = append(l.headers, i)
		switch {
		case reAccountHeader.MatchString(line):
			l.accountStarts = append(l.accountStarts, i)
		case reMountHeader.MatchString(line):
			l.mountStarts = append(l.mountStarts, i)
		}
	}
	return l
}

// mountLine returns the 1-based line of the i-th [[mount]] header, or 0.
func (l *locator) mountLine(i int) int { return l.startLine(l.mountStarts, i) }

// accountLine returns the 1-based line of the i-th [[account]] header, or 0.
func (l *locator) accountLine(i int) int { return l.startLine(l.accountStarts, i) }

func (l *locator) startLine(starts []int, i int) int {
	if l == nil || i < 0 || i >= len(starts) {
		return 0
	}
	return starts[i] + 1
}

// span returns the line range [start, end) of the block that begins at
// header line h: up to the next header that is not one of its own
// sub-tables ([account.options] belongs to the [[account]] above it).
func (l *locator) span(h int, kind string) (int, int) {
	own := regexp.MustCompile(`^\s*\[\s*` + kind + `\.`)
	for _, n := range l.headers {
		if n > h && !own.MatchString(l.lines[n]) {
			return h + 1, n
		}
	}
	return h + 1, len(l.lines)
}

// blockKeyLine returns the 1-based line of `key =` inside the i-th block of
// kind ("account" or "mount"), or 0.
func (l *locator) blockKeyLine(kind string, i int, key string) int {
	if l == nil {
		return 0
	}
	starts := l.mountStarts
	if kind == "account" {
		starts = l.accountStarts
	}
	if i < 0 || i >= len(starts) {
		return 0
	}
	start, end := l.span(starts[i], kind)
	return l.find(start, end, key)
}

// topKeyLine searches the part of the file before the first [[account]] or
// [[mount]]. Qualify key with its table ("general.cache_dir"): [general]
// and [defaults] share some key names.
func (l *locator) topKeyLine(key string) int {
	if l == nil {
		return 0
	}
	end := len(l.lines)
	for _, starts := range [][]int{l.accountStarts, l.mountStarts} {
		if len(starts) > 0 && starts[0] < end {
			end = starts[0]
		}
	}
	return l.find(0, end, key)
}

// anyKeyLine searches the whole file.
func (l *locator) anyKeyLine(key string) int {
	if l == nil {
		return 0
	}
	return l.find(0, len(l.lines), key)
}

// find locates key within lines [start, end). Sub-table keys such as
// options.region are located by their last segment, which also finds inline
// tables' parent key when the leaf is absent.
func (l *locator) find(start, end int, key string) int {
	segs := strings.Split(key, ".")
	leaf := segs[len(segs)-1]
	re := regexp.MustCompile(`^\s*"?` + regexp.QuoteMeta(leaf) + `"?\s*=`)
	// A qualified key (options.region, secrets.pass) prefers its own sub-table
	// header, e.g. [account.options], when the block has one.
	if len(segs) > 1 {
		hdr := regexp.MustCompile(`^\s*\[\s*(mount\.|account\.|defaults\.|general\.)?` + regexp.QuoteMeta(segs[0]) + `\s*\]`)
		for n := start; n < end; n++ {
			if hdr.MatchString(l.lines[n]) {
				for k := n + 1; k < end; k++ {
					if strings.HasPrefix(strings.TrimSpace(l.lines[k]), "[") {
						break
					}
					if re.MatchString(l.lines[k]) {
						return k + 1
					}
				}
				break
			}
		}
	}
	for _, seg := range slices.Backward(segs) {
		re := regexp.MustCompile(`^\s*"?` + regexp.QuoteMeta(seg) + `"?\s*=`)
		inline := regexp.MustCompile(`[{,]\s*"?` + regexp.QuoteMeta(seg) + `"?\s*=`)
		for n := start; n < end; n++ {
			if re.MatchString(l.lines[n]) || inline.MatchString(l.lines[n]) {
				return n + 1
			}
		}
	}
	return 0
}

// headerLine returns the 1-based line of the first table header named name
// (e.g. "mount.options"), or 0.
func (l *locator) headerLine(name string) int {
	if l == nil {
		return 0
	}
	re := regexp.MustCompile(`^\s*\[\s*` + regexp.QuoteMeta(name) + `\s*\]`)
	for _, n := range l.headers {
		if re.MatchString(l.lines[n]) {
			return n + 1
		}
	}
	return 0
}
