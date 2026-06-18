package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/version"
)

func newVersionCmd(_ *app) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show version and supported Proxmox releases",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			w := os.Stdout
			fmt.Fprintln(w, version.String())
			fmt.Fprintf(w, "supported: %s; %s\n", version.SupportedPVE, version.SupportedPDM)
			fmt.Fprintln(w, version.Disclaimer)
			return nil
		},
	}
}
