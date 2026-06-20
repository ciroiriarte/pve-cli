package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// sdnBase resolves the active provider and its SDN root path. PVE serves SDN
// under /cluster/sdn; PDM exposes a smaller set at /sdn.
func (a *app) sdnBase() (provider.Provider, string, error) {
	p, err := a.Provider()
	if err != nil {
		return nil, "", err
	}
	if p.Name() == "pdm" {
		return p, "/sdn", nil
	}
	return p, "/cluster/sdn", nil
}

// newSDNCmd manages software-defined networking (PVE /cluster/sdn; a subset on PDM).
func newSDNCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "sdn", Short: "Manage SDN zones, vnets, subnets, controllers"}
	cmd.AddCommand(newSDNZoneCmd(a), newSDNVnetCmd(a), newSDNSubnetCmd(a), newSDNControllerCmd(a))

	// PVE-only read helpers + apply.
	cmd.AddCommand(&cobra.Command{
		Use: "ipams", Short: "List IPAMs", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, b, err := a.sdnBase()
			if err != nil {
				return err
			}
			return a.renderGet(cmd, p, b+"/ipams", "ipam", "type")
		},
	}, &cobra.Command{
		Use: "dns", Short: "List SDN DNS plugins", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, b, err := a.sdnBase()
			if err != nil {
				return err
			}
			return a.renderGet(cmd, p, b+"/dns", "dns", "type")
		},
	}, &cobra.Command{
		Use: "apply", Short: "Apply pending SDN configuration (reload)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, b, err := a.sdnBase()
			if err != nil {
				return err
			}
			if err := confirm(a, "apply pending SDN configuration (reloads networking cluster-wide)?"); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "PUT", b, nil, "apply SDN config", true, 0)
		},
	})
	return cmd
}

func newSDNZoneCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "zone", Short: "Manage SDN zones"}
	cmd.AddCommand(
		sdnList(a, "list", "List zones", "/zones", "zone", "type", "ipam", "mtu"),
		sdnShow(a, "show <zone>", "Show a zone", "/zones/"),
		sdnDelete(a, "zone", "/zones/"),
	)
	var typ, bridge string
	var set []string
	create := &cobra.Command{
		Use: "create <zone>", Short: "Create a zone (--type required)", Args: cobra.ExactArgs(1),
		Example: "  pc sdn zone create dmz --type vlan --bridge vmbr0",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, b, err := a.sdnBase()
			if err != nil {
				return err
			}
			if typ == "" {
				return fmt.Errorf("--type is required (e.g. vlan, vxlan, qinq, simple, evpn)")
			}
			params, err := mergeSet(map[string]string{"bridge": bridge}, set)
			if err != nil {
				return err
			}
			params.Set("zone", args[0])
			params.Set("type", typ)
			return rawMutate(cmd.Context(), a, p, "POST", b+"/zones", params, "create zone "+args[0], true, 0)
		},
	}
	create.Flags().StringVar(&typ, "type", "", "zone type")
	create.Flags().StringVar(&bridge, "bridge", "", "underlying bridge (e.g. vmbr0; vlan/qinq)")
	create.Flags().StringArrayVar(&set, "set", nil, "any other field key=value (escape hatch, repeatable)")
	cmd.AddCommand(create)
	return cmd
}

func newSDNVnetCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "vnet", Short: "Manage SDN vnets"}
	cmd.AddCommand(
		sdnList(a, "list", "List vnets", "/vnets", "vnet", "zone", "tag", "alias"),
		sdnShow(a, "show <vnet>", "Show a vnet", "/vnets/"),
		sdnDelete(a, "vnet", "/vnets/"),
	)
	var zone, tag, alias string
	var set []string
	create := &cobra.Command{
		Use: "create <vnet>", Short: "Create a vnet (--zone required)", Args: cobra.ExactArgs(1),
		Example: "  pc sdn vnet create v100 --zone dmz --tag 100",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, b, err := a.sdnBase()
			if err != nil {
				return err
			}
			if zone == "" {
				return fmt.Errorf("--zone is required")
			}
			params, err := mergeSet(map[string]string{"tag": tag, "alias": alias}, set)
			if err != nil {
				return err
			}
			params.Set("vnet", args[0])
			params.Set("zone", zone)
			return rawMutate(cmd.Context(), a, p, "POST", b+"/vnets", params, "create vnet "+args[0], true, 0)
		},
	}
	create.Flags().StringVar(&zone, "zone", "", "parent zone")
	create.Flags().StringVar(&tag, "tag", "", "VLAN/VXLAN tag")
	create.Flags().StringVar(&alias, "alias", "", "vnet alias/description")
	create.Flags().StringArrayVar(&set, "set", nil, "any other field key=value (escape hatch, repeatable)")
	cmd.AddCommand(create)
	return cmd
}

func newSDNSubnetCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "subnet", Short: "Manage SDN subnets within a vnet"}
	cmd.AddCommand(&cobra.Command{
		Use: "list <vnet>", Short: "List subnets of a vnet", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, b, err := a.sdnBase()
			if err != nil {
				return err
			}
			return a.renderGet(cmd, p, b+"/vnets/"+args[0]+"/subnets", "subnet", "type", "gateway", "snat")
		},
	})
	var set []string
	var gateway string
	var snat bool
	create := &cobra.Command{
		Use: "create <vnet> <subnet>", Short: "Create a subnet (e.g. 10.0.0.0/24)", Args: cobra.ExactArgs(2),
		Example: "  pc sdn subnet create v100 10.0.0.0/24 --gateway 10.0.0.1 --snat",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, b, err := a.sdnBase()
			if err != nil {
				return err
			}
			base := map[string]string{"gateway": gateway}
			if cmd.Flags().Changed("snat") {
				base["snat"] = boolParam(snat)
			}
			params, err := mergeSet(base, set)
			if err != nil {
				return err
			}
			params.Set("subnet", args[1])
			params.Set("type", "subnet")
			return rawMutate(cmd.Context(), a, p, "POST", b+"/vnets/"+args[0]+"/subnets", params, "create subnet "+args[1], true, 0)
		},
	}
	create.Flags().StringVar(&gateway, "gateway", "", "subnet gateway IP")
	create.Flags().BoolVar(&snat, "snat", false, "enable SNAT for the subnet")
	create.Flags().StringArrayVar(&set, "set", nil, "any other field key=value (escape hatch, repeatable)")
	del := &cobra.Command{
		Use: "delete <vnet> <subnet>", Aliases: []string{"rm"}, Short: "Delete a subnet", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, b, err := a.sdnBase()
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("delete subnet %q from vnet %q?", args[1], args[0])); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", b+"/vnets/"+args[0]+"/subnets/"+args[1], nil, "delete subnet "+args[1], true, 0)
		},
	}
	cmd.AddCommand(create, del)
	return cmd
}

func newSDNControllerCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "controller", Aliases: []string{"ctrl"}, Short: "Manage SDN controllers"}
	cmd.AddCommand(
		sdnList(a, "list", "List controllers", "/controllers", "controller", "type"),
		sdnShow(a, "show <controller>", "Show a controller", "/controllers/"),
		sdnDelete(a, "controller", "/controllers/"),
	)
	var typ string
	var set []string
	create := &cobra.Command{
		Use: "create <controller>", Short: "Create a controller (--type required)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, b, err := a.sdnBase()
			if err != nil {
				return err
			}
			if typ == "" {
				return fmt.Errorf("--type is required (e.g. evpn, bgp, faucet)")
			}
			params, err := setToValues(set)
			if err != nil {
				return err
			}
			params.Set("controller", args[0])
			params.Set("type", typ)
			return rawMutate(cmd.Context(), a, p, "POST", b+"/controllers", params, "create controller "+args[0], true, 0)
		},
	}
	create.Flags().StringVar(&typ, "type", "", "controller type")
	create.Flags().StringArrayVar(&set, "set", nil, "field key=value (repeatable)")
	cmd.AddCommand(create)
	return cmd
}

// sdnList/sdnShow/sdnDelete are small builders over sdnBase to cut boilerplate.
func sdnList(a *app, use, short, suffix string, cols ...string) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: short, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, b, err := a.sdnBase()
			if err != nil {
				return err
			}
			return a.renderGet(cmd, p, b+suffix, cols...)
		},
	}
}

func sdnShow(a *app, use, short, prefix string) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, b, err := a.sdnBase()
			if err != nil {
				return err
			}
			return a.renderGet(cmd, p, b+prefix+args[0])
		},
	}
}

func sdnDelete(a *app, kind, prefix string) *cobra.Command {
	return &cobra.Command{
		Use: "delete <" + kind + ">", Aliases: []string{"rm"}, Short: "Delete a " + kind, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, b, err := a.sdnBase()
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("delete %s %q?", kind, args[0])); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", b+prefix+args[0], nil, "delete "+kind+" "+args[0], true, 0)
		},
	}
}
