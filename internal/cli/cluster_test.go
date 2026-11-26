package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		if strings.HasSuffix(key, "/tasks") {
			// Node task lists are keyed by path; the check must ask for errors in a window.
			if q := r.URL.Query(); q.Get("errors") != "1" || q.Get("since") == "" {
				http.Error(w, "want errors=1&since=", http.StatusBadRequest)
				return
			}
		} else if q := r.URL.RawQuery; q != "" {
			key += "?" + q
		}
		body, ok := f[key]
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

	// A Ceph failure that isn't "not installed" is an error, not a silent skip.
	f = healthyCluster()
	f["/cluster/ceph/status"] = ""
	rep, _ = runClusterCheck(t, f, "got timeout", "--check", "ceph")
	if c := checkByName(t, rep, "ceph"); c.Status != checkError {
		t.Errorf("ceph timeout: %+v, want error", c)
	}

	// Every read failing still yields a full report, not a crash.
	rep, code = runClusterCheck(t, clusterFixture{}, "")
	if len(rep.Checks) != len(clusterCheckNames) || code != ExitCheckWarn {
		t.Fatalf("all reads failing: %d checks exit %d", len(rep.Checks), code)
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
