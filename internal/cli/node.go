package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/domain"
	"github.com/ciroiriarte/pve-cli/internal/output"
)

func newNodeCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Inspect cluster nodes",
	}
	cmd.AddCommand(newNodeListCmd(a), newNodeShowCmd(a))
	cmd.AddCommand(newNodeOpsCmds(a)...)
	return cmd
}

func newNodeListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List cluster nodes",
		Example: "  pc node list\n" +
			"  pc node list -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			nodes, err := p.ListNodes(cmd.Context())
			if err != nil {
				return err
			}
			sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
			return a.render(nodesTable(nodes))
		},
	}
}

func newNodeShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "show <node>",
		Short:   "Show a single node",
		Example: "  pc node show pve-01",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			nodes, err := p.ListNodes(cmd.Context())
			if err != nil {
				return err
			}
			for _, n := range nodes {
				if n.Name == args[0] {
					return a.render(nodesTable([]domain.Node{n}))
				}
			}
			return fmt.Errorf("node %q not found", args[0])
		},
	}
}

func nodesTable(nodes []domain.Node) output.Tabular {
	t := output.Tabular{
		Columns: []string{"node", "status", "cpu", "mem", "maxmem", "uptime"},
		Raw:     nodes,
	}
	for _, n := range nodes {
		cpu := "-"
		if n.MaxCPU > 0 {
			cpu = fmt.Sprintf("%.0f%% of %d", n.CPU*100, n.MaxCPU)
		}
		t.Rows = append(t.Rows, []string{
			n.Name,
			n.Status,
			cpu,
			humanBytes(n.Mem),
			humanBytes(n.MaxMem),
			humanUptime(n.Uptime),
		})
	}
	return t
}
