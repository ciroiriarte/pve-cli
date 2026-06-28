package cli

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

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
	// guestKind drives the unified `pc guest` verbs. Its empty kind means "any":
	// the vmid is resolved against /cluster/resources to learn VM-vs-CT (Proxmox
	// uses a single id namespace, so a vmid is unambiguous), then routed to the
	// right API path. No --type flag is needed.
	guestKind = guestSpec{noun: "guest", kind: "", label: "guest"}
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
		newGuestPowerCmd(a, spec, "suspend", "Suspend (pause)", true),
		newGuestPowerCmd(a, spec, "resume", "Resume", false),
		newGuestCreateCmd(a, spec),
		newGuestCloneCmd(a, spec),
		newGuestDeleteCmd(a, spec),
		newGuestMigrateCmd(a, spec),
		newGuestConfigCmd(a, spec),
		newGuestSnapshotCmd(a, spec),
		newGuestTagCmd(a, spec),
		newGuestConsoleCmd(a, spec),
	)
	cmd.AddCommand(newGuestMonitorCmds(a, spec)...)
	cmd.AddCommand(newGuestExtraCmds(a, spec)...)
	return cmd
}

func newGuestListCmd(a *app, spec guestSpec) *cobra.Command {
	var node, status string
	var tags []string
	cmd := &cobra.Command{
		Use:     "list",
		Short:   fmt.Sprintf("List %ss", spec.label),
		Example: fmt.Sprintf("  pc %s list\n  pc %s list --status running -o json\n  pc %s list --tag prod", spec.noun, spec.noun, spec.noun),
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
			guests = filterGuestsByTags(guests, tags)
			sort.Slice(guests, func(i, j int) bool { return guests[i].VMID < guests[j].VMID })
			return a.render(guestsTable(guests))
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "filter by node")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (running|stopped)")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "filter by tag (comma-separated or repeatable; matches any)")
	_ = cmd.RegisterFlagCompletionFunc("tag", completeTagNames(a))
	return cmd
}

func newGuestShowCmd(a *app, spec guestSpec) *cobra.Command {
	var node, remote string
	cmd := &cobra.Command{
		Use:   "show <vmid>",
		Short: fmt.Sprintf("Show a %s: config plus live status", spec.label),
		Long: fmt.Sprintf("Shows a complete snapshot of the %s — its configuration merged with live\n"+
			"runtime status (status, uptime, cpu/mem). Use `config` for the raw config\n"+
			"(and `--set` to modify it), or `status` for runtime fields only.", spec.label),
		Example: fmt.Sprintf("  pc %s show 100", spec.noun),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			g, err := resolveGuest(cmd.Context(), p, spec, args[0], node, remote)
			if err != nil {
				return err
			}
			cfg, err := p.GuestConfig(cmd.Context(), g)
			if err != nil {
				return err
			}
			// Enrich with live status so `show` is a full snapshot — distinct from
			// `config` (raw config) and `status` (runtime only). Best-effort: a
			// status hiccup must not break showing the config. Config keys win on
			// conflict (they're the authoritative definition).
			if b, berr := guestBase(p, g); berr == nil {
				statusPath := b + "/status/current"
				if p.Name() == "pdm" {
					statusPath = b + "/status"
				}
				if body, serr := p.Raw(cmd.Context(), "GET", statusPath, nil); serr == nil {
					var st map[string]any
					if protocol.DecodeData(body, &st) == nil {
						for k, v := range st {
							if _, ok := cfg[k]; !ok {
								cfg[k] = v
							}
						}
					}
				}
			}
			return a.render(guestConfigTable(g, cfg))
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "node hosting the guest (skips auto-resolution)")
	cmd.Flags().StringVar(&remote, "remote", "", "PDM remote that hosts the guest (disambiguates a shared vmid)")
	return cmd
}

func newGuestPowerCmd(a *app, spec guestSpec, action, short string, destructive bool) *cobra.Command {
	var node, remote string
	var wait, noWait bool
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
			g, err := resolveGuest(cmd.Context(), p, spec, args[0], node, remote)
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
			return finishTask(cmd.Context(), a, p, h, wait && !noWait, timeout,
				fmt.Sprintf("%s %s %d (%s) on %s", action, spec.label, g.VMID, g.Name, g.Node))
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "node hosting the guest (skips auto-resolution)")
	cmd.Flags().StringVar(&remote, "remote", "", "PDM remote that hosts the guest (disambiguates a shared vmid)")
	cmd.Flags().BoolVar(&wait, "wait", true, "wait for the task to finish (default)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return immediately with the task id")
	cmd.Flags().IntVar(&timeout, "wait-timeout", 0, "seconds to wait for the task (0 = no limit)")
	cmd.MarkFlagsMutuallyExclusive("wait", "no-wait")
	return cmd
}

// resolveGuest finds a guest by vmid, honoring an explicit --node override and
// an optional --remote (PDM) to disambiguate a vmid present on several remotes.
func resolveGuest(ctx context.Context, p provider.Provider, spec guestSpec, idArg, node string, remoteOpt ...string) (domain.Guest, error) {
	remote := ""
	if len(remoteOpt) > 0 {
		remote = remoteOpt[0]
	}
	vmid, err := strconv.Atoi(idArg)
	if err != nil {
		// A non-numeric argument is a guest name — look it up.
		return resolveGuestByName(ctx, p, spec, idArg, node, remote)
	}
	// Typed commands (vm/ct) with an explicit --node skip auto-resolution. The
	// unified `guest` command has no kind, so it must resolve to learn whether
	// the vmid is a VM or CT — it falls through and applies --node as an override.
	if node != "" && spec.kind != "" {
		// Explicit node skips auto-resolution; carry remote too so PDM proxied
		// calls (which are remote-scoped, not node-scoped) still work.
		return domain.Guest{VMID: vmid, Kind: spec.kind, Node: node, Remote: remote}, nil
	}
	if remote != "" {
		gr, ok := p.(interface {
			ResolveGuestInRemote(context.Context, int, string) (domain.Guest, error)
		})
		if !ok {
			return domain.Guest{}, fmt.Errorf("--remote is only supported with the PDM provider (current provider: %s); set provider: pdm or drop --remote", p.Name())
		}
		g, err := gr.ResolveGuestInRemote(ctx, vmid, remote)
		if err != nil {
			return domain.Guest{}, err
		}
		if err := enforceKind(spec, g); err != nil {
			return domain.Guest{}, err
		}
		if node != "" {
			g.Node = node
		}
		return g, nil
	}
	g, err := p.ResolveGuest(ctx, vmid)
	if err != nil {
		return domain.Guest{}, err
	}
	if err := enforceKind(spec, g); err != nil {
		return domain.Guest{}, err
	}
	if node != "" {
		g.Node = node
	}
	return g, nil
}

// enforceKind rejects a guest whose kind doesn't match a typed spec. The unified
// `guest` spec has an empty kind and accepts whatever was resolved.
func enforceKind(spec guestSpec, g domain.Guest) error {
	if spec.kind != "" && g.Kind != spec.kind {
		return fmt.Errorf("%d is a %s, not a %s", g.VMID, g.Kind, spec.label)
	}
	return nil
}

// resolveGuestByName resolves a guest from its name when the CLI argument isn't
// a vmid. Names aren't unique in Proxmox, so multiple matches are a conflict that
// points back at the vmid. The kind (vm/ct), --node, and --remote (PDM) scope
// the search.
func resolveGuestByName(ctx context.Context, p provider.Provider, spec guestSpec, name, node, remote string) (domain.Guest, error) {
	guests, err := p.ListGuests(ctx, provider.GuestFilter{Kind: spec.kind, Node: node})
	if err != nil {
		return domain.Guest{}, err
	}
	var matches []domain.Guest
	for _, g := range guests {
		if g.Name == name && (remote == "" || strings.EqualFold(g.Remote, remote)) {
			matches = append(matches, g)
		}
	}
	switch len(matches) {
	case 0:
		scope := ""
		switch {
		case remote != "":
			scope = fmt.Sprintf(" on remote %q", remote)
		case node != "":
			scope = fmt.Sprintf(" on node %q", node)
		}
		return domain.Guest{}, &protocol.APIError{Kind: protocol.KindNotFound,
			Message: fmt.Sprintf("no %s named %q found%s", spec.label, name, scope)}
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, strconv.Itoa(m.VMID))
		}
		return domain.Guest{}, &protocol.APIError{Kind: protocol.KindConflict,
			Message: fmt.Sprintf("%d guests named %q (vmids: %s); use the vmid to disambiguate",
				len(matches), name, strings.Join(ids, ", "))}
	}
}

// firstOnlineNode returns the name of an online cluster node. It lets read-only,
// cluster-wide commands (Ceph info, shared-storage listings) make --node
// optional: the data is identical from any node, so the user shouldn't have to
// name one. Node-local resources still want an explicit --node.
func firstOnlineNode(ctx context.Context, p provider.Provider) (string, error) {
	nodes, err := p.ListNodes(ctx)
	if err != nil {
		return "", err
	}
	for _, n := range nodes {
		if n.Name != "" && (n.Status == "" || n.Status == "online") {
			return n.Name, nil
		}
	}
	return "", fmt.Errorf("could not find an online node to route this request; pass --node explicitly")
}

// nodeOrAuto returns node when set, otherwise an online cluster node.
func nodeOrAuto(ctx context.Context, p provider.Provider, node string) (string, error) {
	if node != "" {
		return node, nil
	}
	return firstOnlineNode(ctx, p)
}

func guestsTable(guests []domain.Guest) output.Tabular {
	// Include the remote column only when present (PDM) and the tags column only
	// when some guest is tagged; keeps the common PVE output clean.
	hasRemote, hasTags := false, false
	for _, g := range guests {
		if g.Remote != "" {
			hasRemote = true
		}
		if strings.TrimSpace(g.Tags) != "" {
			hasTags = true
		}
	}
	cols := []string{"vmid", "name", "kind"}
	if hasRemote {
		cols = append(cols, "remote")
	}
	cols = append(cols, "node", "status", "maxmem", "uptime")
	if hasTags {
		cols = append(cols, "tags")
	}
	t := output.Tabular{Columns: cols, Raw: guests}
	for _, g := range guests {
		row := []string{strconv.Itoa(g.VMID), g.Name, string(g.Kind)}
		if hasRemote {
			row = append(row, g.Remote)
		}
		row = append(row, g.Node, g.Status, humanBytes(g.MaxMem), humanUptime(g.Uptime))
		if hasTags {
			row = append(row, joinTags(splitTags(g.Tags)))
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

func guestConfigTable(g domain.Guest, cfg map[string]any) output.Tabular {
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([][]string, 0, len(cfg))
	for _, k := range keys {
		rows = append(rows, []string{k, fmt.Sprintf("%v", cfg[k])})
	}
	// json/yaml emit the native config object (e.g. {"cores":4,...}) so callers
	// can script `jq '.cores'`; the human key/value table is built from Rows.
	// (The [][]string Rows still render the sorted table for -o table/value/csv.)
	return output.Tabular{Columns: []string{"key", "value"}, Rows: rows, Raw: cfg}
}

// finishTask either waits for a task (with optional spinner) or prints its id.
func finishTask(ctx context.Context, a *app, p provider.Provider, h protocol.TaskHandle, wait bool, timeoutSecs int, label string) error {
	if !wait || a.format == "json" || a.format == "yaml" {
		return a.render(taskHandleTable(h))
	}
	spinner := isTTY()
	// task.Wait returns a KindTaskFailed error when the task ends non-OK, so a
	// success checkmark is only ever reached on genuine success.
	if _, err := task.Wait(ctx, p.TaskStatus, h, task.WaitOptions{
		Timeout: secs(timeoutSecs),
		Spinner: spinner,
		Out:     stderrWriter(),
		Label:   label,
	}); err != nil {
		return err
	}
	if spinner {
		fmt.Fprintf(stderrWriter(), "✔ %s\n", label)
	}
	return nil
}
