package cli

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// Direct Proxmox Backup Server commands (provider: pbs). PBS's API is
// datastore-centric (/admin/datastore/{store}/…), so the curated surface hangs
// off `pc datastore`. It is distinct from `pc pbs`, which inspects PBS
// *remotes through PDM* and keeps working unchanged on provider: pdm. The
// provider gate (see providerGate in root.go) keeps `pc datastore` to pbs and
// the guest/cluster commands off it.

// pbsGroupTypes are PBS's backup group types.
var pbsGroupTypes = map[string]bool{"vm": true, "ct": true, "host": true}

// parseBackupGroup splits a "<type>/<id>" backup group (e.g. "vm/100").
func parseBackupGroup(g string) (typ, id string, err error) {
	typ, id, ok := strings.Cut(g, "/")
	if !ok || id == "" || !pbsGroupTypes[typ] {
		return "", "", fmt.Errorf("invalid backup group %q: want <type>/<id> with type vm, ct or host (e.g. vm/100)", g)
	}
	return typ, id, nil
}

// pbsTarget names the server for confirmation prompts, so a prompt against the
// wrong context is caught (design decision #2).
func (a *app) pbsTarget() string {
	if a.settings == nil || a.settings.Server == "" {
		return "the PBS server"
	}
	if u, err := url.Parse(a.settings.Server); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return a.settings.Server
}

func newDatastoreCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "datastore",
		Aliases: []string{"ds"},
		Short:   "Manage Proxmox Backup Server datastores (provider: pbs)",
		Long: "Datastore-centric management of a Proxmox Backup Server: usage, snapshots,\n" +
			"namespaces, garbage collection, prune, verify and jobs. Requires\n" +
			"provider: pbs. (To inspect PBS remotes through PDM, use `pc pbs`.)",
	}
	cmd.AddCommand(
		pbsList(a, "list", "List datastores", 0,
			func([]string) string { return "/admin/datastore" },
			"store", "comment", "backend-type", "mount-status", "maintenance"),
		pbsObject(a, "show <store>", "Show a datastore's configuration", func(a []string) string { return "/config/datastore/" + a[0] }),
		newDatastoreStatusCmd(a),
		group("namespace", "Datastore namespaces", newDatastoreNamespaceListCmd(a)),
		group("group", "Backup groups", newDatastoreGroupListCmd(a)),
		group("snapshot", "Backup snapshots", newDatastoreSnapshotListCmd(a)),
		group("gc", "Garbage collection",
			newDatastoreGCStatusCmd(a),
			newDatastoreGCStartCmd(a)),
		newDatastorePruneCmd(a),
		group("verify", "Verification", newDatastoreVerifyStartCmd(a)),
		group("job", "Prune, verify and sync jobs", newDatastoreJobListCmd(a)),
	)
	return cmd
}

// pbsList builds a GET list command projecting the chosen columns (json/yaml
// emit the native array).
func pbsList(a *app, use, short string, nargs int, pathFn func([]string) string, cols ...string) *cobra.Command {
	return markTabular(&cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(nargs),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			t, err := rawListTabular(cmd.Context(), p, pathFn(args), nil, cols)
			if err != nil {
				return err
			}
			return a.render(t)
		},
	})
}

// pbsObject builds a GET command for a single-object endpoint (json/yaml emit
// the native object).
func pbsObject(a *app, use, short string, pathFn func([]string) string) *cobra.Command {
	return markTabular(&cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			t, err := rawObjectKV(cmd.Context(), p, pathFn(args))
			if err != nil {
				return err
			}
			return a.render(t)
		},
	})
}

func newDatastoreStatusCmd(a *app) *cobra.Command {
	var verbose bool
	cmd := &cobra.Command{
		Use:   "status [store]",
		Short: "Show datastore usage (all datastores, or one in detail)",
		Example: "  pc datastore status            # usage of every datastore\n" +
			"  pc datastore status store1     # one datastore (counts, gc status)",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				q := url.Values{}
				if verbose {
					q.Set("verbose", "1")
				}
				body, err := p.Raw(cmd.Context(), "GET", "/admin/datastore/"+args[0]+"/status", q)
				if err != nil {
					return err
				}
				var obj map[string]any
				if err := protocol.DecodeData(body, &obj); err != nil {
					return err
				}
				return a.render(kvTable(obj))
			}
			body, err := p.Raw(cmd.Context(), "GET", "/status/datastore-usage", nil)
			if err != nil {
				return err
			}
			var rows []map[string]any
			if err := protocol.DecodeData(body, &rows); err != nil {
				return err
			}
			return a.render(datastoreUsageTable(rows))
		},
	}
	cmd.Flags().BoolVar(&verbose, "verbose", false, "include per-type snapshot counts (single datastore)")
	return cmd
}

// datastoreUsageTable renders /status/datastore-usage with human sizes; Raw is
// the native array.
func datastoreUsageTable(rows []map[string]any) output.Tabular {
	t := output.Tabular{Columns: []string{"store", "used", "avail", "total", "use%", "error"}, Raw: rows}
	for _, r := range rows {
		used, avail, total := num(r["used"]), num(r["avail"]), num(r["total"])
		pct := ""
		if total > 0 {
			pct = fmt.Sprintf("%.1f%%", float64(used)*100/float64(total))
		}
		errStr := ""
		if e, ok := r["error"].(string); ok {
			errStr = e
		}
		store, _ := r["store"].(string)
		t.Rows = append(t.Rows, []string{store, humanBytes(used), humanBytes(avail), humanBytes(total), pct, errStr})
	}
	return t
}

func num(v any) int64 {
	if f, ok := v.(float64); ok {
		return int64(f)
	}
	return 0
}

func newDatastoreNamespaceListCmd(a *app) *cobra.Command {
	var parent string
	var maxDepth int
	cmd := &cobra.Command{
		Use:     "list <store>",
		Short:   "List namespaces in a datastore",
		Example: "  pc datastore namespace list store1\n  pc datastore namespace list store1 --parent prod --max-depth 1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			q := url.Values{}
			if parent != "" {
				q.Set("parent", parent)
			}
			if cmd.Flags().Changed("max-depth") {
				q.Set("max-depth", strconv.Itoa(maxDepth))
			}
			t, err := rawListTabular(cmd.Context(), p, "/admin/datastore/"+args[0]+"/namespace", q, []string{"ns", "comment"})
			if err != nil {
				return err
			}
			return a.render(t)
		},
	}
	cmd.Flags().StringVar(&parent, "parent", "", "list below this namespace")
	cmd.Flags().IntVar(&maxDepth, "max-depth", 0, "recursion depth below --parent")
	return markTabular(cmd)
}

func newDatastoreGroupListCmd(a *app) *cobra.Command {
	var ns string
	cmd := &cobra.Command{
		Use:     "list <store>",
		Short:   "List backup groups in a datastore",
		Example: "  pc datastore group list store1 --ns prod",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			q := url.Values{}
			if ns != "" {
				q.Set("ns", ns)
			}
			t, err := rawListTabular(cmd.Context(), p, "/admin/datastore/"+args[0]+"/groups", q,
				[]string{"backup-type", "backup-id", "backup-count", "last-backup", "owner", "comment"})
			if err != nil {
				return err
			}
			return a.render(t)
		},
	}
	cmd.Flags().StringVar(&ns, "ns", "", "namespace (default: root)")
	return markTabular(cmd)
}

func newDatastoreSnapshotListCmd(a *app) *cobra.Command {
	var ns, grp, typ, id string
	cmd := &cobra.Command{
		Use:   "list <store>",
		Short: "List backup snapshots in a datastore",
		Example: "  pc datastore snapshot list store1\n" +
			"  pc datastore snapshot list store1 --ns prod --group vm/100\n" +
			"  pc datastore snapshot list store1 --type ct",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if grp != "" {
				if typ, id, err = parseBackupGroup(grp); err != nil {
					return err
				}
			} else if typ != "" && !pbsGroupTypes[typ] {
				return fmt.Errorf("invalid --type %q: want vm, ct or host", typ)
			}
			q := url.Values{}
			for k, v := range map[string]string{"ns": ns, "backup-type": typ, "backup-id": id} {
				if v != "" {
					q.Set(k, v)
				}
			}
			t, err := rawListTabular(cmd.Context(), p, "/admin/datastore/"+args[0]+"/snapshots", q,
				[]string{"backup-type", "backup-id", "backup-time", "size", "protected", "owner"})
			if err != nil {
				return err
			}
			return a.render(t)
		},
	}
	cmd.Flags().StringVar(&ns, "ns", "", "namespace (default: root)")
	cmd.Flags().StringVar(&grp, "group", "", "only this backup group (<type>/<id>, e.g. vm/100)")
	cmd.Flags().StringVar(&typ, "type", "", "only this backup type: vm|ct|host")
	cmd.Flags().StringVar(&id, "id", "", "only this backup id (with --type)")
	cmd.MarkFlagsMutuallyExclusive("group", "type")
	cmd.MarkFlagsMutuallyExclusive("group", "id")
	return markTabular(cmd)
}

func newDatastoreGCStatusCmd(a *app) *cobra.Command {
	return markTabular(&cobra.Command{
		Use:     "status [store]",
		Short:   "Show garbage-collection status (all datastores, or one)",
		Example: "  pc datastore gc status\n  pc datastore gc status store1",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				t, err := rawObjectKV(cmd.Context(), p, "/admin/datastore/"+args[0]+"/gc")
				if err != nil {
					return err
				}
				return a.render(t)
			}
			t, err := rawListTabular(cmd.Context(), p, "/admin/gc", nil,
				[]string{"store", "last-run-state", "last-run-endtime", "next-run", "schedule", "removed-bytes", "pending-bytes"})
			if err != nil {
				return err
			}
			return a.render(t)
		},
	})
}

func newDatastoreGCStartCmd(a *app) *cobra.Command {
	var wait, noWait bool
	var timeout int
	cmd := &cobra.Command{
		Use:     "start <store>",
		Short:   "Start garbage collection on a datastore",
		Example: "  pc datastore gc start store1 --yes\n  pc datastore gc start store1 --yes --no-wait",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			store := args[0]
			if err := confirm(a, fmt.Sprintf("start garbage collection on datastore %s (%s)?", store, a.pbsTarget())); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "POST", "/admin/datastore/"+store+"/gc", url.Values{},
				"garbage collection on "+store, wait && !noWait, timeout)
		},
	}
	waitFlags(cmd, &wait, &noWait, &timeout)
	return cmd
}

// keepOptions are PBS's retention flags, shared by prune.
var keepOptions = []string{"keep-last", "keep-hourly", "keep-daily", "keep-weekly", "keep-monthly", "keep-yearly"}

func newDatastorePruneCmd(a *app) *cobra.Command {
	var ns, grp string
	var maxDepth int
	var dryRun, wait, noWait bool
	var timeout int
	var set []string
	keep := make(map[string]*int, len(keepOptions))
	cmd := &cobra.Command{
		Use:   "prune <store>",
		Short: "Prune backup snapshots by retention (confirm-gated; --dry-run to preview)",
		Long: "Removes snapshots that fall outside the given retention. Without --group\n" +
			"the whole datastore (or --ns subtree) is pruned as a task; with --group\n" +
			"only that backup group is pruned and the keep/remove decision for each\n" +
			"snapshot is printed. --dry-run previews without removing anything and\n" +
			"skips the confirmation. At least one --keep-* option is required.",
		Example: "  pc datastore prune store1 --keep-last 7 --keep-weekly 4 --dry-run\n" +
			"  pc datastore prune store1 --group vm/100 --keep-last 3 --yes\n" +
			"  pc datastore prune store1 --ns prod --keep-daily 14 --yes",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			store := args[0]
			base := map[string]string{"ns": ns}
			var retention []string
			for _, k := range keepOptions {
				if cmd.Flags().Changed(k) {
					base[k] = strconv.Itoa(*keep[k])
					retention = append(retention, fmt.Sprintf("%s=%d", k, *keep[k]))
				}
			}
			if dryRun {
				base["dry-run"] = "1"
			}
			path := "/admin/datastore/" + store + "/prune-datastore"
			scope := "datastore " + store
			if grp != "" {
				typ, id, err := parseBackupGroup(grp)
				if err != nil {
					return err
				}
				base["backup-type"], base["backup-id"] = typ, id
				path = "/admin/datastore/" + store + "/prune"
				scope = "group " + grp + " in datastore " + store
			} else if cmd.Flags().Changed("max-depth") {
				base["max-depth"] = strconv.Itoa(maxDepth)
			}
			if ns != "" {
				scope += " (namespace " + ns + ")"
			}
			params, err := mergeSet(base, set)
			if err != nil {
				return err
			}
			hasKeep := false
			for _, k := range keepOptions {
				if params.Get(k) != "" {
					hasKeep = true
				}
			}
			if !hasKeep {
				return fmt.Errorf("no retention given: pass at least one of --%s", strings.Join(keepOptions, ", --"))
			}
			if !dryRun {
				if err := confirm(a, fmt.Sprintf("prune %s on %s keeping %s? snapshots outside this retention are removed",
					scope, a.pbsTarget(), orElse(strings.Join(retention, ","), "the --set retention"))); err != nil {
					return err
				}
			}
			label := "prune " + scope
			if dryRun {
				label = "prune (dry run) " + scope
			}
			return pbsPrune(cmd.Context(), a, p, path, params, label, dryRun, wait && !noWait, timeout)
		},
	}
	for _, k := range keepOptions {
		keep[k] = cmd.Flags().Int(k, 0, "retention: "+strings.ReplaceAll(strings.TrimPrefix(k, "keep-"), "-", " ")+" snapshots to keep")
	}
	cmd.Flags().StringVar(&ns, "ns", "", "namespace (default: root)")
	cmd.Flags().StringVar(&grp, "group", "", "prune only this backup group (<type>/<id>, e.g. vm/100)")
	cmd.Flags().IntVar(&maxDepth, "max-depth", 0, "namespace recursion depth (datastore-wide prune)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be pruned without removing anything (no confirmation)")
	cmd.Flags().StringArrayVar(&set, "set", nil, "extra API parameter key=value (repeatable; wins over flags)")
	cmd.MarkFlagsMutuallyExclusive("group", "max-depth")
	waitFlags(cmd, &wait, &noWait, &timeout)
	return cmd
}

// pbsPrune issues a prune. Group prune answers synchronously with the per-
// snapshot keep/remove list (rendered as a native array); datastore prune runs
// as a task. For a datastore-wide dry run the decisions only exist in the task
// log, so after waiting it is printed (table output only; json/yaml get the
// task handle, as with every other mutation).
func pbsPrune(ctx context.Context, a *app, p provider.Provider, path string, params url.Values, label string, dryRun, wait bool, timeout int) error {
	body, err := p.Raw(ctx, "POST", path, params)
	if err != nil {
		return err
	}
	data, err := mutationData(body, label)
	if err != nil {
		return err
	}
	if _, isList := data.([]any); isList {
		return a.renderBody(body, "backup-type", "backup-id", "backup-time", "keep", "protected")
	}
	if err := handleMutationResponse(ctx, a, p, body, label, wait, timeout); err != nil {
		return err
	}
	upid, _ := data.(string)
	if !dryRun || !wait || upid == "" || a.format == "json" || a.format == "yaml" {
		return nil
	}
	h, err := parseTaskArg(upid)
	if err != nil {
		return err
	}
	return streamTaskLog(ctx, p, h, false, 0)
}

func newDatastoreVerifyStartCmd(a *app) *cobra.Command {
	var ns, grp string
	var backupTime int64
	var maxDepth, outdatedAfter int
	var ignoreVerified, wait, noWait bool
	var timeout int
	var set []string
	cmd := &cobra.Command{
		Use:   "start <store>",
		Short: "Verify backups in a datastore (whole store, a namespace, group or snapshot)",
		Example: "  pc datastore verify start store1 --yes\n" +
			"  pc datastore verify start store1 --group vm/100 --yes\n" +
			"  pc datastore verify start store1 --ignore-verified --outdated-after 30 --yes",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			store := args[0]
			base := map[string]string{"ns": ns}
			scope := "datastore " + store
			if grp != "" {
				typ, id, err := parseBackupGroup(grp)
				if err != nil {
					return err
				}
				base["backup-type"], base["backup-id"] = typ, id
				scope = "group " + grp + " in datastore " + store
			}
			if cmd.Flags().Changed("backup-time") {
				if grp == "" {
					return fmt.Errorf("--backup-time needs --group (a snapshot is <type>/<id>/<time>)")
				}
				base["backup-time"] = strconv.FormatInt(backupTime, 10)
				scope = fmt.Sprintf("snapshot %s/%d in datastore %s", grp, backupTime, store)
			}
			if cmd.Flags().Changed("ignore-verified") {
				base["ignore-verified"] = boolParam(ignoreVerified)
			}
			if cmd.Flags().Changed("outdated-after") {
				base["outdated-after"] = strconv.Itoa(outdatedAfter)
			}
			if cmd.Flags().Changed("max-depth") {
				base["max-depth"] = strconv.Itoa(maxDepth)
			}
			if ns != "" {
				scope += " (namespace " + ns + ")"
			}
			params, err := mergeSet(base, set)
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("start verification of %s on %s? (reads every chunk; I/O heavy)", scope, a.pbsTarget())); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "POST", "/admin/datastore/"+store+"/verify", params,
				"verify "+scope, wait && !noWait, timeout)
		},
	}
	cmd.Flags().StringVar(&ns, "ns", "", "namespace (default: root)")
	cmd.Flags().StringVar(&grp, "group", "", "verify only this backup group (<type>/<id>, e.g. vm/100)")
	cmd.Flags().Int64Var(&backupTime, "backup-time", 0, "verify only this snapshot (epoch seconds; needs --group)")
	cmd.Flags().BoolVar(&ignoreVerified, "ignore-verified", false, "skip snapshots already verified (see --outdated-after)")
	cmd.Flags().IntVar(&outdatedAfter, "outdated-after", 0, "re-verify snapshots verified more than N days ago")
	cmd.Flags().IntVar(&maxDepth, "max-depth", 0, "namespace recursion depth")
	cmd.Flags().StringArrayVar(&set, "set", nil, "extra API parameter key=value (repeatable; wins over flags)")
	waitFlags(cmd, &wait, &noWait, &timeout)
	return cmd
}

// pbsJobKinds maps `job list --type` to PBS's job-status endpoints.
var pbsJobKinds = []string{"prune", "verify", "sync"}

func newDatastoreJobListCmd(a *app) *cobra.Command {
	var types []string
	var store string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List prune, verify and sync jobs with their last/next run",
		Long: "Lists PBS jobs from /admin/{prune,verify,sync}. Each job object is the\n" +
			"native PBS object plus a `job-type` key (prune|verify|sync), since one\n" +
			"list mixes the three kinds.",
		Example: "  pc datastore job list\n  pc datastore job list --type verify --store store1",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			kinds := pbsJobKinds
			if len(types) > 0 {
				for _, t := range types {
					if t != "prune" && t != "verify" && t != "sync" {
						return fmt.Errorf("invalid --type %q: want prune, verify or sync", t)
					}
				}
				kinds = types
			}
			q := url.Values{}
			if store != "" {
				q.Set("store", store)
			}
			all := []map[string]any{}
			for _, k := range kinds {
				body, err := p.Raw(cmd.Context(), "GET", "/admin/"+k, q)
				if err != nil {
					return fmt.Errorf("list %s jobs: %w", k, err)
				}
				var rows []map[string]any
				if err := protocol.DecodeData(body, &rows); err != nil {
					return fmt.Errorf("list %s jobs: %w", k, err)
				}
				for _, r := range rows {
					r["job-type"] = k
					all = append(all, r)
				}
			}
			cols := []string{"id", "job-type", "store", "schedule", "last-run-state", "last-run-endtime", "next-run"}
			t := output.Tabular{Columns: cols, Raw: all}
			for _, r := range all {
				row := make([]string, len(cols))
				for i, c := range cols {
					if v, ok := r[c]; ok && v != nil {
						row[i] = formatCell(v)
					}
				}
				t.Rows = append(t.Rows, row)
			}
			return a.render(t)
		},
	}
	cmd.Flags().StringSliceVar(&types, "type", nil, "job kinds to list: prune,verify,sync (default: all)")
	cmd.Flags().StringVar(&store, "store", "", "only jobs for this datastore")
	return markTabular(cmd)
}
