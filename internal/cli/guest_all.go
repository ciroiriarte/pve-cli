package cli

import (
	"sort"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// newGuestTopCmd provides a unified view and type-agnostic lifecycle across VMs
// and containers.
func newGuestTopCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "guest", Short: "Unified view and lifecycle across VMs and containers"}
	var node, status string
	var tags []string
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
			guests = filterGuestsByTags(guests, tags)
			sort.Slice(guests, func(i, j int) bool { return guests[i].VMID < guests[j].VMID })
			return a.render(guestsTable(guests))
		},
	}
	list.Flags().StringVar(&node, "node", "", "filter by node")
	list.Flags().StringVar(&status, "status", "", "filter by status")
	list.Flags().StringArrayVar(&tags, "tag", nil, "filter by tag (repeatable; matches any)")
	cmd.AddCommand(list)

	// Type-agnostic lifecycle/read verbs: `pc guest start 100` resolves whether
	// 100 is a VM or CT and routes accordingly, so admins don't have to know the
	// type up front (matching `openstack server …`/`kubectl` muscle memory).
	// Provisioning (create/clone/delete/config-write) stays on the typed vm/ct
	// trees, where the type is intrinsic.
	cmd.AddCommand(
		newGuestShowCmd(a, guestKind),
		newGuestStatusCmd(a, guestKind),
		newGuestPowerCmd(a, guestKind, "start", "Start", false),
		newGuestPowerCmd(a, guestKind, "stop", "Stop (hard)", true),
		newGuestPowerCmd(a, guestKind, "shutdown", "Shut down (graceful)", true),
		newGuestPowerCmd(a, guestKind, "reboot", "Reboot", true),
		newGuestPowerCmd(a, guestKind, "suspend", "Suspend (pause)", true),
		newGuestPowerCmd(a, guestKind, "resume", "Resume", false),
		newGuestTagCmd(a, guestKind),
		newGuestConsoleCmd(a, guestKind),
	)
	return cmd
}
