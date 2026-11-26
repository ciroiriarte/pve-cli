package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const (
	pbsGCUPID     = "UPID:pbs:000003E8:00001F40:00000002:6740A1B2:garbage_collection:store1:root@pam!cli:"
	pbsPruneUPID  = "UPID:pbs:000003E8:00001F40:00000003:6740A1B3:prune:store1:root@pam!cli:"
	pbsVerifyUPID = "UPID:pbs:000003E8:00001F40:00000004:6740A1B4:verify:store1:root@pam!cli:"
)

// pbsCall records one request the fake PBS received.
type pbsCall struct {
	Method, Path, Auth string
	Query, Form        url.Values
}

type pbsFake struct {
	*httptest.Server
	mu    sync.Mutex
	calls []pbsCall
}

func (f *pbsFake) last(method, path string) (pbsCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Method == method && f.calls[i].Path == path {
			return f.calls[i], true
		}
	}
	return pbsCall{}, false
}

func (f *pbsFake) count(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Method == method && c.Path == path {
			n++
		}
	}
	return n
}

// pbsServer fakes the PBS API endpoints the curated commands use, recording
// every request (method, path, auth header, query, form).
func pbsServer(t *testing.T) *pbsFake {
	t.Helper()
	f := &pbsFake{}
	routes := map[string]string{
		"GET /version":                                 `{"data":{"version":"4.0.11","release":"4.0","repoid":"abc"}}`,
		"GET /admin/datastore":                         `{"data":[{"store":"store1","comment":"main","backend-type":"filesystem"}]}`,
		"GET /config/datastore/store1":                 `{"data":{"name":"store1","path":"/mnt/datastore/store1","gc-schedule":"daily"}}`,
		"GET /status/datastore-usage":                  `{"data":[{"store":"store1","total":1000,"used":250,"avail":750}]}`,
		"GET /admin/datastore/store1/status":           `{"data":{"total":1000,"used":250,"avail":750,"backend-type":"filesystem"}}`,
		"GET /admin/datastore/store1/namespace":        `{"data":[{"ns":""},{"ns":"prod","comment":"production"}]}`,
		"GET /admin/datastore/store1/groups":           `{"data":[{"backup-type":"vm","backup-id":"100","backup-count":3,"last-backup":1732000000,"owner":"root@pam"}]}`,
		"GET /admin/datastore/store1/snapshots":        `{"data":[{"backup-type":"vm","backup-id":"100","backup-time":1732000000,"size":12345,"protected":false}]}`,
		"GET /admin/datastore/store1/gc":               `{"data":{"store":"store1","last-run-state":"ok","removed-bytes":0}}`,
		"GET /admin/gc":                                `{"data":[{"store":"store1","last-run-state":"ok"}]}`,
		"POST /admin/datastore/store1/gc":              `{"data":"` + pbsGCUPID + `"}`,
		"POST /admin/datastore/store1/prune-datastore": `{"data":"` + pbsPruneUPID + `"}`,
		"POST /admin/datastore/store1/prune":           `{"data":[{"backup-type":"vm","backup-id":"100","backup-time":1732000000,"keep":true},{"backup-type":"vm","backup-id":"100","backup-time":1731000000,"keep":false}]}`,
		"POST /admin/datastore/store1/verify":          `{"data":"` + pbsVerifyUPID + `"}`,
		"GET /admin/prune":                             `{"data":[{"id":"p1","store":"store1","schedule":"daily","keep-last":3}]}`,
		"GET /admin/verify":                            `{"data":[{"id":"v1","store":"store1","schedule":"weekly"}]}`,
		"GET /nodes/localhost/tasks": `{"data":[` +
			`{"upid":"` + pbsGCUPID + `","node":"localhost","pid":1000,"starttime":1732000002,"endtime":1732000010,"worker_type":"garbage_collection","worker_id":"store1","user":"root@pam","status":"OK"},` +
			`{"upid":"` + pbsVerifyUPID + `","node":"localhost","pid":1000,"starttime":1732000004,"worker_type":"verify","worker_id":"store1","user":"root@pam"}]}`,
		"GET /nodes/localhost/tasks/" + pbsGCUPID + "/status":    `{"data":{"upid":"` + pbsGCUPID + `","node":"pbs","type":"garbage_collection","id":"store1","user":"root@pam","status":"stopped","exitstatus":"OK","starttime":1732000002,"pid":1000}}`,
		"GET /nodes/localhost/tasks/" + pbsPruneUPID + "/status": `{"data":{"upid":"` + pbsPruneUPID + `","node":"pbs","type":"prune","status":"stopped","exitstatus":"OK"}}`,
		"GET /nodes/localhost/tasks/" + pbsGCUPID + "/log":       `{"data":[{"n":1,"t":"starting garbage collection on store store1"},{"n":2,"t":"TASK OK"}]}`,
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		path := strings.TrimPrefix(r.URL.Path, "/api2/json")
		f.mu.Lock()
		f.calls = append(f.calls, pbsCall{Method: r.Method, Path: path, Auth: r.Header.Get("Authorization"), Query: r.URL.Query(), Form: r.PostForm})
		f.mu.Unlock()
		if r.Method == "GET" && path == "/admin/sync" {
			w.Write([]byte(pbsSyncJobs(r.URL.Query().Get("sync-direction"))))
			return
		}
		if r.Method == "GET" && path == "/nodes/localhost/tasks/"+pbsPruneUPID+"/log" {
			w.Write([]byte(pbsLongLog(r.URL.Query())))
			return
		}
		if body, ok := routes[r.Method+" "+path]; ok {
			w.Write([]byte(body))
			return
		}
		http.Error(w, `{"data":null,"message":"no such path"}`, http.StatusNotFound)
	}))
	t.Cleanup(f.Close)
	return f
}

func withPBSCreds(srv *pbsFake, args ...string) []string {
	return append([]string{
		"--server", srv.URL, "--provider", "pbs",
		"--token-id", "root@pam!cli", "--token-secret", "secret",
	}, args...)
}

// runJSON runs a command with -o json and decodes the output into v.
func runJSON(t *testing.T, v any, args ...string) {
	t.Helper()
	out, err := runCLI(t, append(args, "-o", "json")...)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("%v: output is not the expected json shape (%v):\n%s", args, err, out)
	}
}

func TestPBSTokenAuthHeaderOnCLIRequests(t *testing.T) {
	srv := pbsServer(t)
	if _, err := runCLI(t, withPBSCreds(srv, "datastore", "list")...); err != nil {
		t.Fatal(err)
	}
	c, ok := srv.last("GET", "/admin/datastore")
	if !ok {
		t.Fatal("GET /admin/datastore not called")
	}
	if want := "PBSAPIToken=root@pam!cli:secret"; c.Auth != want {
		t.Errorf("Authorization = %q, want %q", c.Auth, want)
	}
}

// Every curated read emits the native PBS payload: arrays for lists, objects
// for single-resource reads (design decision #1).
func TestPBSReadsJSONShape(t *testing.T) {
	srv := pbsServer(t)
	arrays := []struct {
		args []string
		key  string // a key the first element must carry
	}{
		{[]string{"datastore", "list"}, "store"},
		{[]string{"datastore", "status"}, "used"},
		{[]string{"datastore", "namespace", "list", "store1"}, "ns"},
		{[]string{"datastore", "group", "list", "store1"}, "backup-count"},
		{[]string{"datastore", "snapshot", "list", "store1"}, "backup-time"},
		{[]string{"datastore", "gc", "status"}, "last-run-state"},
		{[]string{"datastore", "job", "list"}, "job-type"},
	}
	for _, c := range arrays {
		var got []map[string]any
		runJSON(t, &got, withPBSCreds(srv, c.args...)...)
		if len(got) == 0 {
			t.Errorf("%v: empty array", c.args)
			continue
		}
		if _, ok := got[0][c.key]; !ok {
			t.Errorf("%v: first element lacks %q: %v", c.args, c.key, got[0])
		}
	}
	objects := []struct {
		args []string
		key  string
	}{
		{[]string{"datastore", "show", "store1"}, "path"},
		{[]string{"datastore", "status", "store1"}, "avail"},
		{[]string{"datastore", "gc", "status", "store1"}, "removed-bytes"},
	}
	for _, c := range objects {
		var got map[string]any
		runJSON(t, &got, withPBSCreds(srv, c.args...)...)
		if _, ok := got[c.key]; !ok {
			t.Errorf("%v: object lacks %q: %v", c.args, c.key, got)
		}
	}
}

func TestPBSSnapshotListFilters(t *testing.T) {
	srv := pbsServer(t)
	if _, err := runCLI(t, withPBSCreds(srv, "datastore", "snapshot", "list", "store1", "--ns", "prod", "--group", "ct/105")...); err != nil {
		t.Fatal(err)
	}
	c, _ := srv.last("GET", "/admin/datastore/store1/snapshots")
	if c.Query.Get("ns") != "prod" || c.Query.Get("backup-type") != "ct" || c.Query.Get("backup-id") != "105" {
		t.Errorf("snapshot list query = %v", c.Query)
	}
	if _, err := runCLI(t, withPBSCreds(srv, "datastore", "snapshot", "list", "store1", "--group", "bogus")...); err == nil {
		t.Error("expected an error for a malformed --group")
	}
}

func TestPBSJobListMergesKindsAndFilters(t *testing.T) {
	srv := pbsServer(t)
	var all []map[string]any
	runJSON(t, &all, withPBSCreds(srv, "datastore", "job", "list")...)
	kinds := map[any]int{}
	for _, j := range all {
		kinds[j["job-type"]]++
	}
	if kinds["prune"] != 1 || kinds["verify"] != 1 || kinds["sync"] != 2 || len(all) != 4 {
		t.Errorf("job list = %v", all)
	}
	var verify []map[string]any
	runJSON(t, &verify, withPBSCreds(srv, "datastore", "job", "list", "--type", "verify", "--store", "store1")...)
	if len(verify) != 1 || verify[0]["id"] != "v1" {
		t.Errorf("job list --type verify = %v", verify)
	}
	if c, _ := srv.last("GET", "/admin/verify"); c.Query.Get("store") != "store1" {
		t.Errorf("job list --store not forwarded: %v", c.Query)
	}
	if _, err := runCLI(t, withPBSCreds(srv, "datastore", "job", "list", "--type", "tape")...); err == nil {
		t.Error("expected an error for an unknown --type")
	}
}

// Mutations refuse without --yes in non-interactive runs and send nothing.
func TestPBSMutationsAreConfirmGated(t *testing.T) {
	srv := pbsServer(t)
	cases := []struct {
		args       []string
		method, at string
	}{
		{[]string{"datastore", "gc", "start", "store1"}, "POST", "/admin/datastore/store1/gc"},
		{[]string{"datastore", "verify", "start", "store1"}, "POST", "/admin/datastore/store1/verify"},
		{[]string{"datastore", "prune", "store1", "--keep-last", "3"}, "POST", "/admin/datastore/store1/prune-datastore"},
		{[]string{"datastore", "prune", "store1", "--group", "vm/100", "--keep-last", "3"}, "POST", "/admin/datastore/store1/prune"},
	}
	for _, c := range cases {
		_, err := runCLI(t, withPBSCreds(srv, c.args...)...)
		if err == nil || !strings.Contains(err.Error(), "refusing destructive action") {
			t.Errorf("%v: want confirm refusal, got %v", c.args, err)
		}
		if err != nil && !strings.Contains(err.Error(), "store1") {
			t.Errorf("%v: prompt should name the datastore: %v", c.args, err)
		}
		if n := srv.count(c.method, c.at); n != 0 {
			t.Errorf("%v: %s %s was called %d times despite the refusal", c.args, c.method, c.at, n)
		}
	}
}

func TestPBSGCStartReturnsTaskHandle(t *testing.T) {
	srv := pbsServer(t)
	var h map[string]any
	runJSON(t, &h, withPBSCreds(srv, "datastore", "gc", "start", "store1", "--yes")...)
	if h["upid"] != pbsGCUPID || h["backend"] != "pbs" {
		t.Errorf("gc start handle = %v", h)
	}
	if srv.count("POST", "/admin/datastore/store1/gc") != 1 {
		t.Error("POST /admin/datastore/store1/gc not called once")
	}
	// Waiting polls the PBS task endpoint (node "localhost").
	if _, err := runCLI(t, withPBSCreds(srv, "datastore", "gc", "start", "store1", "--yes")...); err != nil {
		t.Fatalf("gc start (wait): %v", err)
	}
	if srv.count("GET", "/nodes/localhost/tasks/"+pbsGCUPID+"/status") == 0 {
		t.Error("waiting did not poll the PBS task status")
	}
}

func TestPBSVerifyStartParams(t *testing.T) {
	srv := pbsServer(t)
	var h map[string]any
	runJSON(t, &h, withPBSCreds(srv, "datastore", "verify", "start", "store1", "--yes",
		"--ns", "prod", "--group", "vm/100", "--ignore-verified", "--outdated-after", "30")...)
	if h["upid"] != pbsVerifyUPID {
		t.Errorf("verify handle = %v", h)
	}
	c, _ := srv.last("POST", "/admin/datastore/store1/verify")
	want := map[string]string{"ns": "prod", "backup-type": "vm", "backup-id": "100", "ignore-verified": "1", "outdated-after": "30"}
	for k, v := range want {
		if c.Form.Get(k) != v {
			t.Errorf("verify form %s = %q, want %q (form %v)", k, c.Form.Get(k), v, c.Form)
		}
	}
	if _, err := runCLI(t, withPBSCreds(srv, "datastore", "verify", "start", "store1", "--yes", "--backup-time", "1")...); err == nil {
		t.Error("--backup-time without --group should be rejected")
	}
}

func TestPBSPrune(t *testing.T) {
	srv := pbsServer(t)

	// No retention: refused before any request.
	if _, err := runCLI(t, withPBSCreds(srv, "datastore", "prune", "store1", "--yes")...); err == nil || !strings.Contains(err.Error(), "no retention") {
		t.Errorf("prune without --keep-*: %v", err)
	}

	// Group dry run: no confirmation needed, synchronous keep/remove list.
	var decisions []map[string]any
	runJSON(t, &decisions, withPBSCreds(srv, "datastore", "prune", "store1", "--group", "vm/100", "--keep-last", "1", "--dry-run")...)
	if len(decisions) != 2 || decisions[1]["keep"] != false {
		t.Errorf("group prune decisions = %v", decisions)
	}
	c, _ := srv.last("POST", "/admin/datastore/store1/prune")
	if c.Form.Get("dry-run") != "1" || c.Form.Get("keep-last") != "1" || c.Form.Get("backup-type") != "vm" || c.Form.Get("backup-id") != "100" {
		t.Errorf("group prune form = %v", c.Form)
	}

	// Datastore prune with --yes runs as a task and returns its handle.
	var h map[string]any
	runJSON(t, &h, withPBSCreds(srv, "datastore", "prune", "store1", "--keep-daily", "7", "--keep-weekly", "4", "--ns", "prod", "--yes")...)
	if h["upid"] != pbsPruneUPID {
		t.Errorf("prune handle = %v", h)
	}
	c, _ = srv.last("POST", "/admin/datastore/store1/prune-datastore")
	if c.Form.Get("keep-daily") != "7" || c.Form.Get("keep-weekly") != "4" || c.Form.Get("ns") != "prod" || c.Form.Get("dry-run") != "" {
		t.Errorf("prune-datastore form = %v", c.Form)
	}

	// Datastore dry run (table output) waits and prints the task log.
	out, err := runCLI(t, withPBSCreds(srv, "datastore", "prune", "store1", "--keep-last", "2", "--dry-run")...)
	if err != nil {
		t.Fatalf("prune --dry-run: %v", err)
	}
	if !strings.Contains(out, "would remove vm/100") {
		t.Errorf("dry-run output should include the task log:\n%s", out)
	}
}

func TestPBSTaskCommands(t *testing.T) {
	srv := pbsServer(t)

	var tasks []map[string]any
	runJSON(t, &tasks, withPBSCreds(srv, "task", "list")...)
	if len(tasks) != 2 {
		t.Fatalf("task list = %v", tasks)
	}
	byType := map[any]map[string]any{}
	for _, tk := range tasks {
		byType[tk["type"]] = tk
	}
	if gc := byType["garbage_collection"]; gc == nil || gc["status"] != "stopped" || gc["exitstatus"] != "OK" || gc["id"] != "store1" || gc["node"] != "pbs" {
		t.Errorf("finished PBS task mapped wrong: %v", gc)
	}
	if v := byType["verify"]; v == nil || v["status"] != "running" {
		t.Errorf("running PBS task mapped wrong: %v", v)
	}

	var st map[string]any
	runJSON(t, &st, withPBSCreds(srv, "task", "show", pbsGCUPID)...)
	if st["exitstatus"] != "OK" || st["type"] != "garbage_collection" {
		t.Errorf("task show = %v", st)
	}

	out, err := runCLI(t, withPBSCreds(srv, "task", "log", pbsGCUPID)...)
	if err != nil || !strings.Contains(out, "starting garbage collection") {
		t.Errorf("task log: err %v out:\n%s", err, out)
	}

	if _, err := runCLI(t, withPBSCreds(srv, "task", "wait", pbsGCUPID)...); err != nil {
		t.Errorf("task wait: %v", err)
	}
}

func TestPBSRawUsesPBSSchema(t *testing.T) {
	srv := pbsServer(t)
	out, err := runCLI(t, withPBSCreds(srv, "raw", "admin", "datastore")...)
	if err != nil {
		t.Fatalf("raw admin datastore: %v", err)
	}
	if !strings.Contains(out, "store1") {
		t.Errorf("raw output:\n%s", out)
	}
	// /admin only exists in the PBS schema; the PVE schema would reject it.
	if _, err := runCLI(t, withCreds(srv.Server, "raw", "admin", "datastore")...); err == nil {
		t.Error("expected the PVE schema to reject /admin/datastore")
	}
}

// Commands that make no sense on PBS fail up front naming the provider, and
// the PBS-only tree refuses other providers (design decision #9).
func TestPBSProviderGate(t *testing.T) {
	srv := pbsServer(t)
	for _, args := range [][]string{
		{"vm", "list"}, {"guest", "list"}, {"node", "list"}, {"storage", "list"}, {"remote", "list"}, {"snapshot", "list"},
	} {
		_, err := runCLI(t, withPBSCreds(srv, args...)...)
		if err == nil || !strings.Contains(err.Error(), "current provider: pbs") {
			t.Errorf("%v on pbs: want a provider error, got %v", args, err)
		}
	}
	srv.mu.Lock()
	n := len(srv.calls)
	srv.mu.Unlock()
	if n != 0 {
		t.Errorf("gated commands reached the server (%d requests)", n)
	}

	// The PDM-through `pc pbs` points at the direct commands.
	_, err := runCLI(t, withPBSCreds(srv, "pbs", "remotes")...)
	if err == nil || !strings.Contains(err.Error(), "pc datastore") {
		t.Errorf("pc pbs on provider pbs: %v", err)
	}

	// pc datastore on PVE / PDM names the current provider.
	for _, prov := range []string{"pve", "pdm"} {
		_, err := runCLI(t, "--server", srv.URL, "--provider", prov, "--token-id", "u@pam!c", "--token-secret", "s", "datastore", "list")
		if err == nil || !strings.Contains(err.Error(), "current provider: "+prov) || !strings.Contains(err.Error(), "pbs") {
			t.Errorf("datastore list on %s: %v", prov, err)
		}
	}
}

func TestPBSHelpFooterAndDatastoreHelp(t *testing.T) {
	t.Setenv("PVE_CLI_SERVER", "https://pbs.example")
	out := helpText(t, "--provider", "pbs", "datastore")
	if !strings.Contains(out, "Active backend: pbs") {
		t.Errorf("help footer should name pbs:\n%s", out)
	}
	// The default port is applied to a port-less PBS server URL.
	if !strings.Contains(out, "https://pbs.example:8007") {
		t.Errorf("footer should show the 8007 default port:\n%s", out)
	}
	// Help is never gated: `pc datastore --help` works on PVE too.
	if out := helpText(t, "datastore"); !strings.Contains(out, "provider: pbs") {
		t.Errorf("datastore help:\n%s", out)
	}
}
