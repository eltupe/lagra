// Command lagra mounts remote storage as Finder volumes, driven by one TOML
// file. See README.md.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// version is set by the Makefile via -ldflags.
var version = "dev"

var configFlag string

func main() {
	// rclone registers its own global flags on pflag.CommandLine at init
	// time and cobra would merge them into our root command. Start clean.
	pflag.CommandLine = pflag.NewFlagSet(os.Args[0], pflag.ContinueOnError)
	root := &cobra.Command{
		Use:           "lagra",
		Short:         "Mount remote storage as Finder volumes, configured from your dotfiles",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&configFlag, "config", "", "config file (default: $LAGRA_CONFIG, $XDG_CONFIG_HOME/lagra/config.toml, ~/.config/lagra/config.toml)")
	root.AddCommand(
		validateCmd(), statusCmd(), mountCmd(), unmountCmd(), syncCmd(), reloadCmd(), quitCmd(),
		runCmd(), appCmd(), settingsCmd(), secretCmd(), configCmd(), backendsCmd(), optionsCmd(), versionCmd(),
	)
	// Started by LaunchServices from the .app bundle (no arguments, executable
	// inside Contents/MacOS): behave as the menu bar app.
	if len(os.Args) == 1 {
		if exe, err := os.Executable(); err == nil && strings.Contains(exe, ".app/Contents/MacOS/") {
			root.SetArgs([]string{"app"})
		}
	}
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "lagra:", err)
		os.Exit(1)
	}
}
