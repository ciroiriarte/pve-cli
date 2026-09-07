package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/spf13/cobra"
)

// newNodeNetworkCmd builds `pc node network` — read plus day-0 interface
// management on a node's ifupdown config (/nodes/<node>/network). Creating,
// editing and deleting interfaces stages changes into
// /etc/network/interfaces.new; `apply` reloads them (ifupdown2). This is a
// separate layer from `pc sdn` (cluster-wide software-defined networking): a
// VLAN-aware bridge built here is what an SDN VLAN zone binds to.
//
// The parent command still lists interfaces (`pc node network <node>`), matching
// the pre-existing behaviour, and namespaces the write verbs beneath it.
func newNodeNetworkCmd(a *app) *cobra.Command {
	network := &cobra.Command{
		Use:   "network <node>",
		Short: "List and manage a node's network interfaces",
		Long: "Read and manage a node's ifupdown interfaces (bonds, bridges, VLANs).\n\n" +
			"Changes from create/update/delete are STAGED (/etc/network/interfaces.new)\n" +
			"until `pc node network apply <node>` reloads them. This is the host-interface\n" +
			"layer; `pc sdn` manages cluster-wide software-defined networking on top of it.\n\n" +
			"Bootstrap recipe (staged until applied):\n" +
			"  pc node network create <node> bond0    --type bond   --bond-mode active-backup --slaves eno1,eno2\n" +
			"  pc node network create <node> vmbr0    --type bridge --bridge-ports bond0 --vlan-aware\n" +
			"  pc node network create <node> vmbr0.10 --type vlan   --cidr 10.0.0.10/24 --gateway 10.0.0.1\n" +
			"  pc node network apply  <node>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return listNodeNetwork(a, cmd, args[0])
		},
	}
	network.AddCommand(
		// An explicit `list <node>` is the unambiguous way to list a node whose
		// hostname collides with a verb (e.g. a node literally named "apply"),
		// which cobra would otherwise route to the subcommand.
		&cobra.Command{
			Use: "list <node>", Short: "List a node's network interfaces", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return listNodeNetwork(a, cmd, args[0])
			},
		},
		newNodeNetworkShowCmd(a),
		newNodeNetworkCreateCmd(a),
		newNodeNetworkUpdateCmd(a),
		newNodeNetworkDeleteCmd(a),
		newNodeNetworkApplyCmd(a),
		newNodeNetworkRevertCmd(a),
	)
	return network
}

// listNodeNetwork renders a node's interface list, shared by the `network`
// parent (back-compat `pc node network <node>`) and the explicit `list` verb.
func listNodeNetwork(a *app, cmd *cobra.Command, node string) error {
	p, err := a.Provider()
	if err != nil {
		return err
	}
	return a.renderGet(cmd, p, "/nodes/"+node+"/network", "iface", "type", "method", "address", "active")
}

func newNodeNetworkShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "show <node> <iface>", Short: "Show a single interface's config", Args: cobra.ExactArgs(2),
		Example: "  pc node network show pve-01 vmbr0",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			return a.renderGet(cmd, p, "/nodes/"+args[0]+"/network/"+args[1])
		},
	}
}

// knownIfaceTypes are the interface types promoted to first-class support. Any
// other PVE type (OVSBridge, vxlan, …) is still reachable via --set type=….
var knownIfaceTypes = map[string]bool{
	"bond": true, "bridge": true, "vlan": true, "eth": true, "alias": true,
}

func newNodeNetworkCreateCmd(a *app) *cobra.Command {
	var iface, typ, cidr, gateway, comment, mtu, bondMode, vids string
	var slaves, bridgePorts []string
	var vlanAware, autostart bool
	var set []string
	cmd := &cobra.Command{
		Use:   "create <node> <iface>",
		Short: "Create a network interface (staged until `apply`)",
		Long: "Creates an interface on the node, staged into /etc/network/interfaces.new\n" +
			"until `pc node network apply <node>`. --type is required (bond|bridge|vlan|\n" +
			"eth|alias; other PVE types via --set type=…). --slaves/--bridge-ports accept a\n" +
			"comma-separated or repeated list and are sent as PVE's space-separated form.",
		Example: "  pc node network create pve-01 vmbr0 --type bridge --bridge-ports bond0 --vlan-aware --cidr 192.0.2.10/24 --gateway 192.0.2.1",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVE(p, "node network create", ""); err != nil {
				return err
			}
			node := args[0]
			if len(args) == 2 {
				iface = args[1]
			}
			if iface == "" {
				return fmt.Errorf("interface name is required (positional <iface> or --iface)")
			}
			// --type may be supplied via --set type=… (the escape hatch for
			// uncurated PVE types like OVSBridge), so only demand --type when the
			// escape hatch didn't set it.
			forcedType := setHasKey(set, "type")
			if typ == "" && !forcedType {
				return fmt.Errorf("--type is required (bond|bridge|vlan|eth|alias; other types via --set type=…)")
			}
			if typ != "" && !knownIfaceTypes[typ] && !forcedType {
				return fmt.Errorf("unsupported --type %q; use one of bond|bridge|vlan|eth|alias, or pass --set type=%s to force it", typ, typ)
			}
			// Only cross-check flag/type combos for a curated type; a forced type
			// takes full responsibility for its own flags (PVE validates them).
			if knownIfaceTypes[typ] {
				if err := validateIfaceFlags(cmd, typ); err != nil {
					return err
				}
			}
			base := map[string]string{
				"type": typ, "cidr": cidr, "gateway": gateway,
				// PVE's node-network schema spells the description "comments"
				// (plural), unlike /access/domains' "comment" — verified live.
				"comments": comment, "mtu": mtu, "bond_mode": bondMode,
				"bridge_vids": vids,
			}
			if s := joinPorts(slaves); s != "" {
				base["slaves"] = s
			}
			if s := joinPorts(bridgePorts); s != "" {
				base["bridge_ports"] = s
			}
			if typ == "bridge" {
				base["bridge_vlan_aware"] = boolParam(vlanAware)
			}
			// autostart defaults to true on create so a freshly-built bond/bridge
			// survives the next reboot (a headless node otherwise goes dark).
			base["autostart"] = boolParam(autostart)
			params, err := mergeSet(base, set)
			if err != nil {
				return err
			}
			params.Set("iface", iface)
			if err := rawMutate(cmd.Context(), a, p, "POST", "/nodes/"+node+"/network", params, "create iface "+iface, true, 0); err != nil {
				return err
			}
			stagedNudge(node, iface)
			return nil
		},
	}
	cmd.Flags().StringVar(&typ, "type", "", "interface type: bond|bridge|vlan|eth|alias")
	cmd.Flags().StringVar(&iface, "iface", "", "interface name (compat alias for the positional <iface>)")
	_ = cmd.Flags().MarkHidden("iface")
	cmd.Flags().StringVar(&cidr, "cidr", "", "IPv4/IPv6 address in CIDR form (e.g. 192.0.2.10/24)")
	cmd.Flags().StringVar(&gateway, "gateway", "", "default gateway")
	cmd.Flags().StringVar(&comment, "comment", "", "interface comment/description")
	cmd.Flags().StringVar(&mtu, "mtu", "", "MTU")
	cmd.Flags().BoolVar(&autostart, "autostart", true, "start the interface on boot (default true)")
	cmd.Flags().StringVar(&bondMode, "bond-mode", "", "bond mode (bond only, e.g. active-backup, 802.3ad)")
	cmd.Flags().StringSliceVar(&slaves, "slaves", nil, "bond member interfaces (bond only; comma-separated or repeatable)")
	cmd.Flags().StringSliceVar(&bridgePorts, "bridge-ports", nil, "bridge member ports (bridge only; comma-separated or repeatable)")
	cmd.Flags().BoolVar(&vlanAware, "vlan-aware", false, "make the bridge VLAN-aware (bridge only)")
	cmd.Flags().StringVar(&vids, "vids", "", "bridge VLAN ids/ranges (bridge only, e.g. 2-4094)")
	cmd.Flags().StringArrayVar(&set, "set", nil, "any other field key=value (escape hatch, repeatable)")
	_ = cmd.RegisterFlagCompletionFunc("type", cobra.FixedCompletions([]string{"bond", "bridge", "vlan", "eth", "alias"}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

func newNodeNetworkUpdateCmd(a *app) *cobra.Command {
	var cidr, gateway, comment, mtu, bondMode, vids string
	var slaves, bridgePorts []string
	var vlanAware, autostart bool
	var set []string
	cmd := &cobra.Command{
		Use:   "update <node> <iface>",
		Short: "Edit a network interface (staged until `apply`)",
		Long: "Edits an interface, staged until `pc node network apply <node>`. Only the\n" +
			"flags you pass are changed; booleans (--autostart/--vlan-aware) are sent only\n" +
			"when set, so untouched fields keep their current values. The interface's type\n" +
			"is read automatically (PVE requires it on edit). Clear an optional field with\n" +
			"`--set delete=<field>` (e.g. --set delete=gateway).",
		Example: "  pc node network update pve-01 vmbr0 --mtu 9000",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVE(p, "node network update", ""); err != nil {
				return err
			}
			node, iface := args[0], args[1]
			// PVE's network PUT requires `type` even for a partial edit, so fetch
			// the interface's current type and pass it through (the caller edits
			// one field like --mtu without having to restate --type).
			ifpath := "/nodes/" + node + "/network/" + iface
			body, err := p.Raw(cmd.Context(), "GET", ifpath, nil)
			if err != nil {
				return fmt.Errorf("read interface %q on %q: %w", iface, node, err)
			}
			var cur map[string]any
			if derr := protocol.DecodeData(body, &cur); derr != nil || cur["type"] == nil {
				return fmt.Errorf("could not determine the type of interface %q on %q", iface, node)
			}
			base := map[string]string{
				"type": fmt.Sprintf("%v", cur["type"]),
				"cidr": cidr, "gateway": gateway, "comments": comment,
				"mtu": mtu, "bond_mode": bondMode, "bridge_vids": vids,
			}
			if s := joinPorts(slaves); s != "" {
				base["slaves"] = s
			}
			if s := joinPorts(bridgePorts); s != "" {
				base["bridge_ports"] = s
			}
			if cmd.Flags().Changed("vlan-aware") {
				base["bridge_vlan_aware"] = boolParam(vlanAware)
			}
			if cmd.Flags().Changed("autostart") {
				base["autostart"] = boolParam(autostart)
			}
			params, err := mergeSet(base, set)
			if err != nil {
				return err
			}
			if err := rawMutate(cmd.Context(), a, p, "PUT", ifpath, params, "update iface "+iface, true, 0); err != nil {
				return err
			}
			stagedNudge(node, iface)
			return nil
		},
	}
	cmd.Flags().StringVar(&cidr, "cidr", "", "IPv4/IPv6 address in CIDR form")
	cmd.Flags().StringVar(&gateway, "gateway", "", "default gateway")
	cmd.Flags().StringVar(&comment, "comment", "", "interface comment/description")
	cmd.Flags().StringVar(&mtu, "mtu", "", "MTU")
	cmd.Flags().BoolVar(&autostart, "autostart", true, "start the interface on boot")
	cmd.Flags().StringVar(&bondMode, "bond-mode", "", "bond mode (bond only)")
	cmd.Flags().StringSliceVar(&slaves, "slaves", nil, "bond member interfaces (comma-separated or repeatable)")
	cmd.Flags().StringSliceVar(&bridgePorts, "bridge-ports", nil, "bridge member ports (comma-separated or repeatable)")
	cmd.Flags().BoolVar(&vlanAware, "vlan-aware", false, "make the bridge VLAN-aware")
	cmd.Flags().StringVar(&vids, "vids", "", "bridge VLAN ids/ranges (e.g. 2-4094)")
	cmd.Flags().StringArrayVar(&set, "set", nil, "any other field key=value (escape hatch, repeatable)")
	return cmd
}

func newNodeNetworkDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "delete <node> <iface>", Aliases: []string{"rm"},
		Short: "Delete a network interface (staged until `apply`)", Args: cobra.ExactArgs(2),
		Example: "  pc node network delete pve-01 vmbr0",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVE(p, "node network delete", ""); err != nil {
				return err
			}
			node, iface := args[0], args[1]
			if err := confirm(a, fmt.Sprintf("delete interface %q on node %q? (staged until apply)", iface, node)); err != nil {
				return err
			}
			if err := rawMutate(cmd.Context(), a, p, "DELETE", "/nodes/"+node+"/network/"+iface, nil, "delete iface "+iface, true, 0); err != nil {
				return err
			}
			stagedNudge(node, iface)
			return nil
		},
	}
}

func newNodeNetworkApplyCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "apply <node>", Aliases: []string{"reload"},
		Short: "Apply (reload) a node's pending network config", Args: cobra.ExactArgs(1),
		Example: "  pc node network apply pve-01",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVE(p, "node network apply", ""); err != nil {
				return err
			}
			node := args[0]
			if err := confirm(a, fmt.Sprintf("apply pending network configuration on node %q? (reloads interfaces via ifupdown2; active SSH and API sessions may drop)", node)); err != nil {
				return err
			}
			if err := rawMutate(cmd.Context(), a, p, "PUT", "/nodes/"+node+"/network", nil, "apply network config on "+node, true, 0); err != nil {
				// Only a transport failure (no HTTP response) plausibly means the
				// reload cut our own connection. A 4xx/5xx or task failure came
				// back over a live link — don't cry "connection dropped" then.
				var apiErr *protocol.APIError
				if errors.As(err, &apiErr) && apiErr.Kind == protocol.KindTransport {
					fmt.Fprintf(stderrWriter(), "[pc] notice: the reload may have dropped this connection; if the management IP/VLAN changed, re-target your profile and re-check with `pc node network %s`\n", node)
				}
				return err
			}
			return nil
		},
	}
}

func newNodeNetworkRevertCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "revert <node>", Aliases: []string{"discard"},
		Short: "Discard a node's staged (unapplied) network changes", Args: cobra.ExactArgs(1),
		Example: "  pc node network revert pve-01",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVE(p, "node network revert", ""); err != nil {
				return err
			}
			node := args[0]
			if err := confirm(a, fmt.Sprintf("discard all staged (unapplied) network changes on node %q?", node)); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", "/nodes/"+node+"/network", nil, "revert staged network changes on "+node, true, 0)
		},
	}
}

// validateIfaceFlags rejects flags that don't apply to the chosen type, so a
// typo is caught client-side rather than silently ignored by the API.
func validateIfaceFlags(cmd *cobra.Command, typ string) error {
	for _, f := range []string{"slaves", "bond-mode"} {
		if cmd.Flags().Changed(f) && typ != "bond" {
			return fmt.Errorf("--%s is only valid for --type bond (for bridges use --bridge-ports)", f)
		}
	}
	for _, f := range []string{"bridge-ports", "vlan-aware", "vids"} {
		if cmd.Flags().Changed(f) && typ != "bridge" {
			return fmt.Errorf("--%s is only valid for --type bridge", f)
		}
	}
	return nil
}

// joinPorts flattens a list flag (comma-separated, repeated, or space-delimited
// within an element) into PVE's space-separated string form, preserving order
// and dropping empties. Order is kept because it can matter to the operator.
func joinPorts(vals []string) string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		for _, f := range strings.Fields(v) {
			out = append(out, f)
		}
	}
	return strings.Join(out, " ")
}

// setHasKey reports whether a --set entry sets the given key (so a curated
// validation can defer to the escape hatch).
func setHasKey(set []string, key string) bool {
	for _, kv := range set {
		if k, _, ok := strings.Cut(kv, "="); ok && k == key {
			return true
		}
	}
	return false
}

// stagedNudge prints the TTY-gated reminder that a change is staged until apply
// (stderr, so machine-consumed output stays clean — design decision #7).
func stagedNudge(node, iface string) {
	if isTTY() {
		fmt.Fprintf(stderrWriter(), "[pc] %s staged on %s (/etc/network/interfaces.new); run `pc node network apply %s` to activate\n", iface, node, node)
	}
}
