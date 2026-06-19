// Package cli builds the cobra command tree — the curated, stable UX surface.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/config"
	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/provider"
	"github.com/ciroiriarte/pve-cli/internal/version"

	// Register backend providers.
	_ "github.com/ciroiriarte/pve-cli/internal/provider/pdm"
	_ "github.com/ciroiriarte/pve-cli/internal/provider/pve"
)

// app is the shared runtime carried across commands via cobra's context.
type app struct {
	// global flags
	profile     string
	context     string
	server      string
	provider    string
	tokenID     string
	secret      string
	format      string
	columns     []string
	noHeaders   bool
	sortBy      string
	insecure    bool
	fingerprint string
	debug       bool
	assumeYes   bool

	settings *config.Settings
	prov     provider.Provider
}

// Provider lazily builds and caches the backend from resolved settings.
func (a *app) Provider() (provider.Provider, error) {
	if a.prov != nil {
		return a.prov, nil
	}
	f, err := config.Load(config.DefaultPath())
	if err != nil {
		return nil, err
	}
	s, err := config.Resolve(f, a.overrides())
	if err != nil {
		return nil, err
	}
	a.settings = s
	if a.format == "" {
		a.format = s.Output
	}
	p, err := provider.New(s, a.debug)
	if err != nil {
		return nil, err
	}
	a.prov = p
	return p, nil
}

// overrides builds the config Overrides from the parsed global flags.
func (a *app) overrides() config.Overrides {
	ov := config.Overrides{
		Profile:     a.profile,
		Context:     a.context,
		Server:      a.server,
		Provider:    a.provider,
		TokenID:     a.tokenID,
		Secret:      a.secret,
		Output:      a.format,
		Fingerprint: a.fingerprint,
	}
	if a.insecure {
		ov.Insecure = &a.insecure
	}
	return ov
}

// providerName resolves the configured backend ("pve"|"pdm") without building a
// client or requiring credentials — used to pick the schema for `pc raw`.
func (a *app) providerName() string {
	f, err := config.Load(config.DefaultPath())
	if err != nil {
		return "pve"
	}
	s, err := config.Resolve(f, a.overrides())
	if err != nil || s.Provider == "" {
		return "pve"
	}
	return s.Provider
}

// outputOptions builds render options from the resolved global flags.
func (a *app) outputOptions() (output.Options, error) {
	fmtStr := a.format
	if fmtStr == "" {
		fmtStr = "table"
	}
	f, err := output.ParseFormat(fmtStr)
	if err != nil {
		return output.Options{}, err
	}
	return output.Options{
		Format:    f,
		Columns:   a.columns,
		NoHeaders: a.noHeaders,
		SortBy:    a.sortBy,
	}, nil
}

// NewRootCmd assembles the full command tree.
func NewRootCmd() *cobra.Command {
	a := &app{}

	root := &cobra.Command{
		Use:   "pc",
		Short: "Remote CLI for Proxmox VE clusters (and Proxmox Datacenter Manager)",
		Long: "pc is a remote-first, OpenStack-Client-inspired CLI for managing Proxmox VE\n" +
			"clusters entirely over their REST API.\n\n" + version.Disclaimer,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Validate global flags early so bad input fails before any network I/O.
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			if a.format != "" {
				if _, err := output.ParseFormat(a.format); err != nil {
					return err
				}
			}
			return nil
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&a.profile, "profile", "", "config profile to use")
	pf.StringVar(&a.context, "context", "", "config context to use")
	pf.StringVar(&a.server, "server", "", "Proxmox API base URL (e.g. https://host:8006)")
	pf.StringVar(&a.provider, "provider", "", "backend provider: pve|pdm (overrides profile)")
	pf.StringVar(&a.tokenID, "token-id", "", "API token id (user@realm!name)")
	pf.StringVar(&a.secret, "token-secret", "", "API token secret")
	pf.StringVarP(&a.format, "format", "o", "", "output format: table|json|yaml|csv|value")
	pf.StringArrayVarP(&a.columns, "column", "c", nil, "select/order output columns (repeatable)")
	pf.BoolVar(&a.noHeaders, "no-headers", false, "omit table/csv headers")
	pf.StringVar(&a.sortBy, "sort", "", "sort by column (NAME[:asc|desc])")
	pf.BoolVar(&a.insecure, "insecure", false, "skip TLS verification (footgun)")
	pf.StringVar(&a.fingerprint, "tls-fingerprint", "", "pin the server cert SHA-256 fingerprint")
	pf.BoolVar(&a.debug, "debug", false, "log request/response metadata to stderr")
	pf.BoolVarP(&a.assumeYes, "yes", "y", false, "assume yes for destructive confirmations")

	root.AddCommand(
		newNodeCmd(a),
		newGuestCmd(a, vmKind),
		newGuestCmd(a, ctKind),
		newGuestTopCmd(a),
		newStorageCmd(a),
		newBackupCmd(a),
		newPoolCmd(a),
		newHACmd(a),
		newFirewallCmd(a),
		newRemoteCmd(a),
		newCephCmd(a),
		newResourcesCmd(a),
		newSDNCmd(a),
		newAccessCmd(a),
		newPBSCmd(a),
		newSubscriptionCmd(a),
		newServerCmd(a),
		newAutoInstallCmd(a),
		newPluginCmd(a),
		newTaskCmd(a),
		newRawCmd(a),
		newAPICmd(a),
		newConfigCmd(a),
		newContextCmd(a),
		newAuthCmd(a),
		newVersionCmd(a),
	)
	return root
}

// Execute runs the root command and maps errors to exit codes. Before handing
// off to cobra, it checks whether the invocation targets an external plugin
// (pc-<name>) and, if so, dispatches to it. Built-in commands take precedence.
func Execute() int {
	root := NewRootCmd()
	if handled, code := dispatchPlugin(root, os.Args[1:]); handled {
		return code
	}
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return ExitCodeFor(err)
	}
	return 0
}
