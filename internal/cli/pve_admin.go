package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newPoolCmd manages resource pools (PVE; also works via PDM where present).
func newPoolCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "pool", Short: "Manage resource pools"}
	cmd.AddCommand(
		anyGet(a, "list", "List pools", 0, func([]string) string { return "/pools" }, "poolid", "comment"),
		anyGet(a, "show <poolid>", "Show a pool and its members", 1, func(a []string) string { return "/pools/" + a[0] }),
	)
	var comment string
	create := &cobra.Command{
		Use: "create <poolid>", Short: "Create a pool", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			params := map[string][]string{"poolid": {args[0]}}
			if comment != "" {
				params["comment"] = []string{comment}
			}
			return rawMutate(cmd.Context(), a, p, "POST", "/pools", params, "create pool "+args[0], true, 0)
		},
	}
	create.Flags().StringVar(&comment, "comment", "", "pool comment")
	del := &cobra.Command{
		Use: "delete <poolid>", Aliases: []string{"rm"}, Short: "Delete a pool", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("delete pool %q?", args[0])); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", "/pools/"+args[0], nil, "delete pool "+args[0], true, 0)
		},
	}
	var addVMs, addStorage string
	var removeMembers bool
	update := &cobra.Command{
		Use: "update <poolid>", Short: "Add or remove pool members", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if addVMs == "" && addStorage == "" {
				return fmt.Errorf("pass --vms and/or --storage")
			}
			params := map[string][]string{}
			if addVMs != "" {
				params["vms"] = []string{addVMs}
			}
			if addStorage != "" {
				params["storage"] = []string{addStorage}
			}
			if removeMembers {
				params["delete"] = []string{"1"}
			}
			return rawMutate(cmd.Context(), a, p, "PUT", "/pools/"+args[0], params, "update pool "+args[0], true, 0)
		},
	}
	update.Flags().StringVar(&addVMs, "vms", "", "comma-separated vmids")
	update.Flags().StringVar(&addStorage, "storage", "", "comma-separated storage ids")
	update.Flags().BoolVar(&removeMembers, "remove", false, "remove the listed members instead of adding")
	cmd.AddCommand(create, del, update)
	return cmd
}

// newHACmd manages High Availability resources and groups (PVE /cluster/ha).
func newHACmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "ha", Short: "Manage High Availability (resources, groups, status)"}
	cmd.AddCommand(
		anyGet(a, "status", "Show current HA manager status", 0, func([]string) string { return "/cluster/ha/status/current" }, "id", "type", "status", "node"),
		newHAResourceCmd(a),
		anyGet(a, "groups", "List HA groups", 0, func([]string) string { return "/cluster/ha/groups" }, "group", "nodes", "restricted"),
	)
	return cmd
}

func newHAResourceCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "resource", Aliases: []string{"res"}, Short: "Manage HA resources"}
	cmd.AddCommand(
		anyGet(a, "list", "List HA resources", 0, func([]string) string { return "/cluster/ha/resources" }, "sid", "state", "group", "max_restart"),
		anyGet(a, "show <sid>", "Show an HA resource", 1, func(a []string) string { return "/cluster/ha/resources/" + a[0] }),
	)
	var group, state string
	add := &cobra.Command{
		Use: "add <sid>", Short: "Put a guest under HA (e.g. vm:100)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			params := map[string][]string{"sid": {args[0]}}
			if group != "" {
				params["group"] = []string{group}
			}
			if state != "" {
				params["state"] = []string{state}
			}
			return rawMutate(cmd.Context(), a, p, "POST", "/cluster/ha/resources", params, "ha add "+args[0], true, 0)
		},
	}
	add.Flags().StringVar(&group, "group", "", "HA group")
	add.Flags().StringVar(&state, "state", "", "requested state (started|stopped|disabled)")
	rm := &cobra.Command{
		Use: "remove <sid>", Aliases: []string{"rm"}, Short: "Remove a resource from HA", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("remove %q from HA?", args[0])); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", "/cluster/ha/resources/"+args[0], nil, "ha remove "+args[0], true, 0)
		},
	}
	cmd.AddCommand(add, rm)
	return cmd
}

// newNodeOpsCmds returns node-scoped admin commands (services, apt, network,
// subscription) added under `pc node`.
func newNodeOpsCmds(a *app) []*cobra.Command {
	service := &cobra.Command{Use: "service", Short: "Inspect and control node services"}
	service.AddCommand(
		anyGet(a, "list <node>", "List node services", 1, func(a []string) string { return "/nodes/" + a[0] + "/services" }, "name", "desc", "state", "active"),
	)
	for _, action := range []string{"start", "stop", "restart", "reload"} {
		act := action
		service.AddCommand(&cobra.Command{
			Use: act + " <node> <service>", Short: act + " a node service", Args: cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				p, err := a.Provider()
				if err != nil {
					return err
				}
				if act == "stop" || act == "restart" {
					if err := confirm(a, fmt.Sprintf("%s service %q on %s?", act, args[1], args[0])); err != nil {
						return err
					}
				}
				return rawMutate(cmd.Context(), a, p, "POST",
					fmt.Sprintf("/nodes/%s/services/%s/%s", args[0], args[1], act), nil,
					fmt.Sprintf("%s %s on %s", act, args[1], args[0]), true, 0)
			},
		})
	}

	apt := &cobra.Command{Use: "apt", Short: "Inspect node package updates"}
	apt.AddCommand(
		anyGet(a, "versions <node>", "Installed package versions", 1, func(a []string) string { return "/nodes/" + a[0] + "/apt/versions" }, "Package", "OldVersion", "Version"),
		anyGet(a, "updates <node>", "Available package updates", 1, func(a []string) string { return "/nodes/" + a[0] + "/apt/update" }, "Package", "OldVersion", "Version"),
	)

	network := &cobra.Command{
		Use: "network <node>", Short: "List a node's network interfaces", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			return a.renderGet(cmd, p, "/nodes/"+args[0]+"/network", "iface", "type", "method", "address", "active")
		},
	}
	subscription := &cobra.Command{
		Use: "subscription <node>", Short: "Show a node's subscription status", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			return a.renderGet(cmd, p, "/nodes/"+args[0]+"/subscription")
		},
	}
	return []*cobra.Command{service, apt, network, subscription}
}
