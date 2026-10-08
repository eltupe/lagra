package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandPath expands a leading "~" and every "${VAR}" in a local path so the
// same file works on several Macs. Unknown variables are an error, not "".
func ExpandPath(p string, lookupEnv func(string) (string, bool), home string) (string, error) {
	var missing []string
	out := envRef.ReplaceAllStringFunc(p, func(ref string) string {
		name := ref[2 : len(ref)-1]
		v, ok := lookupEnv(name)
		if !ok {
			missing = append(missing, name)
			return ref
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("undefined environment variable %s", strings.Join(missing, ", "))
	}
	if out == "~" || strings.HasPrefix(out, "~/") {
		out = filepath.Join(home, strings.TrimPrefix(out, "~"))
	}
	return filepath.Clean(out), nil
}
