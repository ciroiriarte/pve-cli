package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/domain"
	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// Tags in Proxmox are a single string on the guest config / cluster resources.
// The API accepts ';'- or ','-separated input and stores them ';'-separated; we
// split tolerantly (any of ';', ',', whitespace) and always write back ';'.

func splitTags(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ';' || r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
}

// normalizeTags trims, de-duplicates, and sorts a tag list so writes are
// deterministic (Proxmox sorts tags in its UI anyway).
func normalizeTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func joinTags(tags []string) string { return strings.Join(tags, ";") }

// addTags returns current ∪ extra, normalized.
func addTags(current, extra []string) []string {
	return normalizeTags(append(append([]string{}, current...), extra...))
}

// removeTags returns current with anything in drop removed, normalized.
func removeTags(current, drop []string) []string {
	gone := map[string]bool{}
	for _, d := range drop {
		gone[strings.TrimSpace(d)] = true
	}
	kept := make([]string, 0, len(current))
	for _, t := range current {
		if !gone[strings.TrimSpace(t)] {
			kept = append(kept, t)
		}
	}
	return normalizeTags(kept)
}

// guestHasAnyTag reports whether a guest carries any of the wanted tags.
func guestHasAnyTag(guestTags string, want []string) bool {
	if len(want) == 0 {
		return true
	}
	have := map[string]bool{}
	for _, t := range splitTags(guestTags) {
		have[t] = true
	}
	for _, w := range want {
		if have[strings.TrimSpace(w)] {
			return true
		}
	}
	return false
}

// filterGuestsByTags keeps guests carrying any of want (OR match); want empty =
// pass-through. Used by the `--tag` filter on every guest list.
func filterGuestsByTags(guests []domain.Guest, want []string) []domain.Guest {
	if len(want) == 0 {
		return guests
	}
	out := make([]domain.Guest, 0, len(guests))
	for _, g := range guests {
		if guestHasAnyTag(g.Tags, want) {
			out = append(out, g)
		}
	}
	return out
}

// writeGuestTags overwrites a guest's tags via the config endpoint. Tag writes
// are PVE-only (PDM has no config-write API), consistent with `config --set`.
func writeGuestTags(ctx context.Context, a *app, p provider.Provider, g domain.Guest, tags []string, label string) error {
	if err := ensureProvisionable(p, "tag"); err != nil {
		return err
	}
	path := fmt.Sprintf("/nodes/%s/%s/%d/config", g.Node, kindEndpoint(g.Kind), g.VMID)
	return rawMutate(ctx, a, p, "PUT", path, url.Values{"tags": {joinTags(tags)}}, label, true, 0)
}

// newGuestTagCmd is the per-guest tag group, wired under vm/ct/guest. add and rm
// are read-modify-write so a single tag can be changed without clobbering the
// rest; set overwrites (with no tags it clears).
func newGuestTagCmd(a *app, spec guestSpec) *cobra.Command {
	cmd := &cobra.Command{Use: "tag", Short: fmt.Sprintf("Manage %s tags", spec.label)}
	var node, remote string

	resolve := func(cmd *cobra.Command, idArg string) (provider.Provider, domain.Guest, []string, error) {
		p, err := a.Provider()
		if err != nil {
			return nil, domain.Guest{}, nil, err
		}
		g, err := resolveGuest(cmd.Context(), p, spec, idArg, node, remote)
		if err != nil {
			return nil, domain.Guest{}, nil, err
		}
		cfg, err := p.GuestConfig(cmd.Context(), g)
		if err != nil {
			return nil, domain.Guest{}, nil, err
		}
		return p, g, normalizeTags(splitTags(asString(cfg["tags"]))), nil
	}

	list := &cobra.Command{
		Use: "list <vmid>", Short: "List a guest's tags", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _, tags, err := resolve(cmd, args[0])
			if err != nil {
				return err
			}
			return a.render(tagListTable(tags))
		},
	}
	add := &cobra.Command{
		Use: "add <vmid> <tag>...", Short: "Add tags to a guest", Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, g, cur, err := resolve(cmd, args[0])
			if err != nil {
				return err
			}
			next := addTags(cur, args[1:])
			return writeGuestTags(cmd.Context(), a, p, g, next,
				fmt.Sprintf("tag %s %d: %s", spec.label, g.VMID, joinTags(next)))
		},
	}
	rm := &cobra.Command{
		Use: "rm <vmid> <tag>...", Aliases: []string{"remove"}, Short: "Remove tags from a guest", Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, g, cur, err := resolve(cmd, args[0])
			if err != nil {
				return err
			}
			next := removeTags(cur, args[1:])
			return writeGuestTags(cmd.Context(), a, p, g, next,
				fmt.Sprintf("tag %s %d: %s", spec.label, g.VMID, joinTags(next)))
		},
	}
	set := &cobra.Command{
		Use: "set <vmid> [tag]...", Short: "Replace a guest's tags (no tags clears them)", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, g, _, err := resolve(cmd, args[0])
			if err != nil {
				return err
			}
			next := normalizeTags(args[1:])
			return writeGuestTags(cmd.Context(), a, p, g, next,
				fmt.Sprintf("tag %s %d: %s", spec.label, g.VMID, joinTags(next)))
		},
	}
	cmd.PersistentFlags().StringVar(&node, "node", "", "node hosting the guest")
	cmd.PersistentFlags().StringVar(&remote, "remote", "", "PDM remote that hosts the guest")
	cmd.AddCommand(list, add, rm, set)
	return cmd
}

func tagListTable(tags []string) output.Tabular {
	t := output.Tabular{Columns: []string{"tag"}, Raw: tags}
	for _, tag := range tags {
		t.Rows = append(t.Rows, []string{tag})
	}
	return t
}

// --- cluster-wide tag group -------------------------------------------------

type tagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// tagExportEntry is one guest's tags in an export/import file.
type tagExportEntry struct {
	VMID int      `json:"vmid"`
	Name string   `json:"name,omitempty"`
	Kind string   `json:"kind,omitempty"`
	Node string   `json:"node,omitempty"`
	Tags []string `json:"tags"`
}

func newTagCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tag",
		Short: "Cluster-wide tag inventory, bulk tagging, and backup",
		Long: "Inventory, bulk-apply, and back up guest tags across the cluster.\n\n" +
			"Per-guest tag editing lives under `pc vm tag`, `pc ct tag`, and\n" +
			"`pc guest tag`.",
	}
	cmd.AddCommand(
		newTagInventoryCmd(a),
		newBulkTagCmd(a, "add"),
		newBulkTagCmd(a, "rm"),
		newTagExportCmd(a),
		newTagImportCmd(a),
	)
	return cmd
}

func newTagInventoryCmd(a *app) *cobra.Command {
	var node string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all tags in use across the cluster, with counts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			guests, err := p.ListGuests(cmd.Context(), provider.GuestFilter{Node: node})
			if err != nil {
				return err
			}
			counts := map[string]int{}
			for _, g := range guests {
				for _, t := range splitTags(g.Tags) {
					counts[t]++
				}
			}
			rows := make([]tagCount, 0, len(counts))
			for t, c := range counts {
				rows = append(rows, tagCount{Tag: t, Count: c})
			}
			// Most-used first, then alphabetical for stable ties.
			sort.Slice(rows, func(i, j int) bool {
				if rows[i].Count != rows[j].Count {
					return rows[i].Count > rows[j].Count
				}
				return rows[i].Tag < rows[j].Tag
			})
			t := output.Tabular{Columns: []string{"tag", "count"}, Raw: rows}
			for _, r := range rows {
				t.Rows = append(t.Rows, []string{r.Tag, strconv.Itoa(r.Count)})
			}
			return a.render(t)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "limit to guests on this node")
	return cmd
}

// newBulkTagCmd builds `tag add`/`tag rm` for cluster-wide bulk changes. A
// selector is required (--all, --vmid, --node, or --has-tag) so a bare invocation
// can't tag the whole cluster by accident. Affected guests are printed first;
// --dry-run previews only, otherwise the change is confirm-gated (-y/--yes).
func newBulkTagCmd(a *app, op string) *cobra.Command {
	var node, vmid, hasTag string
	var all, dryRun bool
	short := map[string]string{
		"add": "Bulk add tags to many guests",
		"rm":  "Bulk remove tags from many guests",
	}[op]
	cmd := &cobra.Command{
		Use:   op + " <tag>...",
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		Example: fmt.Sprintf("  pc tag %s prod --all --dry-run\n"+
			"  pc tag %s backup --vmid 100,101 --yes\n"+
			"  pc tag %s legacy --has-tag old --node pve-01 --yes", op, op, op),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !all && vmid == "" && node == "" && hasTag == "" {
				return fmt.Errorf("a selector is required: --all, --vmid, --node, or --has-tag")
			}
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensureProvisionable(p, "tag"); err != nil {
				return err
			}
			guests, err := p.ListGuests(cmd.Context(), provider.GuestFilter{Node: node})
			if err != nil {
				return err
			}
			idFilter, err := parseIDSet(vmid)
			if err != nil {
				return err
			}
			var hasFilter []string
			if hasTag != "" {
				hasFilter = splitTags(hasTag)
			}
			tags := args

			type change struct {
				g        domain.Guest
				from, to []string
			}
			var changes []change
			for _, g := range guests {
				if idFilter != nil && !idFilter[g.VMID] {
					continue
				}
				if hasFilter != nil && !guestHasAnyTag(g.Tags, hasFilter) {
					continue
				}
				cur := normalizeTags(splitTags(g.Tags))
				var next []string
				if op == "add" {
					next = addTags(cur, tags)
				} else {
					next = removeTags(cur, tags)
				}
				if joinTags(cur) == joinTags(next) {
					continue // no-op for this guest
				}
				changes = append(changes, change{g: g, from: cur, to: next})
			}
			if len(changes) == 0 {
				fmt.Fprintln(stderrWriter(), "no guests need changing")
				return nil
			}

			// Preview table (always shown before any write).
			pt := output.Tabular{Columns: []string{"vmid", "name", "kind", "node", "tags"}}
			previewRaw := make([]tagExportEntry, 0, len(changes))
			for _, c := range changes {
				pt.Rows = append(pt.Rows, []string{strconv.Itoa(c.g.VMID), c.g.Name, labelFromKind(c.g.Kind), c.g.Node, joinTags(c.to)})
				previewRaw = append(previewRaw, tagExportEntry{VMID: c.g.VMID, Name: c.g.Name, Kind: labelFromKind(c.g.Kind), Node: c.g.Node, Tags: c.to})
			}
			pt.Raw = previewRaw
			if err := a.render(pt); err != nil {
				return err
			}
			if dryRun {
				fmt.Fprintf(stderrWriter(), "dry-run: %d guest(s) would change\n", len(changes))
				return nil
			}
			if err := confirm(a, fmt.Sprintf("%s tag(s) %s on %d guest(s)?", op, joinTags(normalizeTags(tags)), len(changes))); err != nil {
				return err
			}
			var failed int
			for _, c := range changes {
				if err := writeGuestTags(cmd.Context(), a, p, c.g, c.to,
					fmt.Sprintf("tag %s %d", labelFromKind(c.g.Kind), c.g.VMID)); err != nil {
					fmt.Fprintf(stderrWriter(), "%s %d: %v\n", labelFromKind(c.g.Kind), c.g.VMID, err)
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d guest tag update(s) failed", failed, len(changes))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "apply to every guest")
	cmd.Flags().StringVar(&vmid, "vmid", "", "limit to these guest ids (comma-separated)")
	cmd.Flags().StringVar(&node, "node", "", "limit to guests on this node")
	cmd.Flags().StringVar(&hasTag, "has-tag", "", "limit to guests already carrying any of these tags (comma-separated)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview only; make no changes")
	return cmd
}

func newTagExportCmd(a *app) *cobra.Command {
	var node string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export every guest's tags (backup)",
		Args:  cobra.NoArgs,
		Example: "  pc tag export -o json > tags-backup.json\n" +
			"  pc tag export --node pve-01 -o json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			guests, err := p.ListGuests(cmd.Context(), provider.GuestFilter{Node: node})
			if err != nil {
				return err
			}
			sort.Slice(guests, func(i, j int) bool { return guests[i].VMID < guests[j].VMID })
			entries := make([]tagExportEntry, 0, len(guests))
			t := output.Tabular{Columns: []string{"vmid", "name", "kind", "node", "tags"}}
			for _, g := range guests {
				tags := normalizeTags(splitTags(g.Tags))
				entries = append(entries, tagExportEntry{VMID: g.VMID, Name: g.Name, Kind: labelFromKind(g.Kind), Node: g.Node, Tags: tags})
				t.Rows = append(t.Rows, []string{strconv.Itoa(g.VMID), g.Name, labelFromKind(g.Kind), g.Node, joinTags(tags)})
			}
			t.Raw = entries
			return a.render(t)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "limit to guests on this node")
	return cmd
}

func newTagImportCmd(a *app) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Restore guest tags from a `tag export` file",
		Long: "Reads a JSON file produced by `tag export` and overwrites each listed\n" +
			"guest's tags. Guests are re-resolved by vmid against the current cluster,\n" +
			"so a moved guest is still updated. PVE-only (PDM has no config-write API).",
		Args: cobra.ExactArgs(1),
		Example: "  pc tag import tags-backup.json --dry-run\n" +
			"  pc tag import tags-backup.json --yes",
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var entries []tagExportEntry
			if err := json.Unmarshal(raw, &entries); err != nil {
				return fmt.Errorf("parse %s: %w (expected the output of `tag export -o json`)", args[0], err)
			}
			if len(entries) == 0 {
				fmt.Fprintln(stderrWriter(), "nothing to import")
				return nil
			}
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensureProvisionable(p, "tag"); err != nil {
				return err
			}

			// Resolve each entry against the live cluster and compute changes.
			type change struct {
				g  domain.Guest
				to []string
			}
			var changes []change
			pt := output.Tabular{Columns: []string{"vmid", "node", "kind", "tags"}}
			for _, e := range entries {
				g, err := p.ResolveGuest(cmd.Context(), e.VMID)
				if err != nil {
					fmt.Fprintf(stderrWriter(), "skip vmid %d: %v\n", e.VMID, err)
					continue
				}
				want := normalizeTags(e.Tags)
				cur := normalizeTags(splitTags(g.Tags))
				if joinTags(cur) == joinTags(want) {
					continue
				}
				changes = append(changes, change{g: g, to: want})
				pt.Rows = append(pt.Rows, []string{strconv.Itoa(g.VMID), g.Node, labelFromKind(g.Kind), joinTags(want)})
			}
			if len(changes) == 0 {
				fmt.Fprintln(stderrWriter(), "all guests already match the file; nothing to do")
				return nil
			}
			if err := a.render(pt); err != nil {
				return err
			}
			if dryRun {
				fmt.Fprintf(stderrWriter(), "dry-run: %d guest(s) would be updated\n", len(changes))
				return nil
			}
			if err := confirm(a, fmt.Sprintf("overwrite tags on %d guest(s) from %s?", len(changes), args[0])); err != nil {
				return err
			}
			var failed int
			for _, c := range changes {
				if err := writeGuestTags(cmd.Context(), a, p, c.g, c.to,
					fmt.Sprintf("tag %s %d", labelFromKind(c.g.Kind), c.g.VMID)); err != nil {
					fmt.Fprintf(stderrWriter(), "%s %d: %v\n", labelFromKind(c.g.Kind), c.g.VMID, err)
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d guest tag update(s) failed", failed, len(changes))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview only; make no changes")
	return cmd
}

// parseIDSet parses a comma-separated vmid list into a set; "" returns nil (no
// filter). Shared by the bulk tag selectors.
func parseIDSet(csv string) (map[int]bool, error) {
	if strings.TrimSpace(csv) == "" {
		return nil, nil
	}
	set := map[int]bool{}
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid --vmid %q: not a number", part)
		}
		set[n] = true
	}
	return set, nil
}
