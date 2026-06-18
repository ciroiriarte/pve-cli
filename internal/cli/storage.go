package cli

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// rawListTabular GETs a list endpoint via Raw and projects the chosen columns.
func rawListTabular(ctx context.Context, p provider.Provider, path string, q url.Values, cols []string) (output.Tabular, error) {
	body, err := p.Raw(ctx, "GET", path, q)
	if err != nil {
		return output.Tabular{}, err
	}
	var rows []map[string]any
	if err := protocol.DecodeData(body, &rows); err != nil {
		return output.Tabular{}, err
	}
	t := output.Tabular{Columns: cols, Raw: rows}
	for _, r := range rows {
		cells := make([]string, len(cols))
		for i, c := range cols {
			if v, ok := r[c]; ok && v != nil {
				cells[i] = formatCell(v)
			}
		}
		t.Rows = append(t.Rows, cells)
	}
	return t, nil
}

// formatCell renders a decoded JSON value for table display. JSON numbers
// decode to float64; integral ones are shown as plain integers (not 6.5e+09).
func formatCell(v any) string {
	if f, ok := v.(float64); ok && f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return fmt.Sprintf("%v", v)
}

// rawObjectKV GETs a single-object endpoint via Raw and renders sorted key/value rows.
func rawObjectKV(ctx context.Context, p provider.Provider, path string) (output.Tabular, error) {
	body, err := p.Raw(ctx, "GET", path, nil)
	if err != nil {
		return output.Tabular{}, err
	}
	var obj map[string]any
	if err := protocol.DecodeData(body, &obj); err != nil {
		return output.Tabular{}, err
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	t := output.Tabular{Columns: []string{"key", "value"}, Raw: obj}
	for _, k := range keys {
		t.Rows = append(t.Rows, []string{k, fmt.Sprintf("%v", obj[k])})
	}
	return t, nil
}

func newStorageCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "storage", Aliases: []string{"store"}, Short: "Inspect storage"}

	var listNode string
	list := &cobra.Command{
		Use: "list", Short: "List storage (cluster-wide, or per-node with usage)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if listNode != "" {
				t, err := rawListTabular(cmd.Context(), p, fmt.Sprintf("/nodes/%s/storage", listNode), nil,
					[]string{"storage", "type", "content", "active", "used", "avail", "total"})
				if err != nil {
					return err
				}
				return a.render(t)
			}
			t, err := rawListTabular(cmd.Context(), p, "/storage", nil, []string{"storage", "type", "content"})
			if err != nil {
				return err
			}
			return a.render(t)
		},
	}
	list.Flags().StringVar(&listNode, "node", "", "show per-node status incl. usage")

	show := &cobra.Command{
		Use: "show <id>", Short: "Show a storage definition", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			t, err := rawObjectKV(cmd.Context(), p, "/storage/"+args[0])
			if err != nil {
				return err
			}
			return a.render(t)
		},
	}

	content := &cobra.Command{Use: "content", Short: "Storage content (ISOs, templates, backups)"}
	var cNode, cType string
	contentList := &cobra.Command{
		Use: "list <storage>", Short: "List content of a storage", Args: cobra.ExactArgs(1),
		Example: "  pc storage content list local --node pve-01 --type backup",
		RunE: func(cmd *cobra.Command, args []string) error {
			if cNode == "" {
				return fmt.Errorf("--node is required (content is node-scoped)")
			}
			p, err := a.Provider()
			if err != nil {
				return err
			}
			q := url.Values{}
			if cType != "" {
				q.Set("content", cType)
			}
			t, err := rawListTabular(cmd.Context(), p, fmt.Sprintf("/nodes/%s/storage/%s/content", cNode, args[0]), q,
				[]string{"volid", "content", "format", "size", "vmid"})
			if err != nil {
				return err
			}
			return a.render(t)
		},
	}
	contentList.Flags().StringVar(&cNode, "node", "", "node (required)")
	contentList.Flags().StringVar(&cType, "type", "", "filter by content type (backup|iso|images|vztmpl|...)")
	content.AddCommand(contentList)

	cmd.AddCommand(list, show, content)
	return cmd
}

func newBackupCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "backup", Short: "Create and list backups"}

	var storage, mode string
	var noWait bool
	var timeout int
	create := &cobra.Command{
		Use: "create <vmid>", Short: "Back up a guest (vzdump)", Args: cobra.ExactArgs(1),
		Example: "  pc backup create 100 --storage backup-nfs --mode snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if storage == "" {
				return fmt.Errorf("--storage is required")
			}
			p, err := a.Provider()
			if err != nil {
				return err
			}
			// vzdump is node-scoped; resolve the guest's node (vm or ct).
			g, err := p.ResolveGuest(cmd.Context(), atoiOr(args[0]))
			if err != nil {
				return err
			}
			params := url.Values{"vmid": {args[0]}, "storage": {storage}}
			if mode != "" {
				params.Set("mode", mode)
			}
			return rawMutate(cmd.Context(), a, p, "POST", fmt.Sprintf("/nodes/%s/vzdump", g.Node), params,
				fmt.Sprintf("backup guest %d", g.VMID), !noWait, timeout)
		},
	}
	create.Flags().StringVar(&storage, "storage", "", "target storage (required)")
	create.Flags().StringVar(&mode, "mode", "", "snapshot|suspend|stop")
	create.Flags().BoolVar(&noWait, "no-wait", false, "return immediately with the task id")
	create.Flags().IntVar(&timeout, "wait-timeout", 0, "seconds to wait (0 = no limit)")

	var lNode, lStorage string
	list := &cobra.Command{
		Use: "list", Short: "List backup volumes on a storage", Args: cobra.NoArgs,
		Example: "  pc backup list --node pve-01 --storage backup-nfs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if lNode == "" || lStorage == "" {
				return fmt.Errorf("--node and --storage are required")
			}
			p, err := a.Provider()
			if err != nil {
				return err
			}
			t, err := rawListTabular(cmd.Context(), p,
				fmt.Sprintf("/nodes/%s/storage/%s/content", lNode, lStorage),
				url.Values{"content": {"backup"}},
				[]string{"volid", "format", "size", "vmid", "ctime"})
			if err != nil {
				return err
			}
			return a.render(t)
		},
	}
	list.Flags().StringVar(&lNode, "node", "", "node (required)")
	list.Flags().StringVar(&lStorage, "storage", "", "storage (required)")

	cmd.AddCommand(create, list)
	return cmd
}

func atoiOr(s string) int {
	n := 0
	fmt.Sscanf(s, "%d", &n)
	return n
}
