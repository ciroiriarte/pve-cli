package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// newFirewallCmd manages the Proxmox firewall at three scopes, selected by
// flags: cluster (default), node (--node), or guest (--vmid, optionally --ct).
func newFirewallCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "firewall", Aliases: []string{"fw"}, Short: "Manage the Proxmox firewall (cluster/node/guest)"}
	var node, vmid string
	var isCT bool
	cmd.PersistentFlags().StringVar(&node, "node", "", "scope to a node (or the guest's node)")
	cmd.PersistentFlags().StringVar(&vmid, "vmid", "", "scope to a guest")
	cmd.PersistentFlags().BoolVar(&isCT, "ct", false, "the --vmid is a container (default: VM)")

	// fwBase resolves the scope's firewall base path.
	fwBase := func(cmd *cobra.Command) (provider.Provider, string, error) {
		p, err := a.Provider()
		if err != nil {
			return nil, "", err
		}
		if vmid != "" {
			id, err := strconv.Atoi(vmid)
			if err != nil {
				return nil, "", fmt.Errorf("invalid --vmid %q", vmid)
			}
			n := node
			kind := "qemu"
			if isCT {
				kind = "lxc"
			}
			if n == "" { // resolve the guest's node
				g, err := p.ResolveGuest(cmd.Context(), id)
				if err != nil {
					return nil, "", err
				}
				n = g.Node
				kind = string(g.Kind)
			}
			return p, fmt.Sprintf("/nodes/%s/%s/%d/firewall", n, kind, id), nil
		}
		if node != "" {
			return p, "/nodes/" + node + "/firewall", nil
		}
		return p, "/cluster/firewall", nil
	}
	get := func(suffix string, cols ...string) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			p, b, err := fwBase(cmd)
			if err != nil {
				return err
			}
			return a.renderGet(cmd, p, b+suffix, cols...)
		}
	}

	cmd.AddCommand(
		&cobra.Command{Use: "rules", Short: "List firewall rules", Args: cobra.NoArgs,
			RunE: get("/rules", "pos", "type", "action", "source", "dest", "proto", "dport", "enable")},
		&cobra.Command{Use: "aliases", Short: "List firewall aliases", Args: cobra.NoArgs,
			RunE: get("/aliases", "name", "cidr", "comment")},
		&cobra.Command{Use: "ipset", Short: "List IP sets", Args: cobra.NoArgs,
			RunE: get("/ipset", "name", "comment")},
		&cobra.Command{Use: "options", Short: "Show firewall options", Args: cobra.NoArgs,
			RunE: get("/options")},
		newFirewallRuleCmd(a, fwBase),
	)
	// cluster-only helpers
	cmd.AddCommand(
		&cobra.Command{Use: "groups", Short: "List security groups (cluster scope)", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				p, err := a.Provider()
				if err != nil {
					return err
				}
				return a.renderGet(cmd, p, "/cluster/firewall/groups", "group", "comment")
			}},
		&cobra.Command{Use: "macros", Short: "List predefined macros (cluster scope)", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				p, err := a.Provider()
				if err != nil {
					return err
				}
				return a.renderGet(cmd, p, "/cluster/firewall/macros", "macro", "descr")
			}},
	)
	return cmd
}

func newFirewallRuleCmd(a *app, fwBase func(*cobra.Command) (provider.Provider, string, error)) *cobra.Command {
	cmd := &cobra.Command{Use: "rule", Short: "Add or delete firewall rules at the selected scope"}
	var set []string
	add := &cobra.Command{
		Use: "add", Short: "Add a firewall rule (use --set for fields)", Args: cobra.NoArgs,
		Example: "  pc firewall rule add --node pve-01 --set action=ACCEPT --set type=in --set proto=tcp --set dport=22",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, b, err := fwBase(cmd)
			if err != nil {
				return err
			}
			params, err := setToValues(set)
			if err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "POST", b+"/rules", params, "add firewall rule", true, 0)
		},
	}
	add.Flags().StringArrayVar(&set, "set", nil, "field key=value (repeatable)")
	del := &cobra.Command{
		Use: "delete <pos>", Aliases: []string{"rm"}, Short: "Delete a firewall rule by position", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, b, err := fwBase(cmd)
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("delete firewall rule at position %s?", args[0])); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", b+"/rules/"+args[0], nil, "delete firewall rule "+args[0], true, 0)
		},
	}
	cmd.AddCommand(add, del)
	return cmd
}
