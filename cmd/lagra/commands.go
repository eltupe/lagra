package main

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/eltupe/lagra/internal/app"
	"github.com/eltupe/lagra/internal/config"
	"github.com/eltupe/lagra/internal/engine"
	"github.com/eltupe/lagra/internal/ipc"
	"github.com/eltupe/lagra/internal/rclone"
	"github.com/eltupe/lagra/internal/secrets"
)

func loadConfig() (*config.Document, *config.Effective, config.Issues, error) {
	path, err := config.Locate(configFlag)
	if err != nil {
		return nil, nil, nil, err
	}
	doc, err := config.ReadFile(path)
	if err != nil {
		return nil, nil, nil, err
	}
	env, err := config.OSEnv()
	if err != nil {
		return nil, nil, nil, err
	}
	eff, issues := config.Validate(doc, rclone.Registry{}, env)
	return doc, eff, issues, nil
}

func client() (*ipc.Client, error) {
	sock, err := ipc.DefaultSocket()
	if err != nil {
		return nil, err
	}
	return ipc.NewClient(sock), nil
}

func validateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check the config file; exit 1 if invalid (usable as a git hook)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, _ := config.Locate(configFlag)
			doc, eff, issues, err := loadConfig()
			if err != nil {
				if is, ok := errors.AsType[*config.Issue](err); ok {
					fmt.Fprintf(os.Stderr, "%s: %s\n", path, is.Error())
					os.Exit(1)
				}
				return err
			}
			for _, is := range issues {
				kind := "error"
				if is.Warning {
					kind = "warning"
				}
				fmt.Fprintf(os.Stderr, "%s: %s: %s\n", doc.Path, kind, is.Error())
			}
			if issues.HasErrors() {
				os.Exit(1)
			}
			n := 0
			for _, m := range eff.Mounts {
				if m.Enabled {
					n++
				}
			}
			fmt.Printf("%s: OK (%d mounts, %d enabled)\n", doc.Path, len(eff.Mounts), n)
			return nil
		},
	}
}

func statusCmd() *cobra.Command {
	var verbose, asJSON bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Show the state of every mount",
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			st, err := cl.Status(ctx)
			if errors.Is(err, ipc.ErrNoDaemon) {
				fmt.Println("lagra is not running (start it with `lagra run` or open the app)")
				if verbose {
					return printEffective()
				}
				return nil
			}
			if err != nil {
				return err
			}
			if asJSON {
				if err := json.MarshalWrite(os.Stdout, st, ipc.JSONOut, jsontext.WithIndent("  ")); err != nil {
					return err
				}
				fmt.Println()
				return nil
			}
			fmt.Printf("lagra %s (pid %d), up %s\n", st.Version, st.PID, time.Since(st.StartedAt).Round(time.Second))
			fmt.Printf("config: %s", st.ConfigPath)
			if st.ConfigReal != "" && st.ConfigReal != st.ConfigPath {
				fmt.Printf(" -> %s", st.ConfigReal)
			}
			if st.ConfigOK {
				fmt.Printf(" (loaded %s ago)\n", time.Since(st.LoadedAt).Round(time.Second))
			} else {
				fmt.Printf("\nCONFIG ERROR: %s\n", st.ConfigError)
			}
			fmt.Printf("overall: %s\n\n", st.Overall)
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "MOUNT\tSTATE\tSINCE\tUPLOADS\tMOUNT POINT\tNOTE")
			for _, m := range st.Mounts {
				note := ""
				switch {
				case m.Pending != nil:
					note = fmt.Sprintf("pending %s (%s)", m.Pending.Action, m.Pending.Reason)
				case m.State == engine.Broken:
					note = fmt.Sprintf("retry #%d at %s: %s", m.Attempt, m.NextRetry.Format("15:04:05"), m.LastError)
				case m.LastError != "":
					note = m.LastError
				case m.Manual:
					note = "disconnected by you"
				case !m.Enabled:
					note = "disabled in config"
				}
				if m.Stuck {
					note = "uploads stuck; " + note
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n", m.Name, m.State, time.Since(m.Since).Round(time.Second), m.Uploads, m.MountPoint, note)
			}
			tw.Flush()
			if verbose {
				fmt.Println()
				return printEffective()
			}
			return nil
		},
	}
	c.Flags().BoolVarP(&verbose, "verbose", "v", false, "also print the effective configuration (without secrets)")
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return c
}

// printEffective prints the merged config the way the engine sees it.
func printEffective() error {
	_, eff, issues, err := loadConfig()
	if err != nil {
		return err
	}
	if issues.HasErrors() {
		fmt.Println("config has errors:")
		fmt.Println(issues.Error())
		return nil
	}
	fmt.Println("effective configuration:")
	fmt.Printf("  general: log_level=%s cache_dir=%s mount_probe=%s backend_probe=%s upload_stuck_after=%s launch_at_login=%v\n",
		eff.General.LogLevel, eff.General.CacheDir, eff.General.MountProbeInterval, eff.General.BackendProbeInterval, eff.General.UploadStuckAfter, eff.General.LaunchAtLogin)
	for _, a := range eff.Accounts {
		fmt.Printf("  account %s:\n", a.Name)
		fmt.Printf("    options: %s\n", kv(a.Options))
		var secs []string
		for _, k := range slices.Sorted(maps.Keys(a.Secrets)) {
			secs = append(secs, k+"="+a.Secrets[k].Kind())
		}
		fmt.Printf("    secrets: %s\n", strings.Join(secs, " "))
	}
	for _, m := range eff.Mounts {
		fmt.Printf("  mount %s: account=%s type=%s path=%q mount_point=%s enabled=%v read_only=%v probes=%s/%s\n",
			m.Name, m.Account, m.Type, m.Path, m.MountPoint, m.Enabled, m.ReadOnly, m.MountProbeInterval, m.BackendProbeInterval)
		fmt.Printf("    options: %s\n", kv(m.Options))
		fmt.Printf("    vfs:     %s\n", kv(m.VFS))
	}
	return nil
}

func kv(m map[string]string) string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(m)) {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, " ")
}

func mountCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mount <name>",
		Short: "Connect a mount (through the running daemon, or in the foreground if none runs)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = cl.Connect(ctx, args[0])
			if errors.Is(err, ipc.ErrNoDaemon) {
				fmt.Fprintf(os.Stderr, "no daemon running; mounting %q in the foreground (Ctrl-C to unmount)\n", args[0])
				a, err := app.New(app.Options{ConfigFlag: configFlag, Version: version, Only: args[0], LogToStderr: true})
				if err != nil {
					return err
				}
				return a.Run(context.Background())
			}
			if err != nil {
				return err
			}
			return waitForState(cl, args[0], engine.Connected, engine.Offline)
		},
	}
}

func unmountCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "unmount <name>",
		Short: "Disconnect a mount (stays disconnected until `lagra mount` or a config change)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := cl.Disconnect(ctx, args[0], force); err != nil {
				return err
			}
			return waitForState(cl, args[0], engine.Disconnected)
		},
	}
	c.Flags().BoolVar(&force, "force", false, "ignore pending uploads and busy volumes")
	return c
}

// waitForState polls the daemon for up to 90 seconds and prints the outcome.
func waitForState(cl *ipc.Client, name string, want ...engine.State) error {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		st, err := cl.Status(ctx)
		cancel()
		if err != nil {
			return err
		}
		for _, m := range st.Mounts {
			if m.Name != name {
				continue
			}
			if slices.Contains(want, m.State) {
				fmt.Printf("%s: %s (%s)\n", name, m.State, m.MountPoint)
				return nil
			}
			if m.Pending != nil {
				fmt.Printf("%s: %s, change pending (%s)\n", name, m.State, m.Pending.Reason)
				return nil
			}
			if m.State == engine.Failed || m.State == engine.Broken {
				return fmt.Errorf("%s: %s: %s", name, m.State, m.LastError)
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("%s: timed out waiting", name)
}

func syncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync [name]",
		Short: "Push pending uploads now and refresh the listing (all mounts if no name)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			names := args
			if len(names) == 0 {
				st, err := cl.Status(ctx)
				if err != nil {
					return err
				}
				for _, m := range st.Mounts {
					if m.State == engine.Connected || m.State == engine.Offline {
						names = append(names, m.Name)
					}
				}
			}
			for _, n := range names {
				r, err := cl.Sync(ctx, n)
				if err != nil {
					fmt.Printf("%s: %v\n", n, err)
					continue
				}
				fmt.Printf("%s: %d upload(s) pushed, listing refreshed\n", n, r.Expedited)
			}
			return nil
		},
	}
}

func reloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Ask the running daemon to reload the config now",
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			r, err := cl.Reload(ctx)
			if err != nil {
				return err
			}
			for _, w := range r.Warnings {
				fmt.Fprintln(os.Stderr, "warning:", w)
			}
			if !r.OK {
				for _, is := range r.Issues {
					fmt.Fprintln(os.Stderr, "error:", is)
				}
				return errors.New("config rejected; the previous configuration stays active")
			}
			fmt.Printf("reloaded: %d added, %d removed, %d restarted\n", len(r.Added), len(r.Removed), len(r.Restart))
			return nil
		},
	}
}

func quitCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "quit",
		Short: "Stop the running daemon, unmounting everything",
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = cl.Shutdown(ctx, force)
			if errors.Is(err, ipc.ErrPendingUploads) {
				return fmt.Errorf("%v; use --force to quit anyway (uploads resume next start)", err)
			}
			return err
		},
	}
	c.Flags().BoolVar(&force, "force", false, "quit even with pending uploads")
	return c
}

func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the daemon in the foreground without a menu bar icon",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New(app.Options{ConfigFlag: configFlag, Version: version, LogToStderr: term.IsTerminal(int(os.Stderr.Fd()))})
			if err != nil {
				return err
			}
			return a.Run(context.Background())
		},
	}
}

func secretCmd() *cobra.Command {
	c := &cobra.Command{Use: "secret", Short: "Manage the keychain items of accounts' sensitive options"}
	kc := secrets.SecurityKeychain{}
	c.AddCommand(&cobra.Command{
		Use:   "set <account> <option>",
		Short: "Store a secret in the login keychain (read from the terminal, or from stdin when piped)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			value, err := readSecret(fmt.Sprintf("Secret for %s/%s: ", args[0], args[1]))
			if err != nil {
				return err
			}
			if value == "" {
				return errors.New("empty secret")
			}
			ctx, cancel := context.WithTimeout(context.Background(), secrets.Timeout)
			defer cancel()
			if err := kc.Set(ctx, secrets.Service, secrets.Account(args[0], args[1]), value); err != nil {
				return err
			}
			fmt.Printf("stored keychain item service=%s account=%s\n", secrets.Service, secrets.Account(args[0], args[1]))
			restartAccount(args[0])
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "rm <account> <option>",
		Short: "Delete a secret from the keychain",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), secrets.Timeout)
			defer cancel()
			return kc.Delete(ctx, secrets.Service, secrets.Account(args[0], args[1]))
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "check [account]",
		Short: "Verify that every secret source of the config can be resolved (values are never printed)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, eff, issues, err := loadConfig()
			if err != nil {
				return err
			}
			if issues.HasErrors() {
				return errors.New(issues.Error())
			}
			r := &secrets.Resolver{Keychain: kc}
			failed := 0
			for _, a := range eff.Accounts {
				if len(args) == 1 && a.Name != args[0] {
					continue
				}
				for _, opt := range slices.Sorted(maps.Keys(a.Secrets)) {
					src := a.Secrets[opt]
					ctx, cancel := context.WithTimeout(context.Background(), secrets.Timeout)
					_, err := r.Resolve(ctx, a.Name, map[string]config.SecretSource{opt: src})
					cancel()
					if err != nil {
						failed++
						fmt.Printf("%s/%s (%s): %v\n", a.Name, opt, src.Kind(), err)
					} else {
						fmt.Printf("%s/%s (%s): ok\n", a.Name, opt, src.Kind())
					}
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d secret(s) unavailable", failed)
			}
			return nil
		},
	})
	return c
}

// restartAccount asks a running daemon to reconnect the mounts that use
// account, so a new secret takes effect. Without a daemon it does nothing.
func restartAccount(account string) {
	cl, err := client()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	names, _ := cl.RestartAccount(ctx, account)
	for _, n := range names {
		fmt.Printf("%s: reconnecting with the new secret\n", n)
	}
}

func readSecret(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return strings.TrimRight(string(b), "\r\n"), err
	}
	b, err := io.ReadAll(os.Stdin)
	return strings.TrimRight(string(b), "\r\n"), err
}

func configCmd() *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Where the config file is and what an example looks like"}
	c.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the config path and, if it is a symlink, its target",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := config.Locate(configFlag)
			if err != nil {
				return err
			}
			fmt.Println(p)
			if real, err := config.Resolve(p); err == nil && real != p {
				fmt.Println("->", real)
			} else if err != nil {
				fmt.Println("(missing)")
			}
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "example",
		Short: "Print the fully commented example config",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := os.Stdout.Write(config.Example)
			return err
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "edit",
		Short: "Open the resolved config file in $EDITOR",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := config.Locate(configFlag)
			if err != nil {
				return err
			}
			if real, err := config.Resolve(p); err == nil {
				p = real
			}
			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "vi"
			}
			parts := strings.Fields(editor)
			ed := exec.Command(parts[0], append(parts[1:], p)...)
			ed.Stdin, ed.Stdout, ed.Stderr = os.Stdin, os.Stdout, os.Stderr
			return ed.Run()
		},
	})
	return c
}

func backendsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "backends",
		Short: "List the backend types accepted in `type = ...`",
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, b := range rclone.Backends() {
				fmt.Println(b.Name)
			}
			return nil
		},
	}
}

func optionsCmd() *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:   "options <type|vfs>",
		Short: "List the options of a backend (or of the VFS layer) with their sensitivity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg := rclone.Registry{}
			var set config.OptionSet
			if args[0] == "vfs" {
				set = reg.VFS()
			} else {
				s, ok := reg.Backend(args[0])
				if !ok {
					return fmt.Errorf("unsupported backend %q (see `lagra backends`)", args[0])
				}
				set = s
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "OPTION\tTYPE\tDEFAULT\tFLAGS")
			for _, o := range set.Options() {
				if o.Advanced && !all {
					continue
				}
				var flags []string
				if o.Required {
					flags = append(flags, "required")
				}
				if o.Sensitive {
					flags = append(flags, "SENSITIVE -> [account.secrets]")
				}
				if o.Provider != "" {
					flags = append(flags, "provider="+o.Provider)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", o.Name, o.Type, o.Default, strings.Join(flags, ", "))
			}
			return tw.Flush()
		},
	}
	c.Flags().BoolVar(&all, "all", false, "include advanced options")
	return c
}

func settingsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "settings",
		Short: "Open the settings page of the running daemon in your browser",
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			st, err := cl.Status(ctx)
			if err != nil {
				return err
			}
			if st.SettingsURL == "" {
				return errors.New("the running daemon has no settings server")
			}
			return exec.Command("/usr/bin/open", st.SettingsURL).Run()
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("lagra", version)
		},
	}
}
