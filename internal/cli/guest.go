package cli

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/domain"
	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
	"github.com/ciroiriarte/pve-cli/internal/task"
)

// guestSpec parameterizes the shared vm/ct command tree.
type guestSpec struct {
	noun    string
	aliases []string
	kind    domain.GuestKind
	label   string // human label, e.g. "VM"
}

var (
	vmKind = guestSpec{noun: "vm", kind: domain.KindVM, label: "VM"}
	ctKind = guestSpec{noun: "ct", aliases: []string{"container"}, kind: domain.KindCT, label: "container"}
)

func newGuestCmd(a *app, spec guestSpec) *cobra.Command {
	cmd := &cobra.Command{
		Use:     spec.noun,
		Aliases: spec.aliases,
		Short:   fmt.Sprintf("Manage %ss", spec.label),
	}
	cmd.AddCommand(
		newGuestListCmd(a, spec),
		newGuestShowCmd(a, spec),
		newGuestPowerCmd(a, spec, "start", "Start", false),
		newGuestPowerCmd(a, spec, "stop", "Stop (hard)", true),
		newGuestPowerCmd(a, spec, "shutdown", "Shut down (graceful)", true),
		newGuestPowerCmd(a, spec, "reboot", "Reboot", true),
	)
	return cmd
}

func newGuestListCmd(a *app, spec guestSpec) *cobra.Command {
	var node, status string
	cmd := &cobra.Command{
		Use:     "list",
		Short:   fmt.Sprintf("List %ss", spec.label),
		Example: fmt.Sprintf("  pc %s list\n  pc %s list --status running -o json", spec.noun, spec.noun),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			guests, err := p.ListGuests(cmd.Context(), provider.GuestFilter{Kind: spec.kind, Node: node, Status: status})
			if err != nil {
				return err
			}
			sort.Slice(guests, func(i, j int) bool { return guests[i].VMID < guests[j].VMID })
			return a.render(guestsTable(guests))
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "filter by node")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (running|stopped)")
	return cmd
}

func newGuestShowCmd(a *app, spec guestSpec) *cobra.Command {
	var node string
	cmd := &cobra.Command{
		Use:     "show <vmid>",
		Short:   fmt.Sprintf("Show a %s and its config", spec.label),
		Example: fmt.Sprintf("  pc %s show 100", spec.noun),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			g, err := resolveGuest(cmd.Context(), p, spec, args[0], node)
			if err != nil {
				return err
			}
			cfg, err := p.GuestConfig(cmd.Context(), g)
			if err != nil {
				return err
			}
			return a.render(guestConfigTable(g, cfg))
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "node hosting the guest (skips auto-resolution)")
	return cmd
}

func newGuestPowerCmd(a *app, spec guestSpec, action, short string, destructive bool) *cobra.Command {
	var node string
	var noWait bool
	var timeout int
	cmd := &cobra.Command{
		Use:     action + " <vmid>",
		Short:   fmt.Sprintf("%s a %s", short, spec.label),
		Example: fmt.Sprintf("  pc %s %s 100", spec.noun, action),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			g, err := resolveGuest(cmd.Context(), p, spec, args[0], node)
			if err != nil {
				return err
			}
			if destructive {
				if err := confirm(a, fmt.Sprintf("%s %s %d (%s) on node %s?", action, spec.label, g.VMID, g.Name, g.Node)); err != nil {
					return err
				}
			}
			h, err := p.GuestPower(cmd.Context(), g, action)
			if err != nil {
				return err
			}
			return finishTask(cmd.Context(), a, p, h, !noWait, timeout,
				fmt.Sprintf("%s %s %d (%s) on %s", action, spec.label, g.VMID, g.Name, g.Node))
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "node hosting the guest (skips auto-resolution)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return immediately with the task id")
	cmd.Flags().IntVar(&timeout, "wait-timeout", 0, "seconds to wait for the task (0 = no limit)")
	return cmd
}

// resolveGuest finds a guest by vmid, honoring an explicit --node override.
func resolveGuest(ctx context.Context, p provider.Provider, spec guestSpec, idArg, node string) (domain.Guest, error) {
	vmid, err := strconv.Atoi(idArg)
	if err != nil {
		return domain.Guest{}, fmt.Errorf("invalid vmid %q: must be a number", idArg)
	}
	if node != "" {
		return domain.Guest{VMID: vmid, Kind: spec.kind, Node: node}, nil
	}
	g, err := p.ResolveGuest(ctx, vmid)
	if err != nil {
		return domain.Guest{}, err
	}
	if g.Kind != spec.kind {
		return domain.Guest{}, fmt.Errorf("%d is a %s, not a %s", vmid, g.Kind, spec.label)
	}
	return g, nil
}

func guestsTable(guests []domain.Guest) output.Tabular {
	t := output.Tabular{
		Columns: []string{"vmid", "name", "kind", "node", "status", "maxmem", "uptime"},
		Raw:     guests,
	}
	for _, g := range guests {
		t.Rows = append(t.Rows, []string{
			strconv.Itoa(g.VMID),
			g.Name,
			string(g.Kind),
			g.Node,
			g.Status,
			humanBytes(g.MaxMem),
			humanUptime(g.Uptime),
		})
	}
	return t
}

func guestConfigTable(g domain.Guest, cfg map[string]any) output.Tabular {
	type kv struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
	}
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([][]string, 0, len(cfg))
	raw := make([]kv, 0, len(cfg))
	for _, k := range keys {
		rows = append(rows, []string{k, fmt.Sprintf("%v", cfg[k])})
		raw = append(raw, kv{Key: k, Value: cfg[k]})
	}
	return output.Tabular{Columns: []string{"key", "value"}, Rows: rows, Raw: raw}
}

// finishTask either waits for a task (with optional spinner) or prints its id.
func finishTask(ctx context.Context, a *app, p provider.Provider, h protocol.TaskHandle, wait bool, timeoutSecs int, label string) error {
	if !wait || a.format == "json" || a.format == "yaml" {
		return a.render(taskHandleTable(h))
	}
	spinner := isTTY()
	st, err := task.Wait(ctx, p.TaskStatus, h, task.WaitOptions{
		Timeout: secs(timeoutSecs),
		Spinner: spinner,
		Out:     stderrWriter(),
		Label:   label,
	})
	if err != nil {
		return err
	}
	if spinner {
		fmt.Fprintf(stderrWriter(), "✔ %s\n", label)
	}
	_ = st
	return nil
}
