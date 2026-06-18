package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

const pluginPrefix = "pc-"

// pluginDir returns the user plugin directory (overridable for tests).
func pluginDir() string {
	if d := os.Getenv("PVE_CLI_PLUGIN_DIR"); d != "" {
		return d
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "pve-cli", "plugins")
}

// findPlugin locates an executable named pc-<name> in the plugin dir first,
// then on PATH. Returns "" if none is found.
func findPlugin(name string) string {
	bin := pluginPrefix + name
	if dir := pluginDir(); dir != "" {
		cand := filepath.Join(dir, bin)
		if isExecutable(cand) {
			return cand
		}
	}
	if p, err := exec.LookPath(bin); err == nil {
		return p
	}
	return ""
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode()&0o111 != 0
}

// listPlugins returns the sorted, de-duplicated names of discovered plugins
// (plugin dir + PATH), with the path each resolves to.
func listPlugins() map[string]string {
	out := map[string]string{}
	addFrom := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			n := e.Name()
			if !strings.HasPrefix(n, pluginPrefix) {
				continue
			}
			full := filepath.Join(dir, n)
			if !isExecutable(full) {
				continue
			}
			name := strings.TrimPrefix(n, pluginPrefix)
			if _, seen := out[name]; !seen { // plugin dir / earlier PATH wins
				out[name] = full
			}
		}
	}
	addFrom(pluginDir())
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		addFrom(dir)
	}
	return out
}

// dispatchPlugin runs a plugin if args target one that is not a built-in
// command. It returns handled=false when no plugin should run, so the caller
// falls back to normal cobra execution. Built-in commands always win.
func dispatchPlugin(root *cobra.Command, args []string) (handled bool, code int) {
	name := firstNonFlagArg(args)
	if name == "" || name == "help" {
		return false, 0
	}
	if isBuiltinCommand(root, name) {
		return false, 0
	}
	path := findPlugin(name)
	if path == "" {
		return false, 0
	}
	// Pass through every arg after the plugin name.
	return true, execPlugin(path, argsAfter(args, name))
}

// isBuiltinCommand reports whether root has an immediate subcommand by this name
// (or alias).
func isBuiltinCommand(root *cobra.Command, name string) bool {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return true
		}
		for _, a := range c.Aliases {
			if a == name {
				return true
			}
		}
	}
	return false
}

func firstNonFlagArg(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// argsAfter returns the args following the first occurrence of name.
func argsAfter(args []string, name string) []string {
	for i, a := range args {
		if a == name {
			return args[i+1:]
		}
	}
	return nil
}

// execPlugin runs the plugin with inherited stdio and the pve-cli environment,
// returning its exit code.
func execPlugin(path string, args []string) int {
	cmd := exec.Command(path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "Error: plugin failed:", err)
		return ExitGeneric
	}
	return ExitOK
}

func newPluginCmd(_ *app) *cobra.Command {
	cmd := &cobra.Command{Use: "plugin", Short: "Discover external pc-<name> plugins"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List discovered plugins (plugin dir + PATH)",
		Long:  "Executables named pc-<name> are exposed as `pc <name>`. Built-in commands take precedence.",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			plugins := listPlugins()
			if len(plugins) == 0 {
				fmt.Fprintf(os.Stdout, "no plugins found (looked in %s and $PATH)\n", pluginDir())
				return nil
			}
			names := make([]string, 0, len(plugins))
			for n := range plugins {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				fmt.Fprintf(os.Stdout, "%s\t%s\n", n, plugins[n])
			}
			return nil
		},
	})
	return cmd
}
