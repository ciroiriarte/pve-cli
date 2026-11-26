package cli

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// Check statuses. ok/warn/crit grade health; skip means the check does not
// apply (e.g. no Ceph); error means the API read behind it failed.
const (
	checkOK    = "ok"
	checkWarn  = "warn"
	checkCrit  = "crit"
	checkSkip  = "skip"
	checkError = "error"
)

// clusterCheckNames is the fixed run order of `pc cluster check`.
var clusterCheckNames = []string{"quorum", "storage", "tasks", "ha", "ceph", "pressure"}

// clusterReport is the `pc cluster check -o json|yaml` payload (the documented
// schema): an overall status (ok|warn|crit) plus one entry per check.
type clusterReport struct {
	Status string        `json:"status" yaml:"status"`
	Checks []checkResult `json:"checks" yaml:"checks"`
}

// checkResult is one check's verdict. Items lists only the offending entries
// (always an array, empty when the check is clean).
type checkResult struct {
	Name   string      `json:"name" yaml:"name"`
	Status string      `json:"status" yaml:"status"`
	Detail string      `json:"detail" yaml:"detail"`
	Items  []checkItem `json:"items" yaml:"items"`
	err    error       // underlying read error behind an error status (not serialized)
}

// checkItem is one offending entry: a node, storage, task, HA resource or
// Ceph health code. ID carries the task UPID / HA sid where one exists.
type checkItem struct {
	Name   string `json:"name" yaml:"name"`
	Node   string `json:"node,omitempty" yaml:"node,omitempty"`
	ID     string `json:"id,omitempty" yaml:"id,omitempty"`
	Status string `json:"status" yaml:"status"`
	Detail string `json:"detail" yaml:"detail"`
}

// checkExit carries `pc cluster check`'s non-zero verdict to Execute. The
// report is already on stdout, so Execute prints nothing for it.
type checkExit struct {
	code   int
	status string
}

func (e *checkExit) Error() string { return "cluster check status: " + e.status }

// clusterCheckOpts holds the thresholds and filters of one run.
type clusterCheckOpts struct {
	storageWarn, storageCrit float64
	cpuWarn, cpuCrit         float64
	memWarn, memCrit         float64
	since                    time.Duration
}

// nowFunc is the clock for the --since window (overridden in tests).
var nowFunc = time.Now

func newClusterCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "cluster", Short: "Cluster-wide health"}
	cmd.AddCommand(newClusterCheckCmd(a))
	return cmd
}

func newClusterCheckCmd(a *app) *cobra.Command {
	var o clusterCheckOpts
	var only, skip []string
	var failOn string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Run cluster health checks (quorum, storage, tasks, HA, Ceph, node pressure)",
		Long: "Aggregate the cluster's health into one verdict. Checks:\n" +
			"  quorum    cluster quorum and offline nodes (a standalone node is ok)\n" +
			"  storage   storage usage vs --storage-warn/--storage-crit; unavailable\n" +
			"            storages warn; disabled storages and offline nodes are ignored;\n" +
			"            shared storage is counted once\n" +
			"  tasks     tasks that ended with a failure within --since (status not\n" +
			"            OK/WARNINGS), read from every online node's task list. The\n" +
			"            window is on end time, but PVE filters by start time, so only\n" +
			"            tasks started within --since + 7 days are scanned\n" +
			"  ha        HA resources in error/fence (crit) or freeze/recovery (warn);\n" +
			"            a dead/stale HA manager or LRM warns when HA resources exist\n" +
			"  ceph      Ceph health from the first node that reports one (skip when\n" +
			"            every online node says Ceph is not installed/initialized)\n" +
			"  pressure  per-node CPU/memory vs --cpu-*/--mem-* thresholds\n\n" +
			"Statuses:\n" +
			"  ok     nothing wrong observed\n" +
			"  warn   degraded, or only partially observed: items with an\n" +
			"         \"incomplete: …\" detail mark gaps (a node's tasks unreadable, the\n" +
			"         task page cap hit, missing Sys.Audit, missing node metrics)\n" +
			"  crit   a real problem was observed (outage)\n" +
			"  error  the check could not grade anything (nothing gradable, every read\n" +
			"         failed, or a required inventory read failed)\n" +
			"  skip   not applicable (e.g. no Ceph)\n" +
			"A failed read never aborts the other checks.\n\n" +
			"Privileges: Sys.Audit on /nodes and Datastore.Audit on /storage (e.g. the\n" +
			"PVEAuditor role on /). Without them PVE hides other users' tasks, node\n" +
			"CPU/memory and storages; the check flags that instead of reporting healthy.\n\n" +
			"json/yaml schema: {\"status\": ok|warn|crit, \"checks\": [{\"name\", \"status\",\n" +
			"\"detail\", \"items\": [{\"name\", \"node\"?, \"id\"?, \"status\", \"detail\"}]}]}.\n" +
			"items lists only the offending entries (item status is warn or crit).\n\n" +
			"Exit codes: 0 healthy; 7 warn (unless --fail-on crit) or any check in\n" +
			"error (always, whatever --fail-on); 8 crit. With --fail-on crit, warnings\n" +
			"(incomplete data included) exit 0 by design. No report is printed and the\n" +
			"usual codes apply (2 auth/config, 4 API/transport, ...) when the credentials\n" +
			"are rejected at any point, the API is unreachable before any read succeeds,\n" +
			"or every check failed. A connection drop after that only errors the\n" +
			"affected checks.",
		Example: "  pc cluster check\n" +
			"  pc cluster check -o json | jq '.checks[] | select(.status != \"ok\")'\n" +
			"  pc cluster check --skip tasks --storage-warn 85 --storage-crit 95\n" +
			"  pc cluster check --check quorum,ceph --fail-on crit",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sel, err := selectChecks(only, skip)
			if err != nil {
				return err
			}
			if err := validateEnum("--fail-on", failOn, []string{checkWarn, checkCrit}); err != nil {
				return err
			}
			for _, t := range []struct {
				name       string
				warn, crit float64
			}{{"storage", o.storageWarn, o.storageCrit}, {"cpu", o.cpuWarn, o.cpuCrit}, {"mem", o.memWarn, o.memCrit}} {
				finite := !math.IsNaN(t.warn) && !math.IsInf(t.warn, 0) && !math.IsNaN(t.crit) && !math.IsInf(t.crit, 0)
				if !finite || t.warn < 0 || t.crit > 100 || t.warn > t.crit {
					return fmt.Errorf("invalid --%s-warn/--%s-crit (%g/%g): want 0 <= warn <= crit <= 100", t.name, t.name, t.warn, t.crit)
				}
			}
			if o.since <= 0 {
				return fmt.Errorf("--since must be positive")
			}
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVE(p, "cluster check", "or inspect a remote with `pc remote cluster status <remote>`"); err != nil {
				return err
			}
			rep, fatal := runClusterChecks(cmd.Context(), p, o, sel)
			if fatal != nil {
				return fatal
			}
			if err := allErrored(rep); err != nil {
				return err
			}
			if err := a.render(clusterReportTable(rep)); err != nil {
				return err
			}
			return checkExitFor(rep, failOn)
		},
	}
	f := cmd.Flags()
	f.StringSliceVar(&only, "check", nil, "run only these checks (comma-separated: "+strings.Join(clusterCheckNames, ",")+")")
	f.StringSliceVar(&skip, "skip", nil, "skip these checks (comma-separated)")
	cmd.MarkFlagsMutuallyExclusive("check", "skip")
	f.StringVar(&failOn, "fail-on", checkWarn, "lowest status that exits non-zero: warn|crit")
	f.Float64Var(&o.storageWarn, "storage-warn", 80, "storage usage % that warns")
	f.Float64Var(&o.storageCrit, "storage-crit", 90, "storage usage % that is critical")
	f.Float64Var(&o.cpuWarn, "cpu-warn", 80, "node CPU % that warns")
	f.Float64Var(&o.cpuCrit, "cpu-crit", 95, "node CPU % that is critical")
	f.Float64Var(&o.memWarn, "mem-warn", 85, "node memory % that warns")
	f.Float64Var(&o.memCrit, "mem-crit", 95, "node memory % that is critical")
	f.DurationVar(&o.since, "since", 24*time.Hour, "failed-task lookback window")
	return markTabular(cmd)
}

// selectChecks resolves --check/--skip into the set of checks to run.
func selectChecks(only, skip []string) (map[string]bool, error) {
	known := map[string]bool{}
	for _, n := range clusterCheckNames {
		known[n] = true
	}
	for _, n := range append(append([]string{}, only...), skip...) {
		if !known[n] {
			return nil, fmt.Errorf("unknown check %q: want one of %s", n, strings.Join(clusterCheckNames, ","))
		}
	}
	sel := map[string]bool{}
	for _, n := range clusterCheckNames {
		sel[n] = len(only) == 0
	}
	for _, n := range only {
		sel[n] = true
	}
	for _, n := range skip {
		sel[n] = false
	}
	return sel, nil
}

// runClusterChecks runs the selected checks. Every API read is best-effort: a
// failure turns into that check's error status, never an abort. The returned
// fatal error (kept apart from the per-check results so it can't be lost) is
// set when the run as a whole is untrustworthy: a rejected credential at any
// point, or a transport failure before any request succeeded. Later reads
// short-circuit with it.
func runClusterChecks(ctx context.Context, p provider.Provider, o clusterCheckOpts, sel map[string]bool) (clusterReport, error) {
	var fatal error
	succeeded := false
	raw := func(path string, q url.Values) ([]byte, error) {
		if fatal != nil {
			return nil, fatal
		}
		body, err := p.Raw(ctx, "GET", path, q)
		var apiErr *protocol.APIError
		switch {
		case err == nil:
			succeeded = true
		case errors.As(err, &apiErr) && apiErr.Kind == protocol.KindAuth:
			fatal = err
		case errors.As(err, &apiErr) && apiErr.Kind == protocol.KindTransport && !succeeded:
			fatal = err
		}
		return body, err
	}
	get := func(path string, q url.Values) ([]map[string]any, error) {
		body, err := raw(path, q)
		if err != nil {
			return nil, err
		}
		var rows []map[string]any
		err = protocol.DecodeData(body, &rows)
		return rows, err
	}
	// Node online state feeds the storage, tasks, ceph and pressure checks.
	var nodes []map[string]any
	var nodesErr error
	if sel["storage"] || sel["pressure"] || sel["tasks"] || sel["ceph"] {
		nodes, nodesErr = get("/cluster/resources", url.Values{"type": {"node"}})
	}
	// /cluster/status feeds quorum and names the API node for the Ceph fallback.
	var status []map[string]any
	var statusErr error
	if sel["quorum"] || sel["ceph"] {
		status, statusErr = get("/cluster/status", nil)
	}

	rep := clusterReport{Checks: []checkResult{}}
	for _, name := range clusterCheckNames {
		if !sel[name] {
			continue
		}
		var r checkResult
		switch name {
		case "quorum":
			r = errOr(statusErr, func() checkResult { return checkQuorum(status) })
		case "storage":
			rows, err := get("/cluster/resources", url.Values{"type": {"storage"}})
			if err == nil {
				err = nodesErr
			}
			// Best-effort: without the config we just can't tell disabled storages apart.
			cfg, _ := get("/storage", nil)
			r = errOr(err, func() checkResult { return checkStorage(rows, nodes, cfg, o) })
		case "tasks":
			r = errOr(nodesErr, func() checkResult { return checkTasks(raw, get, nodes, o.since, nowFunc()) })
		case "ha":
			rows, err := get("/cluster/ha/status/current", nil)
			r = errOr(err, func() checkResult { return checkHA(rows) })
		case "ceph":
			apiNode := ""
			for _, s := range status {
				if str(s["type"]) == "node" && num(s["local"]) == 1 {
					apiNode = str(s["name"])
				}
			}
			r = checkCeph(raw, nodes, nodesErr, apiNode)
		case "pressure":
			r = errOr(nodesErr, func() checkResult { return checkPressure(nodes, o) })
		}
		r.Name = name
		if r.Items == nil {
			r.Items = []checkItem{}
		}
		rep.Checks = append(rep.Checks, r)
	}
	rep.Status = overallStatus(rep.Checks)
	return rep, fatal
}

func errOr(err error, fn func() checkResult) checkResult {
	if err != nil {
		return checkResult{Status: checkError, Detail: "API read failed: " + err.Error(), err: err}
	}
	return fn()
}

// allErrored returns the first underlying read error when every check that
// ran failed on an API read: such a report carries no health information, so
// the caller exits with that error's usual code instead of a verdict.
func allErrored(rep clusterReport) error {
	var first error
	ran, errored := 0, 0
	for _, c := range rep.Checks {
		if c.Status == checkSkip {
			continue
		}
		ran++
		if c.Status != checkError || c.err == nil {
			continue
		}
		errored++
		if first == nil {
			first = c.err
		}
	}
	if ran > 0 && errored == ran {
		return first
	}
	return nil
}

// checkExitFor maps a rendered report to its exit: crit → 8; warn → 7 unless
// --fail-on crit; any errored check → at least 7 regardless of --fail-on.
func checkExitFor(rep clusterReport, failOn string) error {
	if rep.Status == checkCrit {
		return &checkExit{code: ExitCheckCrit, status: rep.Status}
	}
	for _, c := range rep.Checks {
		if c.Status == checkError {
			return &checkExit{code: ExitCheckWarn, status: rep.Status}
		}
	}
	if rep.Status == checkWarn && failOn == checkWarn {
		return &checkExit{code: ExitCheckWarn, status: rep.Status}
	}
	return nil
}

func severity(s string) int {
	switch s {
	case checkCrit:
		return 2
	case checkWarn, checkError:
		return 1
	}
	return 0
}

// overallStatus is the worst check status; error counts as warn (an unknown
// health is not a healthy one), skip counts as ok.
func overallStatus(checks []checkResult) string {
	worst := 0
	for _, c := range checks {
		if s := severity(c.Status); s > worst {
			worst = s
		}
	}
	return []string{checkOK, checkWarn, checkCrit}[worst]
}

// grade maps a percentage to ok/warn/crit against inclusive thresholds.
func grade(pct, warn, crit float64) string {
	switch {
	case pct >= crit:
		return checkCrit
	case pct >= warn:
		return checkWarn
	}
	return checkOK
}

// summarize sets r.Status to the worst item status and fills the detail.
func summarize(r checkResult, okDetail string, problem func(n int) string) checkResult {
	r.Status = checkOK
	for _, it := range r.Items {
		st := it.Status
		if st == checkError { // an unreadable item makes the check warn, not error
			st = checkWarn
		}
		if severity(st) > severity(r.Status) {
			r.Status = st
		}
	}
	if len(r.Items) == 0 {
		r.Detail = okDetail
	} else {
		r.Detail = problem(len(r.Items))
	}
	return r
}

// num reads a numeric/boolean API field. PVE sends 0/1 integers for flags such
// as online/quorate/shared while its schema declares booleans, and some fields
// arrive as numeric strings, so accept all three.
func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return f
		}
	}
	return 0
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// shortDuration prints 24h rather than time.Duration's 24h0m0s.
func shortDuration(d time.Duration) string {
	s := d.String()
	if d%time.Minute == 0 {
		s = strings.TrimSuffix(s, "0s")
	}
	if d%time.Hour == 0 {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func pct1(f float64) string { return fmt.Sprintf("%.1f%%", math.Floor(f*10)/10) }

// checkQuorum grades /cluster/status. Without a "cluster" entry the node is
// standalone (not clustered), which is a healthy state, not a failure.
func checkQuorum(rows []map[string]any) checkResult {
	var cluster map[string]any
	var offline []map[string]any
	total := 0
	for _, r := range rows {
		switch str(r["type"]) {
		case "cluster":
			cluster = r
		case "node":
			total++
			if num(r["online"]) != 1 {
				offline = append(offline, r)
			}
		}
	}
	if cluster == nil {
		return checkResult{Status: checkOK, Detail: "standalone node (not clustered)"}
	}
	quorate := num(cluster["quorate"]) == 1
	r := checkResult{Status: checkOK}
	itemStatus := checkWarn
	if !quorate {
		r.Status, itemStatus = checkCrit, checkCrit
	} else if len(offline) > 0 {
		r.Status = checkWarn
	}
	names := make([]string, 0, len(offline))
	for _, n := range offline {
		names = append(names, str(n["name"]))
		r.Items = append(r.Items, checkItem{Name: str(n["name"]), Status: itemStatus, Detail: "node offline"})
	}
	q := "quorate"
	if !quorate {
		q = "NOT quorate"
	}
	r.Detail = fmt.Sprintf("cluster %q %s, %d/%d nodes online", str(cluster["name"]), q, total-len(offline), total)
	if len(names) > 0 {
		r.Detail += "; offline: " + strings.Join(names, ", ")
	}
	return r
}

// checkStorage grades /cluster/resources?type=storage. Disabled storages (per
// /storage) and storages on offline nodes are ignored; a shared storage is
// graded once; an enabled storage that isn't available warns.
func checkStorage(rows, nodes, cfg []map[string]any, o clusterCheckOpts) checkResult {
	disabled := map[string]bool{}
	for _, c := range cfg {
		if num(c["disable"]) == 1 {
			disabled[str(c["storage"])] = true
		}
	}
	offline := map[string]bool{}
	for _, n := range nodes {
		if str(n["status"]) != "online" {
			offline[str(n["node"])] = true
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return str(rows[i]["id"]) < str(rows[j]["id"]) })
	graded := map[string]bool{}
	var r checkResult
	for _, s := range rows {
		name, node := str(s["storage"]), str(s["node"])
		if disabled[name] || offline[node] {
			continue
		}
		if str(s["status"]) != "available" {
			r.Items = append(r.Items, checkItem{Name: name, Node: node, Status: checkWarn,
				Detail: fmt.Sprintf("not available (status %q)", str(s["status"]))})
			continue
		}
		key := node + "/" + name
		if num(s["shared"]) == 1 {
			key = name
		}
		max := num(s["maxdisk"])
		if graded[key] || max <= 0 {
			continue
		}
		graded[key] = true
		used := num(s["disk"]) / max * 100
		if st := grade(used, o.storageWarn, o.storageCrit); st != checkOK {
			it := checkItem{Name: name, Status: st,
				Detail: fmt.Sprintf("%s used (%s of %s)", pct1(used), humanBytes(int64(num(s["disk"]))), humanBytes(int64(max)))}
			if num(s["shared"]) != 1 {
				it.Node = node
			}
			r.Items = append(r.Items, it)
		}
	}
	if len(graded) == 0 {
		// Nothing gradable: no storages listed (e.g. a token without
		// Datastore.Audit) or none reporting usage. That's no verdict, not ok.
		r.Status, r.Detail = checkError, "no storage could be graded (needs Datastore.Audit)"
		if len(r.Items) > 0 {
			r.Detail += fmt.Sprintf("; %s unavailable", plural(len(r.Items), "storage"))
		}
		return r
	}
	return summarize(r,
		fmt.Sprintf("%s below %g%%", plural(len(graded), "storage"), o.storageWarn),
		func(n int) string {
			return fmt.Sprintf("%s over threshold or unavailable (%s graded, warn %g%%, crit %g%%)",
				plural(n, "storage"), plural(len(graded), "storage"), o.storageWarn, o.storageCrit)
		})
}

// Task-list paging: taskPageSize rows per request, at most taskMaxPages
// pages per node. taskSpanSlack widens the server-side `since` (which PVE
// compares against a task's start) so a long task that started before the
// window but ended inside it is still listed; the window itself is applied to
// the end time client-side.
const (
	taskPageSize  = 500
	taskMaxPages  = 20
	taskSpanSlack = 7 * 24 * time.Hour
)

// checkTasks reports tasks that ended with a failure within the window, read
// per online node from /nodes/{node}/tasks (/cluster/tasks only keeps the last
// few dozen tasks, so it misses older failures). It asks PVE for
// statusfilter=error,unknown (falling back to errors=1 on servers without that
// parameter) and pages with start= until the window is exhausted. Running
// tasks (no status yet) and "WARNINGS: N" completions are not failures.
//
// A node whose task list can't be read, hits the page cap, or is read without
// Sys.Audit (PVE then lists only the caller's own tasks) yields a warn item
// "incomplete: …"; no online node, or every node failing to read, is an error
// check.
func checkTasks(raw func(string, url.Values) ([]byte, error), get func(string, url.Values) ([]map[string]any, error), nodes []map[string]any, since time.Duration, now time.Time) checkResult {
	cutoff := now.Add(-since).Unix()
	serverSince := strconv.FormatInt(now.Add(-since-taskSpanSlack).Unix(), 10)
	var r checkResult
	var rows []map[string]any
	var firstErr error
	online, failedReads, incomplete := 0, 0, 0
	for _, n := range nodes {
		if str(n["status"]) != "online" {
			continue
		}
		online++
		name := str(n["node"])
		path := "/nodes/" + url.PathEscape(name) + "/tasks"
		filter := url.Values{"statusfilter": {"error,unknown"}}
		var nodeRows []map[string]any
		var err error
		truncated := true
		for page := 0; page < taskMaxPages; page++ {
			q := url.Values{"since": {serverSince}, "limit": {strconv.Itoa(taskPageSize)}, "start": {strconv.Itoa(page * taskPageSize)}}
			for k, v := range filter {
				q[k] = v
			}
			var got []map[string]any
			got, err = get(path, q)
			var apiErr *protocol.APIError
			if err != nil && page == 0 && filter.Has("statusfilter") && errors.As(err, &apiErr) && apiErr.Kind == protocol.KindValidation {
				filter = url.Values{"errors": {"1"}} // older PVE: no statusfilter
				page--
				continue
			}
			if err != nil {
				break
			}
			nodeRows = append(nodeRows, got...)
			if len(got) < taskPageSize {
				truncated = false
				break
			}
		}
		if err != nil {
			failedReads++
			if firstErr == nil {
				firstErr = err
			}
			r.Items = append(r.Items, checkItem{Name: name, Node: name, Status: checkWarn, Detail: "incomplete: task list read failed: " + err.Error()})
			continue
		}
		if truncated {
			incomplete++
			r.Items = append(r.Items, checkItem{Name: name, Node: name, Status: checkWarn,
				Detail: fmt.Sprintf("incomplete: task list truncated after %d failed tasks", len(nodeRows))})
		}
		if missingPriv(raw, "/nodes/"+name, "Sys.Audit") {
			incomplete++
			r.Items = append(r.Items, checkItem{Name: name, Node: name, Status: checkWarn,
				Detail: "incomplete: no Sys.Audit on /nodes/" + name + ", only the caller's own tasks are listed"})
		}
		for _, t := range nodeRows {
			if str(t["node"]) == "" {
				t["node"] = name
			}
		}
		rows = append(rows, nodeRows...)
	}
	if online == 0 {
		return checkResult{Status: checkError, Detail: "no online node to read tasks from"}
	}
	if failedReads == online {
		return checkResult{Status: checkError, Detail: "API read failed on every node", Items: r.Items, err: firstErr}
	}
	ended := func(t map[string]any) int64 {
		if e := int64(num(t["endtime"])); e > 0 {
			return e
		}
		return int64(num(t["starttime"]))
	}
	sort.SliceStable(rows, func(i, j int) bool { return ended(rows[i]) > ended(rows[j]) })
	failed := 0
	seen := map[string]bool{} // a task can shift across pages while paging
	for _, t := range rows {
		st := str(t["status"])
		if ended(t) < cutoff || st == "" || st == "OK" || strings.HasPrefix(st, "WARNINGS") {
			continue
		}
		if id := str(t["upid"]); id != "" {
			if seen[id] {
				continue
			}
			seen[id] = true
		}
		failed++
		name := strings.TrimSpace(str(t["type"]) + " " + str(t["id"]))
		at := time.Unix(ended(t), 0).UTC().Format(time.RFC3339)
		r.Items = append(r.Items, checkItem{Name: name, Node: str(t["node"]), ID: str(t["upid"]), Status: checkWarn,
			Detail: fmt.Sprintf("%s (ended %s, by %s)", st, at, str(t["user"]))})
	}
	r.Status, r.Detail = checkOK, "no failed tasks in the last "+shortDuration(since)
	if failed > 0 {
		r.Detail = fmt.Sprintf("%s failed in the last %s", plural(failed, "task"), shortDuration(since))
	}
	if len(r.Items) > 0 {
		r.Status = checkWarn
	}
	if failedReads > 0 {
		r.Detail += fmt.Sprintf("; task list unreadable on %s", plural(failedReads, "node"))
	}
	if incomplete > 0 {
		r.Detail += "; results incomplete"
	}
	return r
}

// missingPriv reports whether the caller provably lacks priv on path, via
// /access/permissions. An unreadable answer is not proof, so it returns false.
func missingPriv(raw func(string, url.Values) ([]byte, error), path, priv string) bool {
	body, err := raw("/access/permissions", url.Values{"path": {path}})
	if err != nil {
		return false
	}
	var perms map[string]map[string]any
	if protocol.DecodeData(body, &perms) != nil {
		return false
	}
	privs, ok := perms[path]
	if !ok {
		return false
	}
	_, has := privs[priv]
	return !has
}

// checkHA grades HA services from /cluster/ha/status/current, by the raw CRM
// state (crm_state, else state; status is a display string). With services
// configured, a dead/stale HA manager or LRM warns too.
func checkHA(rows []map[string]any) checkResult {
	var r checkResult
	services := 0
	for _, s := range rows {
		if str(s["type"]) == "service" {
			services++
		}
	}
	for _, s := range rows {
		switch str(s["type"]) {
		case "service":
			state := str(s["crm_state"])
			if state == "" {
				state = str(s["state"])
			}
			if state == "" {
				state = str(s["status"])
			}
			var st string
			switch state {
			case "error", "fence":
				st = checkCrit
			case "freeze", "recovery":
				st = checkWarn
			default:
				continue
			}
			r.Items = append(r.Items, checkItem{Name: str(s["sid"]), Node: str(s["node"]), ID: str(s["sid"]), Status: st,
				Detail: "HA state " + state})
		case "master", "lrm":
			status := str(s["status"])
			if services > 0 && haLivenessProblem(status) {
				r.Items = append(r.Items, checkItem{Name: str(s["type"]), Node: str(s["node"]), Status: checkWarn,
					Detail: "HA " + str(s["type"]) + " " + status})
			}
		}
	}
	okDetail := plural(services, "HA resource") + " healthy"
	if services == 0 {
		okDetail = "no HA resources configured"
	}
	return summarize(r, okDetail, func(n int) string {
		return fmt.Sprintf("%s (%s configured)", plural(n, "HA problem"), plural(services, "HA resource"))
	})
}

// haLivenessProblem matches the parenthesized state of an HA master/LRM status
// line ("<node> (<state>, <time>)") against PVE's stale/dead markers. Only the
// parenthesized part is inspected, so a node named e.g. "deadpool01" can't
// trigger it.
func haLivenessProblem(status string) bool {
	open, closing := strings.Index(status, "("), strings.LastIndex(status, ")")
	if open < 0 || closing <= open {
		return false
	}
	state := status[open+1 : closing]
	for _, marker := range []string{"old timestamp", "dead", "unable to read lrm status", "detected time drift!"} {
		if strings.Contains(state, marker) {
			return true
		}
	}
	return false
}

// cephAbsent reports whether a Ceph status error means "Ceph isn't set up on
// that node" rather than "Ceph is unhealthy/unreachable".
func cephAbsent(err error) bool {
	var apiErr *protocol.APIError
	if errors.As(err, &apiErr) && apiErr.Kind == protocol.KindNotFound {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "binary not installed") || strings.Contains(msg, "pveceph configuration not initialized")
}

// checkCeph grades Ceph health. /cluster/ceph/status only answers for the API
// node's local Ceph, so when it fails, every other online node (apiNode, the
// one already asked, is skipped) is tried via /nodes/{node}/ceph/status. The
// first node that reports a health wins. Otherwise: skip when every node said
// "not installed/initialized" (or 404); error when some node failed in any
// other way, or when the node inventory needed to find peers is unavailable.
func checkCeph(raw func(string, url.Values) ([]byte, error), nodes []map[string]any, nodesErr error, apiNode string) checkResult {
	body, err := raw("/cluster/ceph/status", nil)
	if err != nil {
		if nodesErr != nil {
			if cephAbsent(err) {
				err = nodesErr
			}
			return checkResult{Status: checkError, Detail: "can't look for Ceph on other nodes: node inventory read failed: " + nodesErr.Error() + "; API node: " + err.Error(), err: err}
		}
		var checked []string
		var unexpected error
		if !cephAbsent(err) {
			unexpected = err
		}
		if apiNode != "" {
			checked = append(checked, apiNode)
		}
		found := false
		for _, n := range nodes {
			name := str(n["node"])
			if str(n["status"]) != "online" || name == apiNode {
				continue
			}
			b, nerr := raw("/nodes/"+url.PathEscape(name)+"/ceph/status", nil)
			if nerr == nil {
				body, found = b, true
				break
			}
			if !cephAbsent(nerr) && unexpected == nil {
				unexpected = fmt.Errorf("node %s: %w", name, nerr)
			}
			checked = append(checked, name)
		}
		if !found {
			if unexpected != nil {
				return checkResult{Status: checkError, Detail: "API read failed: " + unexpected.Error(), err: unexpected}
			}
			detail := "Ceph not installed/configured on the API node"
			if len(checked) > 0 {
				detail = "Ceph not installed/configured (checked " + strings.Join(checked, ", ") + ")"
			}
			return checkResult{Status: checkSkip, Detail: detail}
		}
	}
	var st struct {
		Health struct {
			Status string `json:"status"`
			Checks map[string]struct {
				Severity string `json:"severity"`
				Summary  struct {
					Message string `json:"message"`
				} `json:"summary"`
			} `json:"checks"`
		} `json:"health"`
	}
	if err := protocol.DecodeData(body, &st); err != nil {
		return checkResult{Status: checkError, Detail: "API read failed: " + err.Error(), err: err}
	}
	cephStatus := func(h string) string {
		switch h {
		case "HEALTH_OK":
			return checkOK
		case "HEALTH_WARN":
			return checkWarn
		}
		return checkCrit
	}
	r := checkResult{Status: cephStatus(st.Health.Status), Detail: st.Health.Status}
	codes := make([]string, 0, len(st.Health.Checks))
	for c := range st.Health.Checks {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, c := range codes {
		chk := st.Health.Checks[c]
		r.Items = append(r.Items, checkItem{Name: c, Status: cephStatus(chk.Severity), Detail: chk.Summary.Message})
	}
	if len(codes) > 0 {
		r.Detail += ": " + strings.Join(codes, ", ")
	}
	return r
}

// checkPressure grades per-node CPU and memory use of online nodes (offline
// nodes are the quorum check's concern). A node reported without cpu/maxmem
// (e.g. a token lacking Sys.Audit) is flagged, not assumed healthy.
func checkPressure(nodes []map[string]any, o clusterCheckOpts) checkResult {
	sort.SliceStable(nodes, func(i, j int) bool { return str(nodes[i]["node"]) < str(nodes[j]["node"]) })
	var r checkResult
	online, graded := 0, 0
	for _, n := range nodes {
		if str(n["status"]) != "online" {
			continue
		}
		online++
		name := str(n["node"])
		_, hasCPU := n["cpu"]
		max := num(n["maxmem"])
		if !hasCPU || max <= 0 {
			r.Items = append(r.Items, checkItem{Name: name, Node: name, Status: checkWarn,
				Detail: "incomplete: metrics unavailable (cpu/memory not reported; needs Sys.Audit)"})
			continue
		}
		graded++
		cpu := num(n["cpu"]) * 100
		if st := grade(cpu, o.cpuWarn, o.cpuCrit); st != checkOK {
			r.Items = append(r.Items, checkItem{Name: name, Node: name, Status: st, Detail: "cpu " + pct1(cpu)})
		}
		mem := num(n["mem"]) / max * 100
		if st := grade(mem, o.memWarn, o.memCrit); st != checkOK {
			r.Items = append(r.Items, checkItem{Name: name, Node: name, Status: st,
				Detail: fmt.Sprintf("mem %s (%s of %s)", pct1(mem), humanBytes(int64(num(n["mem"]))), humanBytes(int64(max)))})
		}
	}
	if graded == 0 {
		r.Status, r.Detail = checkError, "no node metrics to grade"
		if online > 0 {
			r.Detail = fmt.Sprintf("no node metrics to grade (%s online, none reporting cpu/memory; needs Sys.Audit)", plural(online, "node"))
		}
		r.Items = nil
		return r
	}
	return summarize(r,
		fmt.Sprintf("%s within cpu %g%% / mem %g%%", plural(online, "online node"), o.cpuWarn, o.memWarn),
		func(n int) string { return fmt.Sprintf("%s over threshold or unavailable", plural(n, "metric")) })
}

// clusterReportTable renders one row per check, an indented row per offending
// item, and a final overall row. json/yaml get the native report object.
func clusterReportTable(rep clusterReport) output.Tabular {
	t := output.Tabular{Columns: []string{"check", "status", "detail"}, Raw: rep}
	for _, c := range rep.Checks {
		t.Rows = append(t.Rows, []string{c.Name, strings.ToUpper(c.Status), c.Detail})
		for _, it := range c.Items {
			label := it.Name
			if it.Node != "" && it.Node != it.Name {
				label += "@" + it.Node
			}
			t.Rows = append(t.Rows, []string{c.Name, strings.ToUpper(it.Status), "  " + label + ": " + shorten(it.Detail, 160)})
		}
	}
	t.Rows = append(t.Rows, []string{"overall", strings.ToUpper(rep.Status), ""})
	return t
}
