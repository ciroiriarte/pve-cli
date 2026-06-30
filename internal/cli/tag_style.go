package cli

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// Tag presentation (colors, chip shape, ordering, case-sensitivity) lives in the
// datacenter `tag-style` cluster option (/cluster/options), NOT on the guest. It
// is a Proxmox property string of the form
//
//	case-sensitive=<0|1>,color-map=<tag>:<bg>[:<text>];...,ordering=<...>,shape=<...>
//
// When unset, Proxmox auto-derives a color per tag from a hash of its name; this
// group lets an operator make those colors deterministic. All edits are
// read-modify-write on the single property string so unrelated components
// (colors vs shape) survive an edit; PVE-only (PDM proxies remotes — there is no
// single datacenter option to write).

// tagColor is one color-map entry: a tag and its background (and optional text)
// color, stored as 6-hex lowercase without a leading '#'.
type tagColor struct {
	Tag        string `json:"tag"`
	Background string `json:"background"`
	Text       string `json:"text,omitempty"`
}

// tagStyle models the datacenter tag-style option. CaseSensitive is a pointer so
// "unset" round-trips distinctly from an explicit false.
type tagStyle struct {
	Shape         string     `json:"shape,omitempty"`
	Ordering      string     `json:"ordering,omitempty"`
	CaseSensitive *bool      `json:"case_sensitive,omitempty"`
	ColorMap      []tagColor `json:"color_map,omitempty"`
}

var (
	tagShapes    = []string{"full", "circle", "dense", "none"}
	tagOrderings = []string{"config", "alphabetical"}
	hexColorRe   = regexp.MustCompile(`^[0-9a-fA-F]{6}$`)
)

// tagStyleFromAny normalizes the value GET /cluster/options returns for
// `tag-style`. Proxmox is asymmetric here: the PUT takes a property string, but
// the GET parses it into a JSON object ({"color-map":"a:..;b:..","shape":".."},
// color-map kept as its own sub-list string). Older/raw responses may still be a
// bare string, so handle both.
func tagStyleFromAny(v any) tagStyle {
	switch t := v.(type) {
	case string:
		return parseTagStyle(t)
	case map[string]any:
		ts := tagStyle{
			Shape:    asString(t["shape"]),
			Ordering: asString(t["ordering"]),
			ColorMap: parseColorMap(asString(t["color-map"])),
		}
		if cv, ok := t["case-sensitive"]; ok {
			s := asString(cv)
			b := s == "1" || strings.EqualFold(s, "true")
			ts.CaseSensitive = &b
		}
		return ts
	default:
		return tagStyle{}
	}
}

// parseTagStyle parses the tag-style property string. The color-map value
// contains only ':' and ';' sub-delimiters (no ',' or '='), so a top-level split
// on ',' is safe.
func parseTagStyle(s string) tagStyle {
	var ts tagStyle
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "shape":
			ts.Shape = v
		case "ordering":
			ts.Ordering = v
		case "case-sensitive":
			b := v == "1" || strings.EqualFold(v, "true")
			ts.CaseSensitive = &b
		case "color-map":
			ts.ColorMap = parseColorMap(v)
		}
	}
	return ts
}

func parseColorMap(s string) []tagColor {
	var out []tagColor
	for _, e := range strings.Split(s, ";") {
		fields := strings.Split(e, ":")
		tag := strings.TrimSpace(fields[0])
		if tag == "" {
			continue
		}
		c := tagColor{Tag: tag}
		if len(fields) > 1 {
			c.Background = strings.ToLower(strings.TrimSpace(fields[1]))
		}
		if len(fields) > 2 {
			c.Text = strings.ToLower(strings.TrimSpace(fields[2]))
		}
		out = append(out, c)
	}
	return out
}

// String serializes a tag-style to its property-string form with a stable key
// order (color-map sorted by tag) so writes are deterministic.
func (ts tagStyle) String() string {
	var parts []string
	if ts.CaseSensitive != nil {
		parts = append(parts, "case-sensitive="+boolParam(*ts.CaseSensitive))
	}
	if cm := colorMapString(ts.ColorMap); cm != "" {
		parts = append(parts, "color-map="+cm)
	}
	if ts.Ordering != "" {
		parts = append(parts, "ordering="+ts.Ordering)
	}
	if ts.Shape != "" {
		parts = append(parts, "shape="+ts.Shape)
	}
	return strings.Join(parts, ",")
}

func colorMapString(cm []tagColor) string {
	if len(cm) == 0 {
		return ""
	}
	sorted := append([]tagColor(nil), cm...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Tag < sorted[j].Tag })
	parts := make([]string, 0, len(sorted))
	for _, c := range sorted {
		s := c.Tag + ":" + c.Background
		if c.Text != "" {
			s += ":" + c.Text
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ";")
}

// isEmpty reports whether the style has no components — the cue to delete the
// property entirely rather than write an empty string.
func (ts tagStyle) isEmpty() bool {
	return ts.Shape == "" && ts.Ordering == "" && ts.CaseSensitive == nil && len(ts.ColorMap) == 0
}

// setColor inserts or replaces the color entry for c.Tag, preserving order.
func (ts *tagStyle) setColor(c tagColor) {
	for i := range ts.ColorMap {
		if ts.ColorMap[i].Tag == c.Tag {
			ts.ColorMap[i] = c
			return
		}
	}
	ts.ColorMap = append(ts.ColorMap, c)
}

// removeColors drops color entries for any of tags and returns how many matched.
func (ts *tagStyle) removeColors(tags []string) int {
	drop := map[string]bool{}
	for _, t := range tags {
		drop[strings.TrimSpace(t)] = true
	}
	var kept []tagColor
	removed := 0
	for _, c := range ts.ColorMap {
		if drop[c.Tag] {
			removed++
			continue
		}
		kept = append(kept, c)
	}
	ts.ColorMap = kept
	return removed
}

// normalizeHexColor validates a 6-hex color (optionally '#'-prefixed) and returns
// it lowercased without the '#'.
func normalizeHexColor(s string) (string, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if !hexColorRe.MatchString(s) {
		return "", fmt.Errorf("invalid hex color %q: want 6 hex digits like ff0000 (a leading '#' is allowed)", s)
	}
	return strings.ToLower(s), nil
}

func validateEnum(name, val string, allowed []string) error {
	for _, a := range allowed {
		if val == a {
			return nil
		}
	}
	return fmt.Errorf("invalid %s %q: want one of %s", name, val, strings.Join(allowed, "|"))
}

// ensurePVECluster gates tag-style ops on a direct PVE provider. PDM proxies many
// remotes, each with its own datacenter options, so there is no single
// /cluster/options to operate on — inspect those with `pc remote options <id>`.
func ensurePVECluster(p provider.Provider, op string) error {
	if p.Name() == "pdm" {
		return fmt.Errorf("%s operates on the active PVE datacenter's tag-style and is not available via PDM (current provider: pdm); set provider: pve, or inspect a remote with `pc remote options <id>`", op)
	}
	return nil
}

func readTagStyle(ctx context.Context, p provider.Provider) (tagStyle, error) {
	body, err := p.Raw(ctx, "GET", "/cluster/options", nil)
	if err != nil {
		return tagStyle{}, err
	}
	var opts map[string]any
	if err := protocol.DecodeData(body, &opts); err != nil {
		return tagStyle{}, err
	}
	return tagStyleFromAny(opts["tag-style"]), nil
}

// writeTagStyle persists the style via /cluster/options, removing the property
// outright when the style is empty so a cleared style doesn't linger as "".
func writeTagStyle(ctx context.Context, a *app, p provider.Provider, ts tagStyle, label string) error {
	params := url.Values{}
	if ts.isEmpty() {
		params.Set("delete", "tag-style")
	} else {
		params.Set("tag-style", ts.String())
	}
	return rawMutate(ctx, a, p, "PUT", "/cluster/options", params, label, false, 0)
}

// --- color commands ---------------------------------------------------------

func newTagColorCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "color",
		Short: "Manage datacenter tag colors (the color-map in tag-style)",
		Long: "Set deterministic colors for tags. Colors live in the datacenter\n" +
			"`tag-style` option, not on the guest; an unset tag gets an auto-derived\n" +
			"color. PVE-only (PDM proxies remotes — use `pc remote options <id>`).",
	}
	cmd.AddCommand(
		newTagColorListCmd(a),
		newTagColorSetCmd(a),
		newTagColorRmCmd(a),
		newTagColorClearCmd(a),
	)
	return cmd
}

func tagColorTable(colors []tagColor) output.Tabular {
	sorted := make([]tagColor, len(colors))
	copy(sorted, colors)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Tag < sorted[j].Tag })
	t := output.Tabular{Columns: []string{"tag", "background", "text"}, Raw: sorted}
	for _, c := range sorted {
		t.Rows = append(t.Rows, []string{c.Tag, c.Background, c.Text})
	}
	return t
}

func newTagColorListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured tag colors",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVECluster(p, "tag color"); err != nil {
				return err
			}
			ts, err := readTagStyle(cmd.Context(), p)
			if err != nil {
				return err
			}
			return a.render(tagColorTable(ts.ColorMap))
		},
	}
}

func describeColor(c tagColor) string {
	if c.Text != "" {
		return "bg #" + c.Background + ", text #" + c.Text
	}
	return "bg #" + c.Background
}

func newTagColorSetCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <tag> <background> [text]",
		Short: "Set or replace a tag's color (read-modify-write)",
		Long:  "Background and optional text color are 6-hex (RRGGBB), with or without a leading '#'.",
		Args:  cobra.RangeArgs(2, 3),
		Example: "  pc tag color set prod ff0000\n" +
			"  pc tag color set test 808080 ffffff",
		ValidArgsFunction: func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 { // first arg is the tag
				return completeTagNames(a)(cmd, args, tc)
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			tag := strings.TrimSpace(args[0])
			if tag == "" {
				return fmt.Errorf("tag must be non-empty")
			}
			bg, err := normalizeHexColor(args[1])
			if err != nil {
				return err
			}
			c := tagColor{Tag: tag, Background: bg}
			if len(args) == 3 {
				txt, err := normalizeHexColor(args[2])
				if err != nil {
					return err
				}
				c.Text = txt
			}
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVECluster(p, "tag color"); err != nil {
				return err
			}
			ts, err := readTagStyle(cmd.Context(), p)
			if err != nil {
				return err
			}
			ts.setColor(c)
			if err := confirm(a, fmt.Sprintf("set tag %q color to %s on the datacenter tag-style?", tag, describeColor(c))); err != nil {
				return err
			}
			return writeTagStyle(cmd.Context(), a, p, ts, fmt.Sprintf("tag color set %s", tag))
		},
	}
	return cmd
}

func newTagColorRmCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <tag>...",
		Aliases: []string{"remove"},
		Short:   "Remove tag color overrides",
		Args:    cobra.MinimumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, tc string) ([]string, cobra.ShellCompDirective) {
			return completeTagNames(a)(cmd, args, tc)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVECluster(p, "tag color"); err != nil {
				return err
			}
			ts, err := readTagStyle(cmd.Context(), p)
			if err != nil {
				return err
			}
			n := ts.removeColors(args)
			if n == 0 {
				fmt.Fprintln(stderrWriter(), "no matching tag colors to remove")
				return nil
			}
			if err := confirm(a, fmt.Sprintf("remove color(s) for %d tag(s) from the datacenter tag-style?", n)); err != nil {
				return err
			}
			return writeTagStyle(cmd.Context(), a, p, ts, "tag color rm")
		},
	}
}

func newTagColorClearCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Remove all tag color overrides",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVECluster(p, "tag color"); err != nil {
				return err
			}
			ts, err := readTagStyle(cmd.Context(), p)
			if err != nil {
				return err
			}
			if len(ts.ColorMap) == 0 {
				fmt.Fprintln(stderrWriter(), "no tag colors configured")
				return nil
			}
			n := len(ts.ColorMap)
			ts.ColorMap = nil
			if err := confirm(a, fmt.Sprintf("remove ALL %d tag color(s) from the datacenter tag-style?", n)); err != nil {
				return err
			}
			return writeTagStyle(cmd.Context(), a, p, ts, "tag color clear")
		},
	}
}

// --- style (shape / ordering / case-sensitivity) ----------------------------

func newTagStyleCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "style",
		Short: "Show and set datacenter tag-style (shape, ordering, case-sensitivity)",
		Long: "Read and edit the non-color parts of the datacenter `tag-style`. Manage\n" +
			"colors with `pc tag color`. PVE-only.",
	}
	cmd.AddCommand(newTagStyleShowCmd(a), newTagStyleSetCmd(a), newTagStyleClearCmd(a))
	return cmd
}

func tagStyleTable(ts tagStyle) output.Tabular {
	cs := ""
	if ts.CaseSensitive != nil {
		cs = boolParam(*ts.CaseSensitive)
	}
	return output.Tabular{
		Columns: []string{"field", "value"},
		Raw:     ts,
		Rows: [][]string{
			{"shape", ts.Shape},
			{"ordering", ts.Ordering},
			{"case-sensitive", cs},
			{"colors", strconv.Itoa(len(ts.ColorMap))},
		},
	}
}

func newTagStyleShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the datacenter tag-style",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVECluster(p, "tag style"); err != nil {
				return err
			}
			ts, err := readTagStyle(cmd.Context(), p)
			if err != nil {
				return err
			}
			return a.render(tagStyleTable(ts))
		},
	}
}

func newTagStyleClearCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Remove the entire datacenter tag-style (colors, shape, ordering)",
		Long: "Deletes the whole `tag-style` option, reverting to Proxmox defaults\n" +
			"(auto-derived colors). To drop only colors, use `pc tag color clear`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVECluster(p, "tag style"); err != nil {
				return err
			}
			ts, err := readTagStyle(cmd.Context(), p)
			if err != nil {
				return err
			}
			if ts.isEmpty() {
				fmt.Fprintln(stderrWriter(), "no tag-style configured")
				return nil
			}
			if err := confirm(a, "remove the ENTIRE datacenter tag-style (colors, shape, ordering)?"); err != nil {
				return err
			}
			return writeTagStyle(cmd.Context(), a, p, tagStyle{}, "tag style clear")
		},
	}
}

func newTagStyleSetCmd(a *app) *cobra.Command {
	var shape, ordering string
	var caseSensitive bool
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set tag-style shape/ordering/case-sensitivity",
		Long:  "Only the flags you pass are changed; the color-map is preserved.",
		Args:  cobra.NoArgs,
		Example: "  pc tag style set --shape circle\n" +
			"  pc tag style set --ordering alphabetical --case-sensitive",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fl := cmd.Flags()
			if !fl.Changed("shape") && !fl.Changed("ordering") && !fl.Changed("case-sensitive") {
				return fmt.Errorf("nothing to set: pass --shape, --ordering, and/or --case-sensitive")
			}
			if fl.Changed("shape") {
				if err := validateEnum("shape", shape, tagShapes); err != nil {
					return err
				}
			}
			if fl.Changed("ordering") {
				if err := validateEnum("ordering", ordering, tagOrderings); err != nil {
					return err
				}
			}
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVECluster(p, "tag style"); err != nil {
				return err
			}
			ts, err := readTagStyle(cmd.Context(), p)
			if err != nil {
				return err
			}
			if fl.Changed("shape") {
				ts.Shape = shape
			}
			if fl.Changed("ordering") {
				ts.Ordering = ordering
			}
			if fl.Changed("case-sensitive") {
				b := caseSensitive
				ts.CaseSensitive = &b
			}
			if err := confirm(a, "update the datacenter tag-style?"); err != nil {
				return err
			}
			return writeTagStyle(cmd.Context(), a, p, ts, "tag style set")
		},
	}
	cmd.Flags().StringVar(&shape, "shape", "", "chip shape: full|circle|dense|none")
	cmd.Flags().StringVar(&ordering, "ordering", "", "tag ordering: config|alphabetical")
	cmd.Flags().BoolVar(&caseSensitive, "case-sensitive", false, "treat tags as case-sensitive (use --case-sensitive=false to disable)")
	_ = cmd.RegisterFlagCompletionFunc("shape", cobra.FixedCompletions(tagShapes, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("ordering", cobra.FixedCompletions(tagOrderings, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}
