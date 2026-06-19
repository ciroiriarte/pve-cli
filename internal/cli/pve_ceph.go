package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newCephMgmtCmds returns PVE node-scoped Ceph management commands, added under
// `pc ceph` alongside the PDM monitoring verbs. These require --node and act on
// /nodes/{node}/ceph (the PDM verbs take a <cluster> and are PDM-gated), so the
// two sets don't collide and each works on its own provider.
func newCephMgmtCmds(a *app) []*cobra.Command {
	// nodeGet/nodeMutate are small helpers requiring --node.
	// Ceph state is cluster-wide, so these reads route through any online node
	// when --node is omitted.
	withNode := func(c *cobra.Command, node *string) *cobra.Command {
		c.Flags().StringVar(node, "node", "", "node to query (optional; defaults to an online node)")
		return c
	}

	var hNode string
	health := &cobra.Command{
		Use: "health", Short: "Ceph cluster health/status (via a node)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			n, err := nodeOrAuto(cmd.Context(), p, hNode)
			if err != nil {
				return err
			}
			return a.renderGet(cmd, p, "/nodes/"+n+"/ceph/status")
		},
	}
	withNode(health, &hNode)

	osd := newCephOSDCmd(a)
	pool := newCephPoolCmd(a)

	var svcNode, svcService string
	service := &cobra.Command{Use: "service", Short: "Start/stop/restart Ceph services on a node"}
	for _, action := range []string{"start", "stop", "restart"} {
		act := action
		sub := &cobra.Command{
			Use: act, Short: act + " Ceph services on a node", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				p, err := a.Provider()
				if err != nil {
					return err
				}
				if svcNode == "" {
					return fmt.Errorf("--node is required")
				}
				if act != "start" {
					if err := confirm(a, fmt.Sprintf("%s Ceph services on %s?", act, svcNode)); err != nil {
						return err
					}
				}
				params := map[string][]string{}
				if svcService != "" {
					params["service"] = []string{svcService}
				}
				return rawMutate(cmd.Context(), a, p, "POST", "/nodes/"+svcNode+"/ceph/"+act, params,
					fmt.Sprintf("%s ceph on %s", act, svcNode), true, 0)
			},
		}
		sub.Flags().StringVar(&svcNode, "node", "", "node (required)")
		sub.Flags().StringVar(&svcService, "service", "", "specific service (e.g. mon.pve-01); default all")
		service.AddCommand(sub)
	}

	var cfgNode string
	config := &cobra.Command{
		Use: "config", Short: "Show Ceph config (ceph.conf) from a node", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			n, err := nodeOrAuto(cmd.Context(), p, cfgNode)
			if err != nil {
				return err
			}
			return a.renderGet(cmd, p, "/nodes/"+n+"/ceph/cfg/raw")
		},
	}
	withNode(config, &cfgNode)

	return []*cobra.Command{health, osd, pool, service, config}
}

func newCephOSDCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "osd", Short: "Manage Ceph OSDs (--node required for writes)"}
	var node string
	cmd.PersistentFlags().StringVar(&node, "node", "", "node (required for writes; optional for list)")
	need := func() (string, error) {
		if node == "" {
			return "", fmt.Errorf("--node is required")
		}
		return node, nil
	}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List OSDs (tree)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			// The OSD map is cluster-wide; any online node serves it.
			n, err := nodeOrAuto(cmd.Context(), p, node)
			if err != nil {
				return err
			}
			return a.renderGet(cmd, p, "/nodes/"+n+"/ceph/osd")
		},
	})
	for _, action := range []string{"in", "out", "scrub"} {
		act := action
		cmd.AddCommand(&cobra.Command{
			Use: act + " <osdid>", Short: act + " an OSD", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				p, err := a.Provider()
				if err != nil {
					return err
				}
				n, err := need()
				if err != nil {
					return err
				}
				return rawMutate(cmd.Context(), a, p, "POST", fmt.Sprintf("/nodes/%s/ceph/osd/%s/%s", n, args[0], act), nil,
					fmt.Sprintf("osd %s %s", args[0], act), true, 0)
			},
		})
	}
	cmd.AddCommand(&cobra.Command{
		Use: "destroy <osdid>", Short: "Destroy an OSD", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			n, err := need()
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("destroy OSD %s on %s (data loss)?", args[0], n)); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", fmt.Sprintf("/nodes/%s/ceph/osd/%s", n, args[0]), nil,
				"destroy osd "+args[0], true, 0)
		},
	})
	return cmd
}

func newCephPoolCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "pool", Short: "Manage Ceph pools (--node required for writes)"}
	var node string
	cmd.PersistentFlags().StringVar(&node, "node", "", "node (required for writes; optional for list)")
	need := func() (string, error) {
		if node == "" {
			return "", fmt.Errorf("--node is required")
		}
		return node, nil
	}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List Ceph pools", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			// Ceph pools are cluster-wide; any online node serves the list.
			n, err := nodeOrAuto(cmd.Context(), p, node)
			if err != nil {
				return err
			}
			return a.renderGet(cmd, p, "/nodes/"+n+"/ceph/pool", "pool_name", "size", "min_size", "pg_num")
		},
	})
	var set []string
	create := &cobra.Command{
		Use: "create <name>", Short: "Create a Ceph pool (use --set for fields)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			n, err := need()
			if err != nil {
				return err
			}
			params, err := setToValues(set)
			if err != nil {
				return err
			}
			params.Set("name", args[0])
			return rawMutate(cmd.Context(), a, p, "POST", "/nodes/"+n+"/ceph/pool", params, "create ceph pool "+args[0], true, 0)
		},
	}
	create.Flags().StringArrayVar(&set, "set", nil, "field key=value (e.g. --set size=3)")
	del := &cobra.Command{
		Use: "delete <name>", Aliases: []string{"rm"}, Short: "Delete a Ceph pool", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			n, err := need()
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("delete Ceph pool %q on %s (data loss)?", args[0], n)); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", "/nodes/"+n+"/ceph/pool/"+args[0], nil, "delete ceph pool "+args[0], true, 0)
		},
	}
	cmd.AddCommand(create, del)
	return cmd
}
