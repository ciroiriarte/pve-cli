package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
)

// mapsTable renders a slice of JSON objects as a table. Columns in `preferred`
// come first (in that order); any remaining keys follow, sorted.
func mapsTable(rows []map[string]any, preferred ...string) output.Tabular {
	seen := map[string]bool{}
	cols := make([]string, 0, len(preferred))
	for _, c := range preferred {
		cols = append(cols, c)
		seen[c] = true
	}
	extra := []string{}
	for _, r := range rows {
		for k := range r {
			if !seen[k] {
				seen[k] = true
				extra = append(extra, k)
			}
		}
	}
	sort.Strings(extra)
	cols = append(cols, extra...)

	t := output.Tabular{Columns: cols, Raw: rows}
	for _, r := range rows {
		row := make([]string, len(cols))
		for i, c := range cols {
			if v, ok := r[c]; ok && v != nil {
				row[i] = fmt.Sprintf("%v", v)
			}
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// newGuestMonitorCmds returns the read-only monitoring commands shared by the
// vm and ct trees. They work on both providers: PVE addresses the node, PDM
// proxies the same endpoints under /pve/remotes/{remote}.
// newGuestStatusCmd builds the live-runtime "status" command. It is shared by
// the typed vm/ct trees and the unified `guest` command, so the path follows the
// resolved guest's kind.
func newGuestStatusCmd(a *app, spec guestSpec) *cobra.Command {
	var node, remote string
	cmd := &cobra.Command{
		Use:     "status <vmid>",
		Short:   fmt.Sprintf("Show live runtime status of a %s", spec.label),
		Example: fmt.Sprintf("  pc %s status 100", spec.noun),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, g, b, err := resolveGuestBase(cmd, a, spec, args[0], node, remote)
			if err != nil {
				return err
			}
			// PVE serves live status at /status/current; PDM's proxy at /status.
			statusPath := b + "/status/current"
			if p.Name() == "pdm" {
				statusPath = b + "/status"
			}
			body, err := p.Raw(cmd.Context(), "GET", statusPath, nil)
			if err != nil {
				return err
			}
			var st map[string]any
			if err := protocol.DecodeData(body, &st); err != nil {
				return err
			}
			return a.render(guestConfigTable(g, st))
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "node hosting the guest (skips auto-resolution)")
	cmd.Flags().StringVar(&remote, "remote", "", "PDM remote that hosts the guest")
	return cmd
}

func newGuestMonitorCmds(a *app, spec guestSpec) []*cobra.Command {
	var node, remote string
	addScope := func(c *cobra.Command) {
		c.Flags().StringVar(&node, "node", "", "node hosting the guest (skips auto-resolution)")
		c.Flags().StringVar(&remote, "remote", "", "PDM remote that hosts the guest")
	}

	status := newGuestStatusCmd(a, spec)

	pending := &cobra.Command{
		Use:     "pending <vmid>",
		Short:   fmt.Sprintf("Show pending config changes for a %s", spec.label),
		Example: fmt.Sprintf("  pc %s pending 100", spec.noun),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, _, b, err := resolveGuestBase(cmd, a, spec, args[0], node, remote)
			if err != nil {
				return err
			}
			body, err := p.Raw(cmd.Context(), "GET", b+"/pending", nil)
			if err != nil {
				return err
			}
			var rows []map[string]any
			if err := protocol.DecodeData(body, &rows); err != nil {
				return err
			}
			return a.render(mapsTable(rows, "key", "value", "pending", "delete"))
		},
	}
	addScope(pending)

	var timeframe, cf string
	rrd := &cobra.Command{
		Use:     "rrddata <vmid>",
		Short:   fmt.Sprintf("Show %s metrics (RRD time series)", spec.label),
		Example: fmt.Sprintf("  pc %s rrddata 100 --timeframe day", spec.noun),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, _, b, err := resolveGuestBase(cmd, a, spec, args[0], node, remote)
			if err != nil {
				return err
			}
			q := map[string][]string{"timeframe": {timeframe}, "cf": {cf}}
			body, err := p.Raw(cmd.Context(), "GET", b+"/rrddata", q)
			if err != nil {
				return err
			}
			var rows []map[string]any
			if err := protocol.DecodeData(body, &rows); err != nil {
				return err
			}
			return a.render(mapsTable(rows, "time", "cpu", "maxcpu", "mem", "maxmem", "netin", "netout", "diskread", "diskwrite"))
		},
	}
	addScope(rrd)
	rrd.Flags().StringVar(&timeframe, "timeframe", "hour", "hour|day|week|month|year")
	rrd.Flags().StringVar(&cf, "cf", "AVERAGE", "consolidation: AVERAGE|MAX")

	firewall := &cobra.Command{Use: "firewall", Aliases: []string{"fw"}, Short: fmt.Sprintf("Inspect %s firewall config", spec.label)}
	fwRules := &cobra.Command{
		Use: "rules <vmid>", Short: "List firewall rules", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, _, b, err := resolveGuestBase(cmd, a, spec, args[0], node, remote)
			if err != nil {
				return err
			}
			body, err := p.Raw(cmd.Context(), "GET", b+"/firewall/rules", nil)
			if err != nil {
				return err
			}
			var rows []map[string]any
			if err := protocol.DecodeData(body, &rows); err != nil {
				return err
			}
			return a.render(mapsTable(rows, "pos", "type", "action", "source", "dest", "proto", "dport", "enable"))
		},
	}
	fwOpts := &cobra.Command{
		Use: "options <vmid>", Short: "Show firewall options", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, g, b, err := resolveGuestBase(cmd, a, spec, args[0], node, remote)
			if err != nil {
				return err
			}
			body, err := p.Raw(cmd.Context(), "GET", b+"/firewall/options", nil)
			if err != nil {
				return err
			}
			var opts map[string]any
			if err := protocol.DecodeData(body, &opts); err != nil {
				return err
			}
			return a.render(guestConfigTable(g, opts))
		},
	}
	firewall.PersistentFlags().StringVar(&node, "node", "", "node hosting the guest")
	firewall.PersistentFlags().StringVar(&remote, "remote", "", "PDM remote that hosts the guest")
	firewall.AddCommand(fwRules, fwOpts)

	return []*cobra.Command{status, pending, rrd, firewall}
}
