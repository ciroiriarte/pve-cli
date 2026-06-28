package cli

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/domain"
	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// snapshotRow is one snapshot enriched with its owning guest's identity. It is
// the native JSON object emitted by the cluster-wide snapshot commands — keep it
// a flat typed object (decision #1) so callers can `jq '.[].age_seconds'`.
type snapshotRow struct {
	VMID        int    `json:"vmid"`
	Guest       string `json:"guest,omitempty"` // guest name
	Kind        string `json:"kind"`            // vm | ct
	Node        string `json:"node,omitempty"`
	Remote      string `json:"remote,omitempty"` // PDM only
	Snapshot    string `json:"snapshot"`         // snapshot name
	Snaptime    int64  `json:"snaptime,omitempty"`
	AgeSeconds  int64  `json:"age_seconds,omitempty"`
	Parent      string `json:"parent,omitempty"`
	Description string `json:"description,omitempty"`
}

// newSnapshotCmd is the cluster-wide snapshot group: an inventory of every
// guest's snapshots in one shot, plus age-based bulk pruning. The per-guest
// create/list/delete/rollback verbs live under `pc vm|ct|guest snapshot`; these
// commands aggregate across the whole cluster.
func newSnapshotCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Cluster-wide snapshot inventory and cleanup",
		Long: "Audit and prune snapshots across every guest in the cluster.\n\n" +
			"Per-guest snapshot management (create/rollback) lives under\n" +
			"`pc vm snapshot`, `pc ct snapshot`, and `pc guest snapshot`.",
	}
	cmd.AddCommand(newSnapshotListCmd(a), newSnapshotPruneCmd(a))
	return cmd
}

func newSnapshotListCmd(a *app) *cobra.Command {
	var node, remote, vmid string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List snapshots across the whole cluster",
		Args:  cobra.NoArgs,
		Example: "  pc snapshot list\n" +
			"  pc snapshot list --node pve-01\n" +
			"  pc snapshot list --vmid 100,101 -o json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			rows, err := collectSnapshots(cmd.Context(), p, snapshotFilter{node: node, remote: remote, vmid: vmid})
			if err != nil {
				return err
			}
			return a.render(snapshotTable(rows, hasRemoteRows(rows)))
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "limit to guests on this node")
	cmd.Flags().StringVar(&remote, "remote", "", "limit to this PDM remote")
	cmd.Flags().StringVar(&vmid, "vmid", "", "limit to these guest ids (comma-separated)")
	return cmd
}

func newSnapshotPruneCmd(a *app) *cobra.Command {
	var node, remote, vmid, olderThan, before string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete snapshots older than a cutoff, cluster-wide",
		Args:  cobra.NoArgs,
		Long: "Remove snapshots older than a cutoff across the cluster.\n\n" +
			"The matched snapshots are always printed first. Without --dry-run the\n" +
			"deletion is confirm-gated (prompts unless -y/--yes); --dry-run previews\n" +
			"only and never deletes.",
		Example: "  pc snapshot prune --older-than 30d\n" +
			"  pc snapshot prune --before 2026-01-01 --dry-run\n" +
			"  pc snapshot prune --older-than 12w --vmid 100 --yes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cutoff, err := pruneCutoff(olderThan, before)
			if err != nil {
				return err
			}
			p, err := a.Provider()
			if err != nil {
				return err
			}
			all, err := collectSnapshots(cmd.Context(), p, snapshotFilter{node: node, remote: remote, vmid: vmid})
			if err != nil {
				return err
			}
			cutoffUnix := cutoff.Unix()
			var stale []snapshotRow
			for _, r := range all {
				// Snapshots with no recorded time (shouldn't happen for real ones)
				// are left alone — never delete something we can't date.
				if r.Snaptime > 0 && r.Snaptime < cutoffUnix {
					stale = append(stale, r)
				}
			}
			if len(stale) == 0 {
				fmt.Fprintf(stderrWriter(), "no snapshots older than %s\n", cutoff.Format(time.RFC3339))
				return nil
			}
			// Always show what matched before touching anything.
			if err := a.render(snapshotTable(stale, hasRemoteRows(stale))); err != nil {
				return err
			}
			if dryRun {
				fmt.Fprintf(stderrWriter(), "dry-run: %d snapshot(s) would be deleted (older than %s)\n",
					len(stale), cutoff.Format(time.RFC3339))
				return nil
			}
			if err := confirm(a, fmt.Sprintf("permanently delete %d snapshot(s) older than %s?",
				len(stale), cutoff.Format(time.RFC3339))); err != nil {
				return err
			}
			var failed int
			for _, r := range stale {
				g := domain.Guest{VMID: r.VMID, Kind: kindFromLabel(r.Kind), Node: r.Node, Remote: r.Remote}
				base, err := guestBase(p, g)
				if err != nil {
					fmt.Fprintf(stderrWriter(), "skip %s of %s %d: %v\n", r.Snapshot, r.Kind, r.VMID, err)
					failed++
					continue
				}
				label := fmt.Sprintf("delete snapshot %s of %s %d", r.Snapshot, r.Kind, r.VMID)
				if err := rawMutate(cmd.Context(), a, p, "DELETE", base+"/snapshot/"+url.PathEscape(r.Snapshot), nil, label, true, 0); err != nil {
					fmt.Fprintf(stderrWriter(), "%s: %v\n", label, err)
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d snapshot deletion(s) failed", failed, len(stale))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&olderThan, "older-than", "", "delete snapshots older than this age (e.g. 30d, 12w, 48h)")
	cmd.Flags().StringVar(&before, "before", "", "delete snapshots taken before this date (YYYY-MM-DD or RFC3339)")
	cmd.Flags().StringVar(&node, "node", "", "limit to guests on this node")
	cmd.Flags().StringVar(&remote, "remote", "", "limit to this PDM remote")
	cmd.Flags().StringVar(&vmid, "vmid", "", "limit to these guest ids (comma-separated)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview only; never delete")
	return cmd
}

// snapshotFilter narrows the cluster-wide snapshot walk.
type snapshotFilter struct {
	node   string
	remote string
	vmid   string // comma-separated ids; "" = all
}

// collectSnapshots walks every guest matched by the filter and aggregates their
// snapshots into one flat list. The synthetic "current" pseudo-snapshot Proxmox
// returns is skipped — it isn't a real snapshot. Per-guest snapshot fetch errors
// are reported to stderr but don't abort the whole inventory.
func collectSnapshots(ctx context.Context, p provider.Provider, f snapshotFilter) ([]snapshotRow, error) {
	var idFilter map[int]bool
	if f.vmid != "" {
		idFilter = map[int]bool{}
		for _, part := range strings.Split(f.vmid, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid --vmid %q: not a number", part)
			}
			idFilter[n] = true
		}
	}

	guests, err := p.ListGuests(ctx, provider.GuestFilter{Node: f.node})
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	var rows []snapshotRow
	for _, g := range guests {
		if idFilter != nil && !idFilter[g.VMID] {
			continue
		}
		if f.remote != "" && !strings.EqualFold(g.Remote, f.remote) {
			continue
		}
		base, err := guestBase(p, g)
		if err != nil {
			// e.g. a PDM guest with no remote when none was requested; skip it.
			continue
		}
		body, err := p.Raw(ctx, "GET", base+"/snapshot", nil)
		if err != nil {
			fmt.Fprintf(stderrWriter(), "snapshots of %s %d: %v\n", labelFromKind(g.Kind), g.VMID, err)
			continue
		}
		var snaps []map[string]any
		if err := protocol.DecodeData(body, &snaps); err != nil {
			fmt.Fprintf(stderrWriter(), "snapshots of %s %d: %v\n", labelFromKind(g.Kind), g.VMID, err)
			continue
		}
		for _, s := range snaps {
			name := fmt.Sprintf("%v", s["name"])
			if name == "current" { // Proxmox's synthetic "you are here" entry
				continue
			}
			row := snapshotRow{
				VMID:        g.VMID,
				Guest:       g.Name,
				Kind:        labelFromKind(g.Kind),
				Node:        g.Node,
				Remote:      g.Remote,
				Snapshot:    name,
				Description: strings.TrimSpace(asString(s["description"])),
				Parent:      asString(s["parent"]),
			}
			if t, ok := asInt64(s["snaptime"]); ok && t > 0 {
				row.Snaptime = t
				if now > t {
					row.AgeSeconds = now - t
				}
			}
			rows = append(rows, row)
		}
	}
	// Stable, predictable ordering: by guest, then oldest snapshot first.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].VMID != rows[j].VMID {
			return rows[i].VMID < rows[j].VMID
		}
		return rows[i].Snaptime < rows[j].Snaptime
	})
	return rows, nil
}

// snapshotTable renders snapshot rows for the human view. Raw stays the native
// typed slice so json/yaml emit objects, not the string projection.
func snapshotTable(rows []snapshotRow, withRemote bool) output.Tabular {
	cols := []string{"vmid", "guest", "kind", "node"}
	if withRemote {
		cols = append(cols, "remote")
	}
	cols = append(cols, "snapshot", "age", "description")
	t := output.Tabular{Columns: cols, Raw: rows}
	for _, r := range rows {
		cells := []string{strconv.Itoa(r.VMID), r.Guest, r.Kind, r.Node}
		if withRemote {
			cells = append(cells, r.Remote)
		}
		cells = append(cells, r.Snapshot, humanizeAge(r.AgeSeconds), r.Description)
		t.Rows = append(t.Rows, cells)
	}
	return t
}

func hasRemoteRows(rows []snapshotRow) bool {
	for _, r := range rows {
		if r.Remote != "" {
			return true
		}
	}
	return false
}

// pruneCutoff resolves the prune boundary from exactly one of --older-than or
// --before. Snapshots taken before the returned time are stale.
func pruneCutoff(olderThan, before string) (time.Time, error) {
	switch {
	case olderThan != "" && before != "":
		return time.Time{}, fmt.Errorf("pass only one of --older-than or --before")
	case olderThan != "":
		d, err := parseAge(olderThan)
		if err != nil {
			return time.Time{}, err
		}
		return time.Now().Add(-d), nil
	case before != "":
		return parseDate(before)
	default:
		return time.Time{}, fmt.Errorf("specify a cutoff: --older-than <age> or --before <date>")
	}
}

// parseAge parses a human age such as "30d", "12w", "48h", "90m" or any Go
// duration. Days and weeks extend time.ParseDuration, which stops at hours.
func parseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty age")
	}
	last := s[len(s)-1]
	if last == 'd' || last == 'w' {
		n, err := strconv.ParseFloat(s[:len(s)-1], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid age %q", s)
		}
		unit := 24 * time.Hour
		if last == 'w' {
			unit = 7 * 24 * time.Hour
		}
		return time.Duration(n * float64(unit)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid age %q (use e.g. 30d, 12w, 48h)", s)
	}
	return d, nil
}

// parseDate accepts a bare date (YYYY-MM-DD, interpreted in local time) or a
// full RFC3339 timestamp.
func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid date %q (use YYYY-MM-DD or RFC3339)", s)
}

// humanizeAge renders an age in seconds as a compact human string.
func humanizeAge(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	switch {
	case sec >= 86400:
		return fmt.Sprintf("%dd", sec/86400)
	case sec >= 3600:
		return fmt.Sprintf("%dh", sec/3600)
	case sec >= 60:
		return fmt.Sprintf("%dm", sec/60)
	default:
		return fmt.Sprintf("%ds", sec)
	}
}

// labelFromKind maps the API kind ("qemu"/"lxc") to the friendly "vm"/"ct".
func labelFromKind(k domain.GuestKind) string {
	switch k {
	case domain.KindVM:
		return "vm"
	case domain.KindCT:
		return "ct"
	default:
		return string(k)
	}
}

// kindFromLabel is the inverse of labelFromKind, for rebuilding a guest from a row.
func kindFromLabel(label string) domain.GuestKind {
	switch label {
	case "vm":
		return domain.KindVM
	case "ct":
		return domain.KindCT
	default:
		return domain.GuestKind(label)
	}
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// asInt64 coerces a JSON number (float64) or numeric string to int64.
func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return i, true
		}
	}
	return 0, false
}
