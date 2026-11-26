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
			"  tasks     tasks that failed within --since (status not OK/WARNINGS)\n" +
			"  ha        HA resources in error/fence (crit) or freeze/recovery (warn)\n" +
			"  ceph      Ceph health (skip when Ceph is not installed)\n" +
			"  pressure  per-node CPU/memory vs --cpu-*/--mem-* thresholds\n\n" +
			"Each check is ok, warn, crit, skip (not applicable) or error (its API read\n" +
			"failed; counted as warn). A failed read never aborts the other checks.\n\n" +
			"json/yaml schema: {\"status\": ok|warn|crit, \"checks\": [{\"name\", \"status\",\n" +
			"\"detail\", \"items\": [{\"name\", \"node\"?, \"id\"?, \"status\", \"detail\"}]}]}.\n" +
			"items lists only the offending entries.\n\n" +
			"Exit codes: 0 healthy, 7 warn (or error), 8 crit. --fail-on crit exits 0 on\n" +
			"warn. Other codes keep their usual meaning (2 auth/config, 4 API, ...).",
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
				if t.warn < 0 || t.crit > 100 || t.warn > t.crit {
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
			rep := runClusterChecks(cmd.Context(), p, o, sel)
			if err := a.render(clusterReportTable(rep)); err != nil {
				return err
			}
			switch {
			case rep.Status == checkCrit:
				return &checkExit{code: ExitCheckCrit, status: rep.Status}
			case rep.Status == checkWarn && failOn == checkWarn:
				return &checkExit{code: ExitCheckWarn, status: rep.Status}
			}
			return nil
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
// failure turns into that check's error status, never an abort.
func runClusterChecks(ctx context.Context, p provider.Provider, o clusterCheckOpts, sel map[string]bool) clusterReport {
	get := func(path string, q url.Values) ([]map[string]any, error) {
		body, err := p.Raw(ctx, "GET", path, q)
		if err != nil {
			return nil, err
		}
		var rows []map[string]any
		err = protocol.DecodeData(body, &rows)
		return rows, err
	}
	// Node online state feeds both the storage and pressure checks.
	var nodes []map[string]any
	var nodesErr error
	if sel["storage"] || sel["pressure"] || sel["tasks"] {
		nodes, nodesErr = get("/cluster/resources", url.Values{"type": {"node"}})
	}

	rep := clusterReport{Checks: []checkResult{}}
	for _, name := range clusterCheckNames {
		if !sel[name] {
			continue
		}
		var r checkResult
		switch name {
		case "quorum":
			rows, err := get("/cluster/status", nil)
			r = errOr(err, func() checkResult { return checkQuorum(rows) })
		case "storage":
			rows, err := get("/cluster/resources", url.Values{"type": {"storage"}})
			if err == nil {
				err = nodesErr
			}
			// Best-effort: without the config we just can't tell disabled storages apart.
			cfg, _ := get("/storage", nil)
			r = errOr(err, func() checkResult { return checkStorage(rows, nodes, cfg, o) })
		case "tasks":
			r = errOr(nodesErr, func() checkResult { return checkTasks(get, nodes, o.since, nowFunc()) })
		case "ha":
			rows, err := get("/cluster/ha/status/current", nil)
			r = errOr(err, func() checkResult { return checkHA(rows) })
		case "ceph":
			r = checkCeph(ctx, p)
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
	return rep
}

func errOr(err error, fn func() checkResult) checkResult {
	if err != nil {
		return checkResult{Status: checkError, Detail: "API read failed: " + err.Error()}
	}
	return fn()
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
		if severity(it.Status) > severity(r.Status) {
			r.Status = it.Status
		}
	}
	if len(r.Items) == 0 {
		r.Detail = okDetail
	} else {
		r.Detail = problem(len(r.Items))
	}
	return r
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
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
	return summarize(r,
		fmt.Sprintf("%s below %g%%", plural(len(graded), "storage"), o.storageWarn),
		func(n int) string {
			return fmt.Sprintf("%s over threshold or unavailable (%s graded, warn %g%%, crit %g%%)",
				plural(n, "storage"), plural(len(graded), "storage"), o.storageWarn, o.storageCrit)
		})
}

// checkTasks reports tasks that finished with a failure within the window,
// read per online node from /nodes/{node}/tasks?errors=1 (/cluster/tasks only
// keeps the last few dozen tasks, so it misses older failures). Running tasks
// (no status yet) and "WARNINGS: N" completions are not failures. A node whose
// task list can't be read yields an error item; all of them failing is an
// error check.
func checkTasks(get func(string, url.Values) ([]map[string]any, error), nodes []map[string]any, since time.Duration, now time.Time) checkResult {
	cutoff := now.Add(-since).Unix()
	var r checkResult
	var rows []map[string]any
	online, failedReads := 0, 0
	for _, n := range nodes {
		if str(n["status"]) != "online" {
			continue
		}
		online++
		name := str(n["node"])
		got, err := get("/nodes/"+url.PathEscape(name)+"/tasks", url.Values{
			"errors": {"1"}, "since": {strconv.FormatInt(cutoff, 10)}, "limit": {"1000"}})
		if err != nil {
			failedReads++
			r.Items = append(r.Items, checkItem{Name: name, Node: name, Status: checkError, Detail: "task list read failed: " + err.Error()})
			continue
		}
		for _, t := range got {
			if str(t["node"]) == "" {
				t["node"] = name
			}
		}
		rows = append(rows, got...)
	}
	if online > 0 && failedReads == online {
		return checkResult{Status: checkError, Detail: "API read failed on every node", Items: r.Items}
	}
	sort.SliceStable(rows, func(i, j int) bool { return num(rows[i]["starttime"]) > num(rows[j]["starttime"]) })
	for _, t := range rows {
		st := str(t["status"])
		if int64(num(t["starttime"])) < cutoff || st == "" || st == "OK" || strings.HasPrefix(st, "WARNINGS") {
			continue
		}
		name := strings.TrimSpace(str(t["type"]) + " " + str(t["id"]))
		started := time.Unix(int64(num(t["starttime"])), 0).UTC().Format(time.RFC3339)
		r.Items = append(r.Items, checkItem{Name: name, Node: str(t["node"]), ID: str(t["upid"]), Status: checkWarn,
			Detail: fmt.Sprintf("%s (started %s by %s)", st, started, str(t["user"]))})
	}
	failed := len(r.Items) - failedReads
	r.Status, r.Detail = checkOK, "no failed tasks in the last "+shortDuration(since)
	if failed > 0 {
		r.Detail = fmt.Sprintf("%s failed in the last %s", plural(failed, "task"), shortDuration(since))
	}
	if failed > 0 || failedReads > 0 {
		r.Status = checkWarn
	}
	if failedReads > 0 {
		r.Detail += fmt.Sprintf("; task list unreadable on %s", plural(failedReads, "node"))
	}
	return r
}

// checkHA grades HA services from /cluster/ha/status/current.
func checkHA(rows []map[string]any) checkResult {
	var r checkResult
	services := 0
	for _, s := range rows {
		if str(s["type"]) != "service" {
			continue
		}
		services++
		state := str(s["state"])
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
	}
	okDetail := plural(services, "HA resource") + " healthy"
	if services == 0 {
		okDetail = "no HA resources configured"
	}
	return summarize(r, okDetail, func(n int) string {
		return fmt.Sprintf("%s of %d in a bad state", plural(n, "HA resource"), services)
	})
}

// checkCeph grades /cluster/ceph/status. Ceph not being installed/configured
// is a skip, not a failure.
func checkCeph(ctx context.Context, p provider.Provider) checkResult {
	body, err := p.Raw(ctx, "GET", "/cluster/ceph/status", nil)
	if err != nil {
		msg := strings.ToLower(err.Error())
		var apiErr *protocol.APIError
		if (errors.As(err, &apiErr) && apiErr.Kind == protocol.KindNotFound) ||
			strings.Contains(msg, "not installed") || strings.Contains(msg, "not configured") ||
			strings.Contains(msg, "no such file") || strings.Contains(msg, "not initialized") {
			return checkResult{Status: checkSkip, Detail: "Ceph not installed/configured"}
		}
		return checkResult{Status: checkError, Detail: "API read failed: " + err.Error()}
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
		return checkResult{Status: checkError, Detail: "API read failed: " + err.Error()}
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
// nodes are the quorum check's concern).
func checkPressure(nodes []map[string]any, o clusterCheckOpts) checkResult {
	sort.SliceStable(nodes, func(i, j int) bool { return str(nodes[i]["node"]) < str(nodes[j]["node"]) })
	var r checkResult
	online := 0
	for _, n := range nodes {
		if str(n["status"]) != "online" {
			continue
		}
		online++
		name := str(n["node"])
		cpu := num(n["cpu"]) * 100
		if st := grade(cpu, o.cpuWarn, o.cpuCrit); st != checkOK {
			r.Items = append(r.Items, checkItem{Name: name, Node: name, Status: st, Detail: "cpu " + pct1(cpu)})
		}
		if max := num(n["maxmem"]); max > 0 {
			mem := num(n["mem"]) / max * 100
			if st := grade(mem, o.memWarn, o.memCrit); st != checkOK {
				r.Items = append(r.Items, checkItem{Name: name, Node: name, Status: st,
					Detail: fmt.Sprintf("mem %s (%s of %s)", pct1(mem), humanBytes(int64(num(n["mem"]))), humanBytes(int64(max)))})
			}
		}
	}
	return summarize(r,
		fmt.Sprintf("%s within cpu %g%% / mem %g%%", plural(online, "online node"), o.cpuWarn, o.memWarn),
		func(n int) string { return fmt.Sprintf("%s over threshold", plural(n, "metric")) })
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
