// Prints the license texts of Go and of every module linked into lagra, for
// THIRD_PARTY_NOTICES.txt in the app bundle (see the Makefile's app target).
// MIT, BSD and ISC require their notices to travel with binaries, Apache-2.0
// also its NOTICE files:
//
//	go run ./hack/notices > THIRD_PARTY_NOTICES.txt
//
// A module without a license file is an error, so a new dependency cannot
// reach a release unnoticed.
package main

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// The package whose dependencies end up in the binary.
const mainPkg = "./cmd/lagra"

// licenseFile matches LICENSE, LICENSE.md, LICENSE-APACHE, COPYING, NOTICE
// and their spellings; .go files with such names are code.
var licenseFile = regexp.MustCompile(`(?i)^(licen[cs]es?|copying|copyright|notices?|unlicense)([-._].*)?$`)

const header = `Third-party notices for Lagra

Lagra is free software under the GNU General Public License, version 3 or
later; see the file LICENSE next to this one. Its source code is at
https://github.com/eltupe/lagra.

The Lagra program contains Go and the Go modules listed below. Their license
texts follow in full.

`

type module struct {
	Path    string
	Version string
	Dir     string
	Main    bool
	Replace *module
}

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "notices:", err)
		os.Exit(1)
	}
}

func run(out io.Writer) error {
	mods, err := linkedModules(mainPkg)
	if err != nil {
		return err
	}
	env, err := goEnv("GOROOT", "GOVERSION")
	if err != nil {
		return err
	}
	// Homebrew's go keeps LICENSE in the keg, one level above GOROOT.
	goDir := env[0]
	if _, err := os.Stat(filepath.Join(goDir, "LICENSE")); err != nil {
		goDir = filepath.Dir(goDir)
	}
	all := append([]module{{Path: "Go", Version: env[1], Dir: goDir}}, mods...)

	type entry struct {
		mod   module
		files []string
	}
	var entries []entry
	var missing []string
	for _, m := range all {
		files, err := licenseFiles(m.Dir)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			missing = append(missing, m.Path+" "+m.Version)
			continue
		}
		entries = append(entries, entry{m, files})
	}
	if len(missing) > 0 {
		return fmt.Errorf("no license file in:\n  %s", strings.Join(missing, "\n  "))
	}

	w := bufio.NewWriter(out)
	w.WriteString(header)
	for _, e := range entries {
		fmt.Fprintf(w, "  %s %s\n", e.mod.Path, e.mod.Version)
	}
	for _, e := range entries {
		fmt.Fprintf(w, "\n%s\n%s %s\n", strings.Repeat("=", 80), e.mod.Path, e.mod.Version)
		for _, f := range e.files {
			text, err := os.ReadFile(filepath.Join(e.mod.Dir, f))
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "\n--- %s ---\n\n%s\n", f, bytes.TrimSpace(toUTF8(text)))
		}
	}
	return w.Flush()
}

// linkedModules lists the modules whose packages pkg imports, directly or
// not, sorted by path. Replaced modules report their replacement.
func linkedModules(pkg string) ([]module, error) {
	cmd := exec.Command("go", "list", "-deps", "-json=Module", pkg)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	seen := map[string]module{}
	dec := jsontext.NewDecoder(bytes.NewReader(out))
	for {
		var p struct{ Module *module }
		if err := json.UnmarshalDecode(dec, &p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		m := p.Module
		if m == nil || m.Main { // standard library, or lagra itself
			continue
		}
		if m.Replace != nil {
			m = m.Replace
		}
		if m.Dir == "" {
			return nil, fmt.Errorf("%s %s is not downloaded; run go mod download", m.Path, m.Version)
		}
		seen[m.Path] = *m
	}
	mods := make([]module, 0, len(seen))
	for _, m := range seen {
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mods, nil
}

// licenseFiles returns the license and notice files at the root of dir.
func licenseFiles(dir string) ([]string, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, de := range des {
		name := de.Name()
		if de.Type().IsRegular() && licenseFile.MatchString(name) && filepath.Ext(name) != ".go" {
			files = append(files, name)
		}
	}
	return files, nil
}

// cp1252 holds the characters of Windows-1252 at 0x80–0x9f; above that it
// equals Latin-1.
var cp1252 = []rune("€�‚ƒ„…†‡ˆ‰Š‹Œ�Ž��‘’“”•–—˜™š›œ�žŸ")

// toUTF8 converts text that is not UTF-8 from Windows-1252, the encoding of
// the few license files that are not (go-nfs-client's, for one), so the
// notices file stays valid UTF-8.
func toUTF8(text []byte) []byte {
	if utf8.Valid(text) {
		return text
	}
	var b strings.Builder
	for _, c := range text {
		if c >= 0x80 && c < 0xa0 {
			b.WriteRune(cp1252[c-0x80])
		} else {
			b.WriteRune(rune(c))
		}
	}
	return []byte(b.String())
}

// goEnv returns the values of the given go env variables, in order.
func goEnv(vars ...string) ([]string, error) {
	out, err := exec.Command("go", append([]string{"env"}, vars...)...).Output()
	if err != nil {
		return nil, fmt.Errorf("go env: %w", err)
	}
	vals := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(vals) != len(vars) {
		return nil, fmt.Errorf("go env: got %d values for %d variables", len(vals), len(vars))
	}
	return vals, nil
}
