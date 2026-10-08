package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// EnvConfig is the environment variable that names the config file.
const EnvConfig = "LAGRA_CONFIG"

// Locate returns the config file path using the documented search order:
// explicit flag, $LAGRA_CONFIG, $XDG_CONFIG_HOME/lagra/config.toml,
// ~/.config/lagra/config.toml. The returned path may be a symlink.
func Locate(flagPath string) (string, error) {
	if flagPath != "" {
		return flagPath, nil
	}
	if p := os.Getenv(EnvConfig); p != "" {
		return p, nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "lagra", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate config: %w", err)
	}
	return filepath.Join(home, ".config", "lagra", "config.toml"), nil
}

// Resolve follows symlinks so that watchers and editors see the real file.
func Resolve(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("config file %s does not exist", path)
		}
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	return real, nil
}
