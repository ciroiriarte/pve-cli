package cli

import (
	"sort"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// newGuestTopCmd provides a unified view across VMs and containers.
func newGuestTopCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "guest", Short: "Unified view across VMs and containers"}
	var node, status string
	list := &cobra.Command{
		Use:   "list",
		Short: "List all guests (VMs + containers)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			guests, err := p.ListGuests(cmd.Context(), provider.GuestFilter{Node: node, Status: status})
			if err != nil {
				return err
			}
			sort.Slice(guests, func(i, j int) bool { return guests[i].VMID < guests[j].VMID })
			return a.render(guestsTable(guests))
		},
	}
	list.Flags().StringVar(&node, "node", "", "filter by node")
	list.Flags().StringVar(&status, "status", "", "filter by status")
	cmd.AddCommand(list)
	return cmd
}
