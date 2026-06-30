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

// guestTagsAndDigest reads a guest's current tags plus the config digest. The
// digest is sent back on write so a concurrent edit between read and write fails
// loudly (optimistic locking) instead of silently clobbering.
func guestTagsAndDigest(ctx context.Context, p provider.Provider, g domain.Guest) ([]string, string, error) {
	cfg, err := p.GuestConfig(ctx, g)
	if err != nil {
		return nil, "", err
	}
	return normalizeTags(splitTags(asString(cfg["tags"]))), asString(cfg["digest"]), nil
}

// writeGuestTags overwrites a guest's tags via the config endpoint, passing the
// config digest for optimistic locking when known. Tag writes are PVE-only —
// PDM provisioning/config-write is not supported (see ensureProvisionable).
func writeGuestTags(ctx context.Context, a *app, p provider.Provider, g domain.Guest, tags []string, digest, label string) error {
	if err := ensureProvisionable(p, "tag"); err != nil {
		return err
	}
	path := fmt.Sprintf("/nodes/%s/%s/%d/config", g.Node, kindEndpoint(g.Kind), g.VMID)
	params := url.Values{"tags": {joinTags(tags)}}
	if digest != "" {
		params.Set("digest", digest)
	}
	return rawMutate(ctx, a, p, "PUT", path, params, label, true, 0)
}

// completeTagNames offers existing cluster tag names for shell completion.
func completeTagNames(a *app) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		p, err := a.Provider()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		guests, err := p.ListGuests(cmd.Context(), provider.GuestFilter{})
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		seen := map[string]bool{}
		var out []string
		for _, g := range guests {
			for _, t := range splitTags(g.Tags) {
				if !seen[t] {
					seen[t] = true
					out = append(out, t)
				}
			}
		}
		sort.Strings(out)
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// newGuestTagCmd is the per-guest tag group, wired under vm/ct/guest. add and rm
// are read-modify-write so a single tag can be changed without clobbering the
// rest; set replaces; clear removes all.
func newGuestTagCmd(a *app, spec guestSpec) *cobra.Command {
	cmd := &cobra.Command{Use: "tag", Short: fmt.Sprintf("Manage %s tags", spec.label)}
	var node, remote string

	// resolveGuestOnly resolves the provider + guest without reading config. Tags
	// come from the cluster-resource view (populated on PVE and PDM alike), so a
	// read needs no per-guest config call — which also keeps `list` working on PDM
	// where the proxied config endpoint isn't reliably reachable.
	resolveGuestOnly := func(cmd *cobra.Command, idArg string) (provider.Provider, domain.Guest, error) {
		p, err := a.Provider()
		if err != nil {
			return nil, domain.Guest{}, err
		}
		g, err := resolveGuest(cmd.Context(), p, spec, idArg, node, remote)
		return p, g, err
	}
	// prepWrite resolves the guest and, gating PDM out first (fail fast instead of
	// attempting a config read PDM can't serve), reads the live tags + digest.
	prepWrite := func(cmd *cobra.Command, idArg string) (provider.Provider, domain.Guest, []string, string, error) {
		p, g, err := resolveGuestOnly(cmd, idArg)
		if err != nil {
			return nil, domain.Guest{}, nil, "", err
		}
		if err := ensureProvisionable(p, "tag"); err != nil {
			return nil, domain.Guest{}, nil, "", err
		}
		cur, digest, err := guestTagsAndDigest(cmd.Context(), p, g)
		return p, g, cur, digest, err
	}
	write := func(cmd *cobra.Command, p provider.Provider, g domain.Guest, next []string, digest string) error {
		return writeGuestTags(cmd.Context(), a, p, g, next, digest,
			fmt.Sprintf("tag %s %d: %s", spec.label, g.VMID, joinTags(next)))
	}

	list := &cobra.Command{
		Use: "list <vmid>", Short: "List a guest's tags", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, g, err := resolveGuestOnly(cmd, args[0])
			if err != nil {
				return err
			}
			return a.render(tagListTable(normalizeTags(splitTags(g.Tags))))
		},
	}
	add := &cobra.Command{
		Use: "add <vmid> <tag>...", Short: "Add tags to a guest", Args: cobra.MinimumNArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 { // first arg is the vmid, not a tag
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return completeTagNames(a)(cmd, args, tc)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			p, g, cur, digest, err := prepWrite(cmd, args[0])
			if err != nil {
				return err
			}
			return write(cmd, p, g, addTags(cur, args[1:]), digest)
		},
	}
	rm := &cobra.Command{
		Use: "rm <vmid> <tag>...", Aliases: []string{"remove"}, Short: "Remove tags from a guest", Args: cobra.MinimumNArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return completeTagNames(a)(cmd, args, tc)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			p, g, cur, digest, err := prepWrite(cmd, args[0])
			if err != nil {
				return err
			}
			return write(cmd, p, g, removeTags(cur, args[1:]), digest)
		},
	}
	set := &cobra.Command{
		Use: "set <vmid> [tag]...", Short: "Replace a guest's tags (use `clear` to remove all)", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, g, _, digest, err := prepWrite(cmd, args[0])
			if err != nil {
				return err
			}
			return write(cmd, p, g, normalizeTags(args[1:]), digest)
		},
	}
	clear := &cobra.Command{
		Use: "clear <vmid>", Short: "Remove all tags from a guest", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, g, _, digest, err := prepWrite(cmd, args[0])
			if err != nil {
				return err
			}
			return write(cmd, p, g, nil, digest)
		},
	}
	cmd.PersistentFlags().StringVar(&node, "node", "", "node hosting the guest")
	cmd.PersistentFlags().StringVar(&remote, "remote", "", "PDM remote that hosts the guest")
	cmd.AddCommand(list, add, rm, set, clear)
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
	Tag    string `json:"tag"`
	Count  int    `json:"count"`
	Guests []int  `json:"guests,omitempty"`
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
		newBulkTagCmd(a, "clear"),
		newTagRenameCmd(a),
		newTagExportCmd(a),
		newTagImportCmd(a),
		newTagColorCmd(a),
		newTagStyleCmd(a),
	)
	return cmd
}

func newTagInventoryCmd(a *app) *cobra.Command {
	var node string
	var showGuests bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all tags in use across the cluster, with counts",
		Args:  cobra.NoArgs,
		Example: "  pc tag list\n" +
			"  pc tag list --show-guests",
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
			members := map[string][]int{}
			for _, g := range guests {
				for _, t := range splitTags(g.Tags) {
					counts[t]++
					members[t] = append(members[t], g.VMID)
				}
			}
			rows := make([]tagCount, 0, len(counts))
			for t, c := range counts {
				row := tagCount{Tag: t, Count: c}
				if showGuests {
					sort.Ints(members[t])
					row.Guests = members[t]
				}
				rows = append(rows, row)
			}
			// Most-used first, then alphabetical for stable ties.
			sort.Slice(rows, func(i, j int) bool {
				if rows[i].Count != rows[j].Count {
					return rows[i].Count > rows[j].Count
				}
				return rows[i].Tag < rows[j].Tag
			})
			cols := []string{"tag", "count"}
			if showGuests {
				cols = append(cols, "guests")
			}
			t := output.Tabular{Columns: cols, Raw: rows}
			for _, r := range rows {
				cells := []string{r.Tag, strconv.Itoa(r.Count)}
				if showGuests {
					cells = append(cells, joinInts(r.Guests))
				}
				t.Rows = append(t.Rows, cells)
			}
			return a.render(t)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "limit to guests on this node")
	cmd.Flags().BoolVar(&showGuests, "show-guests", false, "include the vmids carrying each tag")
	return cmd
}

// newBulkTagCmd builds `tag add`/`tag rm`/`tag clear` for cluster-wide changes. A
// selector is required (--all, --vmid, --node, or --has-tag) so a bare invocation
// can't touch the whole cluster by accident. Affected guests are printed first;
// --dry-run previews only, otherwise the change is confirm-gated (-y/--yes).
func newBulkTagCmd(a *app, op string) *cobra.Command {
	var node string
	var vmid, hasTag []string
	var all, dryRun bool
	meta := map[string]struct{ short, use string }{
		"add":   {"Bulk add tags to many guests", "add <tag>..."},
		"rm":    {"Bulk remove tags from many guests", "rm <tag>..."},
		"clear": {"Bulk remove ALL tags from many guests", "clear"},
	}[op]
	minArgs := cobra.MinimumNArgs(1)
	if op == "clear" {
		minArgs = cobra.NoArgs
	}
	cmd := &cobra.Command{
		Use:   meta.use,
		Short: meta.short,
		Args:  minArgs,
		Example: fmt.Sprintf("  pc tag %s --all --dry-run\n"+
			"  pc tag %s --vmid 100,101 --yes\n"+
			"  pc tag %s --has-tag old --node pve-01 --yes", exampleArgs(op), exampleArgs(op), exampleArgs(op)),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
			if op == "clear" {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return completeTagNames(a)(cmd, args, tc)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !all && len(vmid) == 0 && node == "" && len(hasTag) == 0 {
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
			tags := normalizeTags(args)

			// Compute the proposed change per guest from the (fast) resource view
			// for the preview; the actual write re-reads live config + digest.
			plan := func(cur []string) []string {
				switch op {
				case "add":
					return addTags(cur, tags)
				case "rm":
					return removeTags(cur, tags)
				default: // clear
					return nil
				}
			}
			var targets []domain.Guest
			pt := output.Tabular{Columns: []string{"vmid", "name", "kind", "node", "tags"}}
			previewRaw := make([]tagExportEntry, 0)
			for _, g := range guests {
				if idFilter != nil && !idFilter[g.VMID] {
					continue
				}
				if len(hasTag) > 0 && !guestHasAnyTag(g.Tags, hasTag) {
					continue
				}
				cur := normalizeTags(splitTags(g.Tags))
				next := plan(cur)
				if joinTags(cur) == joinTags(next) {
					continue // no-op
				}
				targets = append(targets, g)
				pt.Rows = append(pt.Rows, []string{strconv.Itoa(g.VMID), g.Name, labelFromKind(g.Kind), g.Node, joinTags(next)})
				previewRaw = append(previewRaw, tagExportEntry{VMID: g.VMID, Name: g.Name, Kind: labelFromKind(g.Kind), Node: g.Node, Tags: next})
			}
			if len(targets) == 0 {
				fmt.Fprintln(stderrWriter(), "no guests need changing")
				return nil
			}
			pt.Raw = previewRaw
			if err := a.render(pt); err != nil {
				return err
			}
			if dryRun {
				fmt.Fprintf(stderrWriter(), "dry-run: %d guest(s) would change\n", len(targets))
				return nil
			}
			prompt := fmt.Sprintf("%s tag(s) %s on %d guest(s)?", op, joinTags(tags), len(targets))
			if op == "clear" {
				prompt = fmt.Sprintf("clear ALL tags on %d guest(s)?", len(targets))
			}
			if err := confirm(a, prompt); err != nil {
				return err
			}
			var failed int
			for _, g := range targets {
				cur, digest, err := guestTagsAndDigest(cmd.Context(), p, g)
				if err != nil {
					fmt.Fprintf(stderrWriter(), "%s %d: %v\n", labelFromKind(g.Kind), g.VMID, err)
					failed++
					continue
				}
				next := plan(cur)
				if joinTags(cur) == joinTags(next) {
					continue // changed away between preview and write
				}
				if err := writeGuestTags(cmd.Context(), a, p, g, next, digest,
					fmt.Sprintf("tag %s %d", labelFromKind(g.Kind), g.VMID)); err != nil {
					fmt.Fprintf(stderrWriter(), "%s %d: %v\n", labelFromKind(g.Kind), g.VMID, err)
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d guest tag update(s) failed", failed, len(targets))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "apply to every guest")
	cmd.Flags().StringSliceVar(&vmid, "vmid", nil, "limit to these guest ids (comma-separated or repeatable)")
	cmd.Flags().StringVar(&node, "node", "", "limit to guests on this node")
	cmd.Flags().StringSliceVar(&hasTag, "has-tag", nil, "limit to guests already carrying any of these tags")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview only; make no changes")
	_ = cmd.RegisterFlagCompletionFunc("has-tag", completeTagNames(a))
	return cmd
}

// exampleArgs renders the positional placeholder for a bulk op's examples.
func exampleArgs(op string) string {
	if op == "clear" {
		return "clear"
	}
	return op + " sometag"
}

func newTagRenameCmd(a *app) *cobra.Command {
	var node string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "rename <old> <new>",
		Short: "Rename a tag across every guest that carries it",
		Args:  cobra.ExactArgs(2),
		Example: "  pc tag rename prdo prod --dry-run\n" +
			"  pc tag rename legacy deprecated --yes",
		ValidArgsFunction: func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 { // only complete the <old> tag from existing names
				return completeTagNames(a)(cmd, args, tc)
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			oldTag, newTag := strings.TrimSpace(args[0]), strings.TrimSpace(args[1])
			if oldTag == "" || newTag == "" {
				return fmt.Errorf("old and new tag must be non-empty")
			}
			if oldTag == newTag {
				return fmt.Errorf("old and new tag are identical")
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
			rename := func(cur []string) []string { return addTags(removeTags(cur, []string{oldTag}), []string{newTag}) }
			var targets []domain.Guest
			pt := output.Tabular{Columns: []string{"vmid", "name", "kind", "node", "tags"}}
			for _, g := range guests {
				if !guestHasAnyTag(g.Tags, []string{oldTag}) {
					continue
				}
				cur := normalizeTags(splitTags(g.Tags))
				targets = append(targets, g)
				pt.Rows = append(pt.Rows, []string{strconv.Itoa(g.VMID), g.Name, labelFromKind(g.Kind), g.Node, joinTags(rename(cur))})
			}
			if len(targets) == 0 {
				fmt.Fprintf(stderrWriter(), "no guests carry tag %q\n", oldTag)
				return nil
			}
			if err := a.render(pt); err != nil {
				return err
			}
			if dryRun {
				fmt.Fprintf(stderrWriter(), "dry-run: %q -> %q on %d guest(s)\n", oldTag, newTag, len(targets))
				return nil
			}
			if err := confirm(a, fmt.Sprintf("rename tag %q to %q on %d guest(s)?", oldTag, newTag, len(targets))); err != nil {
				return err
			}
			var failed int
			for _, g := range targets {
				cur, digest, err := guestTagsAndDigest(cmd.Context(), p, g)
				if err != nil {
					fmt.Fprintf(stderrWriter(), "%s %d: %v\n", labelFromKind(g.Kind), g.VMID, err)
					failed++
					continue
				}
				next := rename(cur)
				if joinTags(cur) == joinTags(next) {
					continue
				}
				if err := writeGuestTags(cmd.Context(), a, p, g, next, digest,
					fmt.Sprintf("tag %s %d", labelFromKind(g.Kind), g.VMID)); err != nil {
					fmt.Fprintf(stderrWriter(), "%s %d: %v\n", labelFromKind(g.Kind), g.VMID, err)
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d guest tag update(s) failed", failed, len(targets))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "limit to guests on this node")
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
			// A backup is only re-importable as json/yaml; warn if the human table
			// is being redirected to a file (a classic `export > backup.json` trap).
			if !isTTY() && a.format != "json" && a.format != "yaml" {
				fmt.Fprintln(stderrWriter(), "warning: writing the human table; pass -o json for a re-importable backup")
			}
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
	return markTabular(cmd)
}

func newTagImportCmd(a *app) *cobra.Command {
	var dryRun, force bool
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Restore guest tags from a `tag export` file",
		Long: "Reads a JSON file produced by `tag export` and overwrites each listed\n" +
			"guest's tags. Guests are re-resolved by vmid against the current cluster.\n" +
			"If the live guest's name/kind no longer matches the file (a recycled\n" +
			"vmid), it is skipped unless --force. PVE-only (PDM has no config-write).",
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
			// Unresolved vmids and name/kind mismatches (recycled vmids) count as
			// failures so a partial restore exits non-zero.
			type change struct {
				g  domain.Guest
				to []string
			}
			var changes []change
			var failed int
			pt := output.Tabular{Columns: []string{"vmid", "node", "kind", "tags"}}
			for _, e := range entries {
				g, err := p.ResolveGuest(cmd.Context(), e.VMID)
				if err != nil {
					fmt.Fprintf(stderrWriter(), "skip vmid %d: %v\n", e.VMID, err)
					failed++
					continue
				}
				if !force && tagEntryMismatch(e, g) {
					fmt.Fprintf(stderrWriter(), "skip vmid %d: backup is %q/%s but live is %q/%s (recycled vmid?); use --force to override\n",
						e.VMID, e.Name, e.Kind, g.Name, labelFromKind(g.Kind))
					failed++
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
				if failed > 0 {
					return fmt.Errorf("%d entr(ies) could not be applied; nothing changed", failed)
				}
				fmt.Fprintln(stderrWriter(), "all guests already match the file; nothing to do")
				return nil
			}
			if err := a.render(pt); err != nil {
				return err
			}
			if dryRun {
				fmt.Fprintf(stderrWriter(), "dry-run: %d guest(s) would be updated", len(changes))
				if failed > 0 {
					fmt.Fprintf(stderrWriter(), ", %d skipped", failed)
				}
				fmt.Fprintln(stderrWriter())
				if failed > 0 {
					return fmt.Errorf("%d entr(ies) could not be applied", failed)
				}
				return nil
			}
			if err := confirm(a, fmt.Sprintf("overwrite tags on %d guest(s) from %s?", len(changes), args[0])); err != nil {
				return err
			}
			for _, c := range changes {
				cur, digest, err := guestTagsAndDigest(cmd.Context(), p, c.g)
				if err != nil {
					fmt.Fprintf(stderrWriter(), "%s %d: %v\n", labelFromKind(c.g.Kind), c.g.VMID, err)
					failed++
					continue
				}
				if joinTags(cur) == joinTags(c.to) {
					continue
				}
				if err := writeGuestTags(cmd.Context(), a, p, c.g, c.to, digest,
					fmt.Sprintf("tag %s %d", labelFromKind(c.g.Kind), c.g.VMID)); err != nil {
					fmt.Fprintf(stderrWriter(), "%s %d: %v\n", labelFromKind(c.g.Kind), c.g.VMID, err)
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d entr(ies) could not be applied", failed)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview only; make no changes")
	cmd.Flags().BoolVar(&force, "force", false, "apply even when the live name/kind differs from the backup")
	return cmd
}

// tagEntryMismatch reports whether a live guest no longer matches the backup
// entry's identity (name/kind), signalling a recycled vmid. Empty backup fields
// are treated as "don't care" for backward compatibility with older exports.
func tagEntryMismatch(e tagExportEntry, g domain.Guest) bool {
	if e.Name != "" && e.Name != g.Name {
		return true
	}
	if e.Kind != "" && e.Kind != labelFromKind(g.Kind) {
		return true
	}
	return false
}

// parseIDSet parses vmid selector values (already comma-split by cobra) into a
// set; empty returns nil (no filter).
func parseIDSet(vals []string) (map[int]bool, error) {
	set := map[int]bool{}
	for _, part := range vals {
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
	if len(set) == 0 {
		return nil, nil
	}
	return set, nil
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, ",")
}
