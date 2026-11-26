package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// clusterFixture serves the endpoints `pc cluster check` reads. A path mapped
// to "" returns HTTP 500 with errMsg; unmapped paths 404.
type clusterFixture map[string]string

const (
	healthyStatus     = `[{"type":"cluster","name":"lab","quorate":1,"nodes":3},{"type":"node","name":"pve-01","online":1},{"type":"node","name":"pve-02","online":1},{"type":"node","name":"pve-03","online":1}]`
	healthyNodes      = `[{"type":"node","node":"pve-01","status":"online","cpu":0.10,"mem":10,"maxmem":100},{"type":"node","node":"pve-02","status":"online","cpu":0.20,"mem":20,"maxmem":100},{"type":"node","node":"pve-03","status":"online","cpu":0.30,"mem":30,"maxmem":100}]`
	healthyStorage    = `[{"storage":"local","node":"pve-01","status":"available","shared":0,"disk":10,"maxdisk":100},{"storage":"nfs","node":"pve-01","status":"available","shared":1,"disk":50,"maxdisk":100},{"storage":"nfs","node":"pve-02","status":"available","shared":1,"disk":50,"maxdisk":100}]`
	healthyTasks      = `[{"type":"vzdump","id":"100","node":"pve-01","status":"OK","starttime":1795000000,"upid":"UPID:a"},{"type":"qmstart","id":"101","node":"pve-01","status":"WARNINGS: 1","starttime":1795000000,"upid":"UPID:b"}]`
	healthyHA         = `[{"id":"quorum","type":"quorum","quorate":1,"status":"OK"},{"id":"service:vm:100","type":"service","sid":"vm:100","state":"started","node":"pve-01"}]`
	healthyCeph       = `{"health":{"status":"HEALTH_OK","checks":{}}}`
	clusterFixtureNow = 1795003600 // one hour after the fixture tasks started
)

func healthyCluster() clusterFixture {
	return clusterFixture{
		"/cluster/status":                 healthyStatus,
		"/cluster/resources?type=node":    healthyNodes,
		"/cluster/resources?type=storage": healthyStorage,
		"/storage":                        `[{"storage":"local"},{"storage":"nfs"},{"storage":"old","disable":1}]`,
		"/nodes/pve-01/tasks":             healthyTasks,
		"/nodes/pve-02/tasks":             `[]`,
		"/nodes/pve-03/tasks":             `[]`,
		"/cluster/ha/status/current":      healthyHA,
		"/cluster/ceph/status":            healthyCeph,
	}
}

func (f clusterFixture) server(t *testing.T, errMsg string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/api2/json")
		var start, limit int
		paged := strings.HasSuffix(key, "/tasks")
		if paged {
			// Node task lists are keyed by path and paged by start/limit; the
			// check must ask for failures (statusfilter, or errors=1 on servers
			// without it) since some point in time.
			q := r.URL.Query()
			if _, old := f["!nostatusfilter"]; old && q.Has("statusfilter") {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"data":null,"errors":{"statusfilter":"property is not defined in schema"}}`))
				return
			}
			if (q.Get("statusfilter") != "error,unknown" && q.Get("errors") != "1") || q.Get("since") == "" || q.Get("limit") == "" {
				http.Error(w, "want statusfilter|errors, since, limit", http.StatusTeapot)
				return
			}
			start, _ = strconv.Atoi(q.Get("start"))
			limit, _ = strconv.Atoi(q.Get("limit"))
		} else if q := r.URL.RawQuery; q != "" {
			key += "?" + q
		}
		body, ok := f[key]
		if ok && paged && body != "" {
			var rows []json.RawMessage
			if err := json.Unmarshal([]byte(body), &rows); err != nil {
				t.Errorf("bad fixture %s: %v", key, err)
			}
			end := start + limit
			if start > len(rows) {
				start = len(rows)
			}
			if end > len(rows) {
				end = len(rows)
			}
			b, _ := json.Marshal(rows[start:end])
			body = string(b)
		}
		switch {
		case !ok:
			http.NotFound(w, r)
		case body == "":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"data":null,"message":"` + errMsg + `"}`))
		default:
			w.Write([]byte(`{"data":` + body + `}`))
		}
	}))
}

func runClusterCheck(t *testing.T, f clusterFixture, errMsg string, args ...string) (clusterReport, int) {
	t.Helper()
	old := nowFunc
	nowFunc = func() time.Time { return time.Unix(clusterFixtureNow, 0) }
	defer func() { nowFunc = old }()
	srv := f.server(t, errMsg)
	defer srv.Close()
	out, err := runCLI(t, withCreds(srv, append([]string{"cluster", "check", "-o", "json"}, args...)...)...)
	var rep clusterReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("output is not a json object (err=%v): %v\n%s", err, jerr, out)
	}
	return rep, ExitCodeFor(err)
}

// runClusterCheckErr runs the check against f and returns raw stdout and the
// exit code, for runs that print no report.
func runClusterCheckErr(t *testing.T, f clusterFixture, errMsg string, args ...string) (string, int) {
	t.Helper()
	srv := f.server(t, errMsg)
	defer srv.Close()
	out, err := runCLI(t, withCreds(srv, append([]string{"cluster", "check", "-o", "json"}, args...)...)...)
	return out, ExitCodeFor(err)
}

func checkByName(t *testing.T, rep clusterReport, name string) checkResult {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q missing from %+v", name, rep.Checks)
	return checkResult{}
}

func TestClusterCheckHealthy(t *testing.T) {
	rep, code := runClusterCheck(t, healthyCluster(), "")
	if code != ExitOK || rep.Status != checkOK {
		t.Fatalf("healthy cluster: status %q exit %d, want ok/0: %+v", rep.Status, code, rep)
	}
	if len(rep.Checks) != len(clusterCheckNames) {
		t.Fatalf("want %d checks, got %d", len(clusterCheckNames), len(rep.Checks))
	}
	for _, c := range rep.Checks {
		if c.Status != checkOK || c.Items == nil || len(c.Items) != 0 {
			t.Errorf("check %s = %q items=%v, want ok with empty items", c.Name, c.Status, c.Items)
		}
	}
	// The shared nfs storage is graded once, the disabled one not at all.
	if d := checkByName(t, rep, "storage").Detail; !strings.HasPrefix(d, "2 storages") {
		t.Errorf("storage detail = %q, want 2 storages graded", d)
	}
}

// TestClusterCheckJSONShape pins the documented schema: a top-level object with
// status + checks, every check carrying an items array (never null).
func TestClusterCheckJSONShape(t *testing.T) {
	srv := healthyCluster().server(t, "")
	defer srv.Close()
	out, err := runCLI(t, withCreds(srv, "cluster", "check", "-o", "json")...)
	if err != nil {
		t.Fatalf("cluster check: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("not a json object: %v\n%s", err, out)
	}
	if _, ok := obj["status"].(string); !ok {
		t.Fatalf("missing string status: %s", out)
	}
	checks, ok := obj["checks"].([]any)
	if !ok || len(checks) == 0 {
		t.Fatalf("checks is not a non-empty array: %s", out)
	}
	for _, c := range checks {
		m := c.(map[string]any)
		for _, k := range []string{"name", "status", "detail"} {
			if _, ok := m[k].(string); !ok {
				t.Errorf("check missing %q: %v", k, m)
			}
		}
		if _, ok := m["items"].([]any); !ok {
			t.Errorf("check items is not an array: %v", m)
		}
	}
}

func TestClusterCheckStandaloneIsOK(t *testing.T) {
	f := healthyCluster()
	f["/cluster/status"] = `[{"type":"node","name":"bigiron","online":1,"local":1}]`
	rep, code := runClusterCheck(t, f, "", "--check", "quorum")
	q := checkByName(t, rep, "quorum")
	if q.Status != checkOK || !strings.Contains(q.Detail, "standalone") || code != ExitOK {
		t.Fatalf("standalone: %+v exit %d", q, code)
	}
	if len(rep.Checks) != 1 {
		t.Fatalf("--check quorum ran %d checks", len(rep.Checks))
	}
}

func TestClusterCheckDegraded(t *testing.T) {
	f := healthyCluster()
	f["/cluster/status"] = `[{"type":"cluster","name":"lab","quorate":1},{"type":"node","name":"pve-01","online":1},{"type":"node","name":"pve-02","online":1},{"type":"node","name":"pve-03","online":0}]`
	f["/cluster/resources?type=node"] = `[{"node":"pve-01","status":"online","cpu":0.97,"mem":10,"maxmem":100},{"node":"pve-02","status":"online","cpu":0.1,"mem":88,"maxmem":100},{"node":"pve-03","status":"offline"}]`
	f["/cluster/resources?type=storage"] = `[{"storage":"local","node":"pve-01","status":"available","disk":95,"maxdisk":100},` +
		`{"storage":"nfs","node":"pve-01","status":"available","shared":1,"disk":85,"maxdisk":100},` +
		`{"storage":"nfs","node":"pve-02","status":"available","shared":1,"disk":85,"maxdisk":100},` +
		`{"storage":"iscsi","node":"pve-02","status":"unknown","disk":0,"maxdisk":0},` +
		`{"storage":"local","node":"pve-03","status":"unknown"},` +
		`{"storage":"old","node":"pve-01","status":"unknown"}]`
	f["/nodes/pve-01/tasks"] = `[{"type":"vzdump","id":"100","status":"job errors","starttime":1795003000,"upid":"UPID:fail","user":"root@pam"},` +
		`{"type":"vzdump","id":"100","node":"pve-01","status":"old failure","starttime":1790000000,"upid":"UPID:old"},` +
		`{"type":"qmstart","id":"102","node":"pve-01","starttime":1795003500,"upid":"UPID:running"}]`
	f["/nodes/pve-03/tasks"] = "" // offline: must not be queried
	f["/cluster/ha/status/current"] = `[{"type":"service","sid":"vm:100","state":"error","node":"pve-01"},{"type":"service","sid":"ct:200","state":"freeze","node":"pve-02"},{"type":"service","sid":"vm:101","state":"started"}]`
	f["/cluster/ceph/status"] = `{"health":{"status":"HEALTH_WARN","checks":{"OSD_DOWN":{"severity":"HEALTH_WARN","summary":{"message":"1 osds down"}}}}}`

	rep, code := runClusterCheck(t, f, "")
	if rep.Status != checkCrit || code != ExitCheckCrit {
		t.Fatalf("degraded: status %q exit %d, want crit/%d", rep.Status, code, ExitCheckCrit)
	}
	want := map[string]struct {
		status string
		items  int
	}{
		"quorum":   {checkWarn, 1}, // quorate, one node offline
		"storage":  {checkCrit, 3}, // local 95% crit, nfs once at 85% warn, iscsi unavailable; pve-03 + disabled ignored
		"tasks":    {checkWarn, 1}, // only the in-window finished failure
		"ha":       {checkCrit, 2}, // error crit, freeze warn
		"ceph":     {checkWarn, 1},
		"pressure": {checkCrit, 2}, // pve-01 cpu 97% crit, pve-02 mem 88% warn
	}
	for name, w := range want {
		c := checkByName(t, rep, name)
		if c.Status != w.status || len(c.Items) != w.items {
			t.Errorf("%s: status %q items %d (%+v), want %q/%d", name, c.Status, len(c.Items), c.Items, w.status, w.items)
		}
	}
	if task := checkByName(t, rep, "tasks").Items[0]; task.ID != "UPID:fail" || task.Node != "pve-01" {
		t.Errorf("failed task item = %+v", task)
	}
}

func TestClusterCheckNotQuorateIsCrit(t *testing.T) {
	f := healthyCluster()
	f["/cluster/status"] = `[{"type":"cluster","name":"lab","quorate":0},{"type":"node","name":"pve-01","online":1},{"type":"node","name":"pve-02","online":0},{"type":"node","name":"pve-03","online":0}]`
	rep, code := runClusterCheck(t, f, "", "--check", "quorum")
	if q := checkByName(t, rep, "quorum"); q.Status != checkCrit || len(q.Items) != 2 || code != ExitCheckCrit {
		t.Fatalf("not quorate: %+v exit %d", q, code)
	}
}

// TestClusterCheckAPIErrorIsBestEffort: a failed read degrades only its own
// check to error (counted as warn), and Ceph-not-installed is a skip.
func TestClusterCheckAPIErrorIsBestEffort(t *testing.T) {
	f := healthyCluster()
	f["/nodes/pve-01/tasks"], f["/nodes/pve-02/tasks"], f["/nodes/pve-03/tasks"] = "", "", ""
	f["/cluster/ceph/status"] = ""
	rep, code := runClusterCheck(t, f, "binary not installed: /usr/bin/ceph-mon")
	if c := checkByName(t, rep, "ceph"); c.Status != checkSkip {
		t.Errorf("ceph absent: %+v, want skip", c)
	}
	if c := checkByName(t, rep, "tasks"); c.Status != checkError || !strings.Contains(c.Detail, "API read failed") {
		t.Errorf("tasks read failure: %+v, want error", c)
	}
	if c := checkByName(t, rep, "quorum"); c.Status != checkOK {
		t.Errorf("quorum should be unaffected: %+v", c)
	}
	if rep.Status != checkWarn || code != ExitCheckWarn {
		t.Fatalf("error check: overall %q exit %d, want warn/%d", rep.Status, code, ExitCheckWarn)
	}

	// A Ceph failure that isn't "not installed" is an error, not a silent skip,
	// and an errored check exits 7 even with --fail-on crit.
	for _, msg := range []string{"got timeout", "rados_connect failed - No such file or directory"} {
		f = healthyCluster()
		f["/cluster/ceph/status"] = ""
		rep, code = runClusterCheck(t, f, msg, "--check", "quorum,ceph", "--fail-on", "crit")
		if c := checkByName(t, rep, "ceph"); c.Status != checkError || code != ExitCheckWarn {
			t.Errorf("ceph %q: %+v exit %d, want error/%d", msg, c, code, ExitCheckWarn)
		}
	}

	// Every check erroring: no verdict, the underlying error's exit code.
	out, code := runClusterCheckErr(t, clusterFixture{}, "")
	if out != "" || code != ExitNotFound {
		t.Fatalf("all reads failing: exit %d, out %q; want %d and no report", code, out, ExitNotFound)
	}

	// One node's task list failing: the others are still checked, the check warns.
	f = healthyCluster()
	f["/nodes/pve-02/tasks"] = ""
	rep, code = runClusterCheck(t, f, "boom", "--check", "tasks")
	c := checkByName(t, rep, "tasks")
	if c.Status != checkWarn || len(c.Items) != 1 || c.Items[0].Status != checkError || c.Items[0].Node != "pve-02" || code != ExitCheckWarn {
		t.Fatalf("partial task read failure: %+v exit %d", c, code)
	}
}

func TestClusterCheckFailOnAndFilters(t *testing.T) {
	f := healthyCluster()
	f["/cluster/resources?type=storage"] = `[{"storage":"local","node":"pve-01","status":"available","disk":85,"maxdisk":100}]`
	if _, code := runClusterCheck(t, f, ""); code != ExitCheckWarn {
		t.Errorf("warn default: exit %d, want %d", code, ExitCheckWarn)
	}
	if rep, code := runClusterCheck(t, f, "", "--fail-on", "crit"); code != ExitOK || rep.Status != checkWarn {
		t.Errorf("--fail-on crit with warn: status %q exit %d, want warn/0", rep.Status, code)
	}
	if _, code := runClusterCheck(t, f, "", "--storage-warn", "90", "--storage-crit", "95"); code != ExitOK {
		t.Errorf("raised thresholds: exit %d, want 0", code)
	}
	if _, code := runClusterCheck(t, f, "", "--storage-crit", "85"); code != ExitCheckCrit {
		t.Errorf("crit threshold: exit %d, want %d", code, ExitCheckCrit)
	}
	rep, code := runClusterCheck(t, f, "", "--skip", "storage,ceph")
	if code != ExitOK || len(rep.Checks) != len(clusterCheckNames)-2 {
		t.Errorf("--skip: %d checks exit %d", len(rep.Checks), code)
	}
}

func TestClusterCheckValidation(t *testing.T) {
	srv := healthyCluster().server(t, "")
	defer srv.Close()
	for _, args := range [][]string{
		{"--check", "bogus"},
		{"--check", "ha", "--skip", "ceph"},
		{"--fail-on", "never"},
		{"--storage-warn", "95", "--storage-crit", "90"},
		{"--since", "0s"},
		{"--cpu-warn", "NaN"},
		{"--mem-crit", "Inf"},
		{"--storage-warn", "-Inf"},
		{"--storage-crit", "NaN"},
	} {
		_, err := runCLI(t, withCreds(srv, append([]string{"cluster", "check"}, args...)...)...)
		if err == nil {
			t.Errorf("%v: want error", args)
		}
		if c := ExitCodeFor(err); c == ExitCheckWarn || c == ExitCheckCrit {
			t.Errorf("%v: usage error mapped to a health exit code %d", args, c)
		}
	}
}

func TestClusterCheckRefusedOnPDM(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, err := runCLI(t, withPDMCreds(srv, "cluster", "check")...)
	if err == nil || !strings.Contains(err.Error(), "current provider: pdm") {
		t.Fatalf("pdm: err = %v, want provider-named refusal", err)
	}
}

func TestClusterCheckTable(t *testing.T) {
	f := healthyCluster()
	f["/cluster/resources?type=storage"] = `[{"storage":"local","node":"pve-01","status":"available","disk":85,"maxdisk":100}]`
	old := nowFunc
	nowFunc = func() time.Time { return time.Unix(clusterFixtureNow, 0) }
	defer func() { nowFunc = old }()
	srv := f.server(t, "")
	defer srv.Close()
	out, _ := runCLI(t, withCreds(srv, "cluster", "check")...)
	for _, want := range []string{"CHECK", "quorum", "WARN", "local@pve-01: 85.0% used", "overall", "no failed tasks in the last 24h"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

// TestClusterCheckBooleanFlags: the schema declares online/quorate/shared as
// booleans; real JSON true/false (and numeric strings) must grade like 0/1.
func TestClusterCheckBooleanFlags(t *testing.T) {
	f := healthyCluster()
	f["/cluster/status"] = `[{"type":"cluster","name":"lab","quorate":true},{"type":"node","name":"pve-01","online":true},{"type":"node","name":"pve-02","online":"1"},{"type":"node","name":"pve-03","online":false}]`
	f["/cluster/resources?type=storage"] = `[{"storage":"local","node":"pve-01","status":"available","shared":false,"disk":10,"maxdisk":100},` +
		`{"storage":"nfs","node":"pve-01","status":"available","shared":true,"disk":50,"maxdisk":100},` +
		`{"storage":"nfs","node":"pve-02","status":"available","shared":true,"disk":50,"maxdisk":100}]`
	f["/storage"] = `[{"storage":"old","disable":true}]`
	rep, _ := runClusterCheck(t, f, "", "--check", "quorum,storage")
	q := checkByName(t, rep, "quorum")
	if q.Status != checkWarn || len(q.Items) != 1 || q.Items[0].Name != "pve-03" || !strings.Contains(q.Detail, " quorate, 2/3") {
		t.Errorf("quorum with booleans: %+v", q)
	}
	if s := checkByName(t, rep, "storage"); s.Status != checkOK || !strings.HasPrefix(s.Detail, "2 storages") {
		t.Errorf("storage with shared:true: %+v, want 2 graded", s)
	}
	f["/cluster/status"] = `[{"type":"cluster","name":"lab","quorate":false},{"type":"node","name":"pve-01","online":true}]`
	if rep, code := runClusterCheck(t, f, "", "--check", "quorum"); checkByName(t, rep, "quorum").Status != checkCrit || code != ExitCheckCrit {
		t.Errorf("quorate:false must be crit, got %+v exit %d", rep, code)
	}
}

const clusterFixtureCutoff = clusterFixtureNow - 24*3600

// TestClusterCheckTaskSpanningCutoff: the window applies to the end time, so a
// long task that started before it but failed inside it counts.
func TestClusterCheckTaskSpanningCutoff(t *testing.T) {
	f := healthyCluster()
	f["/nodes/pve-01/tasks"] = fmt.Sprintf(`[{"type":"vzdump","id":"100","status":"job errors","starttime":%d,"endtime":%d,"upid":"UPID:span"},`+
		`{"type":"vzdump","id":"101","status":"old","starttime":%d,"endtime":%d,"upid":"UPID:old"}]`,
		clusterFixtureCutoff-3600, clusterFixtureCutoff+600, clusterFixtureCutoff-7200, clusterFixtureCutoff-60)
	rep, code := runClusterCheck(t, f, "", "--check", "tasks")
	c := checkByName(t, rep, "tasks")
	if c.Status != checkWarn || len(c.Items) != 1 || c.Items[0].ID != "UPID:span" || code != ExitCheckWarn {
		t.Fatalf("spanning task: %+v exit %d", c, code)
	}
}

func warningRows(n int) string {
	rows := make([]string, n)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"type":"qmstart","id":"%d","status":"WARNINGS: 1","starttime":%d,"upid":"UPID:w%d"}`, i, clusterFixtureNow-60, i)
	}
	return strings.Join(rows, ",")
}

// TestClusterCheckTaskPagination: a page full of WARNINGS rows must not hide a
// failure on the next page; hitting the page cap marks the check incomplete.
func TestClusterCheckTaskPagination(t *testing.T) {
	f := healthyCluster()
	f["/nodes/pve-01/tasks"] = `[` + warningRows(taskPageSize) + `,{"type":"vzdump","id":"100","status":"job errors","starttime":1795003000,"upid":"UPID:p2"}]`
	rep, _ := runClusterCheck(t, f, "", "--check", "tasks")
	if c := checkByName(t, rep, "tasks"); len(c.Items) != 1 || c.Items[0].ID != "UPID:p2" {
		t.Errorf("failure on page 2: %+v", c)
	}

	f["/nodes/pve-01/tasks"] = `[` + warningRows(taskPageSize*taskMaxPages) + `]`
	rep, code := runClusterCheck(t, f, "", "--check", "tasks")
	c := checkByName(t, rep, "tasks")
	if c.Status != checkWarn || len(c.Items) != 1 || c.Items[0].Status != checkError ||
		!strings.Contains(c.Items[0].Detail, "truncated") || !strings.Contains(c.Detail, "incomplete") || code != ExitCheckWarn {
		t.Fatalf("page cap: %+v exit %d", c, code)
	}
}

// TestClusterCheckTaskStatusfilterFallback: servers without statusfilter get errors=1.
func TestClusterCheckTaskStatusfilterFallback(t *testing.T) {
	f := healthyCluster()
	f["!nostatusfilter"] = "x"
	f["/nodes/pve-01/tasks"] = `[{"type":"vzdump","id":"100","status":"job errors","starttime":1795003000,"upid":"UPID:f"}]`
	rep, _ := runClusterCheck(t, f, "", "--check", "tasks")
	if c := checkByName(t, rep, "tasks"); len(c.Items) != 1 || c.Items[0].ID != "UPID:f" {
		t.Fatalf("errors=1 fallback: %+v", c)
	}
}

// TestClusterCheckMissingPrivileges: without Sys.Audit, PVE hides other users'
// tasks and node metrics; both are flagged instead of reported healthy.
func TestClusterCheckMissingPrivileges(t *testing.T) {
	f := healthyCluster()
	f["/access/permissions?path=%2Fnodes%2Fpve-02"] = `{"/nodes/pve-02":{"VM.Audit":1}}`
	f["/access/permissions?path=%2Fnodes%2Fpve-01"] = `{"/nodes/pve-01":{"Sys.Audit":1}}`
	f["/cluster/resources?type=node"] = `[{"node":"pve-01","status":"online","cpu":0.1,"mem":10,"maxmem":100},{"node":"pve-02","status":"online"},{"node":"pve-03","status":"online","cpu":0.1,"mem":10,"maxmem":100}]`
	rep, code := runClusterCheck(t, f, "", "--check", "tasks,pressure", "--fail-on", "crit")
	tk := checkByName(t, rep, "tasks")
	if tk.Status != checkWarn || len(tk.Items) != 1 || tk.Items[0].Node != "pve-02" || !strings.Contains(tk.Items[0].Detail, "Sys.Audit") {
		t.Errorf("tasks without Sys.Audit: %+v", tk)
	}
	pr := checkByName(t, rep, "pressure")
	if pr.Status != checkWarn || len(pr.Items) != 1 || pr.Items[0].Node != "pve-02" || !strings.Contains(pr.Items[0].Detail, "metrics unavailable") {
		t.Errorf("pressure without metrics: %+v", pr)
	}
	// Incomplete data is a warning, which --fail-on crit tolerates.
	if code != ExitOK {
		t.Errorf("exit %d, want 0 with --fail-on crit", code)
	}
}

// TestClusterCheckAuthAndTransportFailures: a rejected token or an unreachable
// API is not a health verdict — no report, the usual exit code, and no
// further reads after the first fatal one.
func TestClusterCheckAuthAndTransportFailures(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"data":null}`))
	}))
	defer srv.Close()
	for _, failOn := range []string{"warn", "crit"} {
		hits = 0
		out, err := runCLI(t, withCreds(srv, "cluster", "check", "--fail-on", failOn)...)
		if code := ExitCodeFor(err); code != ExitAuthConfig || out != "" {
			t.Errorf("401 --fail-on %s: exit %d out %q, want %d and no report", failOn, code, out, ExitAuthConfig)
		}
		if hits > 1 {
			t.Errorf("401: %d requests, want reads to stop after the first", hits)
		}
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	out, err := runCLI(t, "--server", url, "--token-id", "u@pam!c", "--token-secret", "secret", "cluster", "check", "--fail-on", "crit")
	if code := ExitCodeFor(err); code != ExitAPIServer || out != "" {
		t.Errorf("refused connection: exit %d out %q, want %d and no report", code, out, ExitAPIServer)
	}
}

func TestClusterCheckCephDetection(t *testing.T) {
	// 404 everywhere: skip, naming the nodes that were tried.
	f := healthyCluster()
	delete(f, "/cluster/ceph/status")
	rep, _ := runClusterCheck(t, f, "", "--check", "ceph")
	if c := checkByName(t, rep, "ceph"); c.Status != checkSkip || !strings.Contains(c.Detail, "pve-01, pve-02, pve-03") {
		t.Errorf("ceph 404: %+v", c)
	}

	// Not initialized on the API node: skip too.
	f = healthyCluster()
	f["/cluster/ceph/status"] = ""
	rep, _ = runClusterCheck(t, f, "pveceph configuration not initialized", "--check", "ceph")
	if c := checkByName(t, rep, "ceph"); c.Status != checkSkip {
		t.Errorf("ceph not initialized: %+v", c)
	}

	// Ceph only on another node: found via /nodes/{node}/ceph/status.
	f = healthyCluster()
	f["/cluster/ceph/status"] = ""
	f["/nodes/pve-02/ceph/status"] = `{"health":{"status":"HEALTH_WARN","checks":{"OSD_DOWN":{"severity":"HEALTH_WARN","summary":{"message":"1 osds down"}}}}}`
	rep, _ = runClusterCheck(t, f, "binary not installed: /usr/bin/ceph-mon", "--check", "ceph")
	if c := checkByName(t, rep, "ceph"); c.Status != checkWarn || len(c.Items) != 1 {
		t.Errorf("ceph on another node: %+v", c)
	}
}

func TestClusterCheckHAStates(t *testing.T) {
	f := healthyCluster()
	f["/cluster/ha/status/current"] = `[` +
		`{"type":"master","node":"pve-01","status":"pve-01 (active, Wed Nov 25 10:00:00 2026)"},` +
		`{"type":"lrm","node":"pve-02","status":"pve-02 (old timestamp - dead?, Wed Nov 25 09:00:00 2026)"},` +
		`{"type":"service","sid":"vm:100","state":"started","crm_state":"fence","node":"pve-02"},` +
		`{"type":"service","sid":"vm:101","crm_state":"recovery","node":"pve-02"},` +
		`{"type":"service","sid":"vm:102","state":"started","node":"pve-01"}]`
	rep, code := runClusterCheck(t, f, "", "--check", "ha")
	c := checkByName(t, rep, "ha")
	got := map[string]string{}
	for _, it := range c.Items {
		got[it.Name] = it.Status
	}
	if c.Status != checkCrit || code != ExitCheckCrit || got["vm:100"] != checkCrit || got["vm:101"] != checkWarn || got["lrm"] != checkWarn || len(c.Items) != 3 {
		t.Fatalf("HA states: %+v exit %d", c, code)
	}

	// A dead LRM without HA resources isn't a problem.
	f["/cluster/ha/status/current"] = `[{"type":"lrm","node":"pve-02","status":"pve-02 (old timestamp - dead?, x)"}]`
	if rep, _ := runClusterCheck(t, f, "", "--check", "ha"); checkByName(t, rep, "ha").Status != checkOK {
		t.Errorf("dead lrm without services: %+v", rep)
	}
}
