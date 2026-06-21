package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

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

	var delNode string
	contentDelete := &cobra.Command{
		Use: "delete <storage> <volume>", Aliases: []string{"rm"}, Short: "Delete a volume (backup/ISO/image)", Args: cobra.ExactArgs(2),
		Example: "  pc storage content delete local backup/vzdump-qemu-100-...vma.zst --node pve-01",
		RunE: func(cmd *cobra.Command, args []string) error {
			if delNode == "" {
				return fmt.Errorf("--node is required (content is node-scoped)")
			}
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("delete volume %q from %s on %s?", args[1], args[0], delNode)); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE",
				fmt.Sprintf("/nodes/%s/storage/%s/content/%s", delNode, args[0], args[1]), nil,
				"delete "+args[1], true, 0)
		},
	}
	contentDelete.Flags().StringVar(&delNode, "node", "", "node (required)")
	content.AddCommand(contentList, contentDelete, newStorageUploadCmd(a))

	var statusNode string
	status := &cobra.Command{
		Use: "status <storage>", Short: "Show storage usage/status on a node", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			// Shared storage (NFS/CephFS/PBS) reports the same from any node;
			// default to an online node. Pass --node for node-local storage.
			n, err := nodeOrAuto(cmd.Context(), p, statusNode)
			if err != nil {
				return err
			}
			return a.renderGet(cmd, p, fmt.Sprintf("/nodes/%s/storage/%s/status", n, args[0]))
		},
	}
	status.Flags().StringVar(&statusNode, "node", "", "node to query (optional; defaults to an online node)")

	var pruneNode, pruneVMID string
	var pruneApply bool
	prune := &cobra.Command{
		Use: "prune-backups <storage>", Short: "List (or apply) backup retention pruning", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if pruneNode == "" {
				return fmt.Errorf("--node is required")
			}
			p, err := a.Provider()
			if err != nil {
				return err
			}
			q := url.Values{}
			if pruneVMID != "" {
				q.Set("vmid", pruneVMID)
			}
			path := fmt.Sprintf("/nodes/%s/storage/%s/prunebackups", pruneNode, args[0])
			if !pruneApply {
				return a.renderGet(cmd, p, path+"?"+q.Encode(), "volid", "type", "vmid", "mark")
			}
			if err := confirm(a, fmt.Sprintf("apply pruning on %s/%s (deletes old backups)?", pruneNode, args[0])); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", path, q, "prune backups on "+args[0], true, 0)
		},
	}
	prune.Flags().StringVar(&pruneNode, "node", "", "node (required)")
	prune.Flags().StringVar(&pruneVMID, "vmid", "", "limit to a vmid")
	prune.Flags().BoolVar(&pruneApply, "apply", false, "actually delete (default: dry-run list)")

	cmd.AddCommand(list, show, content, status, prune)
	return cmd
}

// uploader is the optional provider capability for multipart file uploads
// (PVE-only). PDM has no storage-upload API, so it simply won't satisfy it.
type uploader interface {
	RawBody(ctx context.Context, method, path, contentType string, body io.Reader, contentLength int64) ([]byte, error)
}

// detectUploadContent guesses the storage content type from a filename, so the
// user rarely needs --content. Empty means "couldn't tell".
func detectUploadContent(name string) string {
	low := strings.ToLower(name)
	switch {
	case strings.HasSuffix(low, ".iso"):
		return "iso"
	case strings.HasSuffix(low, ".tar.gz"), strings.HasSuffix(low, ".tgz"),
		strings.HasSuffix(low, ".tar.xz"), strings.HasSuffix(low, ".txz"),
		strings.HasSuffix(low, ".tar.zst"), strings.HasSuffix(low, ".tar.bz2"):
		return "vztmpl"
	}
	return ""
}

func newStorageUploadCmd(a *app) *cobra.Command {
	var node, content, filename, checksum, checksumAlgo string
	var wait, noWait bool
	var timeout int
	cmd := &cobra.Command{
		Use:   "upload <storage> <file>",
		Short: "Upload an ISO, container template, or snippet to a storage",
		Long: "Streams a local file to a storage via the multipart upload API. The content\n" +
			"type is auto-detected from the extension (.iso -> iso; .tar.{gz,xz,zst} ->\n" +
			"vztmpl); override with --content. Returns a task (use --wait/--no-wait).",
		Example: "  pc storage content upload local /isos/debian-13.iso\n" +
			"  pc storage content upload local tmpl.tar.zst --content vztmpl --node pve-01",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			storage, srcPath := args[0], args[1]
			p, err := a.Provider()
			if err != nil {
				return err
			}
			up, ok := p.(uploader)
			if !ok {
				return fmt.Errorf("upload is only available with the pve provider (not %s)", p.Name())
			}

			name := filename
			if name == "" {
				name = filepath.Base(srcPath)
			}
			ctype := content
			if ctype == "" {
				if ctype = detectUploadContent(name); ctype == "" {
					return fmt.Errorf("cannot detect content type for %q; pass --content iso|vztmpl|snippets|import", name)
				}
			}
			if checksum != "" && checksumAlgo == "" {
				return fmt.Errorf("--checksum-algorithm is required with --checksum (md5|sha1|sha256|sha512)")
			}

			f, err := os.Open(srcPath)
			if err != nil {
				return err
			}
			defer f.Close()
			fi, err := f.Stat()
			if err != nil {
				return err
			}

			// Storage content is node-scoped; shared storage (NFS/CephFS/PBS) is
			// reachable from any online node, node-local needs the right --node.
			n, err := nodeOrAuto(cmd.Context(), p, node)
			if err != nil {
				return err
			}

			// pveproxy rejects chunked transfer-encoding, so we must send a
			// Content-Length. Build the multipart prefix (fields + file header) and
			// suffix (closing boundary) into a buffer to measure them exactly, then
			// stream prefix -> file -> suffix so the file itself never buffers.
			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			if err := mw.WriteField("content", ctype); err != nil {
				return err
			}
			if checksum != "" {
				if err := mw.WriteField("checksum", checksum); err != nil {
					return err
				}
				if err := mw.WriteField("checksum-algorithm", checksumAlgo); err != nil {
					return err
				}
			}
			if _, err := mw.CreateFormFile("filename", name); err != nil {
				return err
			}
			prefixLen := buf.Len()
			if err := mw.Close(); err != nil { // appends the closing boundary
				return err
			}
			prefix := buf.Bytes()[:prefixLen]
			suffix := buf.Bytes()[prefixLen:]
			contentLength := int64(len(prefix)) + fi.Size() + int64(len(suffix))
			reader := io.MultiReader(bytes.NewReader(prefix), f, bytes.NewReader(suffix))

			path := fmt.Sprintf("/nodes/%s/storage/%s/upload", n, storage)
			body, err := up.RawBody(cmd.Context(), "POST", path, mw.FormDataContentType(), reader, contentLength)
			if err != nil {
				return err
			}
			// The upload runs as a task; reuse the shared wait/print UX.
			var upid string
			if err := protocol.DecodeData(body, &upid); err != nil || upid == "" {
				fmt.Fprintf(stderrWriter(), "uploaded %s to %s\n", name, storage)
				return nil
			}
			h, err := parseTaskArg(upid)
			if err != nil {
				return nil
			}
			label := fmt.Sprintf("upload %s -> %s", name, storage)
			h.Display = label
			return finishTask(cmd.Context(), a, p, h, wait && !noWait, timeout, label)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "node to upload through (optional; defaults to an online node)")
	cmd.Flags().StringVar(&content, "content", "", "content type: iso|vztmpl|snippets|import (auto-detected from extension)")
	cmd.Flags().StringVar(&filename, "filename", "", "stored filename (defaults to the source basename)")
	cmd.Flags().StringVar(&checksum, "checksum", "", "expected checksum of the file")
	cmd.Flags().StringVar(&checksumAlgo, "checksum-algorithm", "", "checksum algorithm (md5|sha1|sha256|sha512)")
	waitFlags(cmd, &wait, &noWait, &timeout)
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
		Example: "  pc backup list --storage backup-nfs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if lStorage == "" {
				return fmt.Errorf("--storage is required")
			}
			p, err := a.Provider()
			if err != nil {
				return err
			}
			// Backups on shared storage are visible from any node; default to an
			// online one. Pass --node to target node-local storage.
			n, err := nodeOrAuto(cmd.Context(), p, lNode)
			if err != nil {
				return err
			}
			t, err := rawListTabular(cmd.Context(), p,
				fmt.Sprintf("/nodes/%s/storage/%s/content", n, lStorage),
				url.Values{"content": {"backup"}},
				[]string{"volid", "format", "size", "vmid", "ctime"})
			if err != nil {
				return err
			}
			return a.render(t)
		},
	}
	list.Flags().StringVar(&lNode, "node", "", "node to query (optional; defaults to an online node)")
	list.Flags().StringVar(&lStorage, "storage", "", "storage (required)")

	cmd.AddCommand(create, list, newBackupJobCmd(a))
	return cmd
}

// newBackupJobCmd manages scheduled backup jobs (cluster/backup).
func newBackupJobCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "job", Short: "Manage scheduled backup jobs"}
	cmd.AddCommand(
		anyGet(a, "list", "List backup jobs", 0, func([]string) string { return "/cluster/backup" }, "id", "schedule", "storage", "enabled", "mode"),
		anyGet(a, "show <id>", "Show a backup job", 1, func(a []string) string { return "/cluster/backup/" + a[0] }),
	)
	var set []string
	var jStorage, jSchedule, jMode, jVMID string
	var jAll, jEnabled bool
	create := &cobra.Command{
		Use: "create", Short: "Create a scheduled backup job", Args: cobra.NoArgs,
		Example: "  pc backup job create --storage backup-nfs --schedule '02:00' --all\n" +
			"  pc backup job create --storage backup-nfs --vmid 100,101 --mode snapshot",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			base := map[string]string{
				"storage":  jStorage,
				"schedule": jSchedule,
				"mode":     jMode,
				"vmid":     jVMID,
			}
			// Booleans map to 0/1 only when the user set them, so the API keeps
			// its own defaults otherwise.
			if cmd.Flags().Changed("all") {
				base["all"] = boolParam(jAll)
			}
			if cmd.Flags().Changed("enabled") {
				base["enabled"] = boolParam(jEnabled)
			}
			params, err := mergeSet(base, set)
			if err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "POST", "/cluster/backup", params, "create backup job", true, 0)
		},
	}
	create.Flags().StringVar(&jStorage, "storage", "", "target storage")
	create.Flags().StringVar(&jSchedule, "schedule", "", "schedule (e.g. '02:00' or 'mon..fri 22:00')")
	create.Flags().StringVar(&jMode, "mode", "", "snapshot|suspend|stop")
	create.Flags().StringVar(&jVMID, "vmid", "", "comma-separated guest ids to back up")
	create.Flags().BoolVar(&jAll, "all", false, "back up all guests")
	create.Flags().BoolVar(&jEnabled, "enabled", true, "whether the job is enabled")
	create.Flags().StringArrayVar(&set, "set", nil, "any other field key=value (escape hatch, repeatable)")
	del := &cobra.Command{
		Use: "delete <id>", Aliases: []string{"rm"}, Short: "Delete a backup job", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("delete backup job %q?", args[0])); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", "/cluster/backup/"+args[0], nil, "delete backup job "+args[0], true, 0)
		},
	}
	cmd.AddCommand(create, del)
	return cmd
}

func atoiOr(s string) int {
	n := 0
	fmt.Sscanf(s, "%d", &n)
	return n
}
