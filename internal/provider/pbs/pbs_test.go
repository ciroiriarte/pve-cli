package pbs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ciroiriarte/pve-cli/internal/auth"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
	"github.com/ciroiriarte/pve-cli/internal/transport"
)

const upid = "UPID:pbs1:000003E8:00001F40:00000002:6740A1B2:garbage_collection:store1:root@pam:"

// newPBS returns a PBS provider against handler, recording request URLs.
func newPBS(t *testing.T, handler http.HandlerFunc) *PBS {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	tok, _ := auth.NewToken("root@pam!t", "s")
	cl, err := transport.New(transport.Options{BaseURL: srv.URL, Auth: tok})
	if err != nil {
		t.Fatal(err)
	}
	return &PBS{cl: cl}
}

func TestListTasksMapsPBSRows(t *testing.T) {
	p := newPBS(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/localhost/tasks" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[` +
			`{"upid":"` + upid + `","node":"localhost","pid":1000,"starttime":5,"endtime":9,"worker_type":"garbage_collection","worker_id":"store1","user":"root@pam","status":"OK"},` +
			`{"upid":"` + upid + `","node":"localhost","starttime":6,"worker_type":"verify","worker_id":"store1","user":"root@pam"},` +
			`{"upid":"` + upid + `","node":"localhost","starttime":7,"endtime":8,"worker_type":"prune","worker_id":"store1","user":"root@pam","status":"unexpected error"}]}`))
	})
	ts, err := p.ListTasks(context.Background(), "ignored")
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.TaskStatus{
		{UPID: upid, Node: "pbs1", Type: "garbage_collection", ID: "store1", User: "root@pam", Status: "stopped", ExitStatus: "OK", StartTime: 5, PID: 1000},
		{UPID: upid, Node: "pbs1", Type: "verify", ID: "store1", User: "root@pam", Status: "running", StartTime: 6},
		{UPID: upid, Node: "pbs1", Type: "prune", ID: "store1", User: "root@pam", Status: "stopped", ExitStatus: "unexpected error", StartTime: 7},
	}
	if len(ts) != len(want) {
		t.Fatalf("got %d tasks, want %d", len(ts), len(want))
	}
	for i := range want {
		if ts[i] != want[i] {
			t.Errorf("task %d = %+v\nwant %+v", i, ts[i], want[i])
		}
	}
}

func TestTaskLogQuery(t *testing.T) {
	var q url.Values
	var path string
	p := newPBS(t, func(w http.ResponseWriter, r *http.Request) {
		q, path = r.URL.Query(), r.URL.Path
		w.Write([]byte(`{"data":[{"n":1,"t":"x"}]}`))
	})
	h := protocol.TaskHandle{UPID: upid}
	if _, err := p.TaskLog(context.Background(), h, provider.LogOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "/nodes/localhost/tasks/"+upid+"/log") {
		t.Errorf("path = %q", path)
	}
	// PBS defaults to 50 lines; unset means all (limit=0).
	if q.Get("limit") != "0" || q.Has("start") {
		t.Errorf("default query = %v, want limit=0 and no start", q)
	}
	if _, err := p.TaskLog(context.Background(), h, provider.LogOptions{Start: 11, Limit: 5}); err != nil {
		t.Fatal(err)
	}
	if q.Get("start") != "10" || q.Get("limit") != "5" {
		t.Errorf("query = %v, want start=10 (0-based) limit=5", q)
	}
}

func TestTaskStatusPath(t *testing.T) {
	var path string
	p := newPBS(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Write([]byte(`{"data":{"upid":"` + upid + `","status":"stopped","exitstatus":"OK","type":"garbage_collection"}}`))
	})
	st, err := p.TaskStatus(context.Background(), protocol.TaskHandle{UPID: upid})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api2/json/nodes/localhost/tasks/"+upid+"/status" || !st.OK() {
		t.Errorf("path %q status %+v", path, st)
	}
}

func TestUnsupportedOpsNameProvider(t *testing.T) {
	p := &PBS{}
	ctx := context.Background()
	_, e1 := p.ListGuests(ctx, provider.GuestFilter{})
	_, e2 := p.ListNodes(ctx)
	_, e3 := p.ListRemotes(ctx)
	for _, err := range []error{e1, e2, e3} {
		if !errors.Is(err, provider.ErrUnsupported) || !strings.Contains(err.Error(), "current provider: pbs") {
			t.Errorf("err = %v, want ErrUnsupported naming pbs", err)
		}
	}
}

func TestRawRoutesMethodParams(t *testing.T) {
	var got *http.Request
	p := newPBS(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r
		w.Write([]byte(`{"data":null}`))
	})
	ctx := context.Background()
	if _, err := p.Raw(ctx, "get", "api2/json/admin/datastore", url.Values{"a": {"1"}}); err != nil {
		t.Fatal(err)
	}
	if got.Method != "GET" || got.URL.Path != "/api2/json/admin/datastore" || got.URL.Query().Get("a") != "1" {
		t.Errorf("GET routed as %s %s ?%s", got.Method, got.URL.Path, got.URL.RawQuery)
	}
	if _, err := p.Raw(ctx, "POST", "/admin/datastore/s/gc", url.Values{"b": {"2"}}); err != nil {
		t.Fatal(err)
	}
	if got.Method != "POST" || got.PostForm.Get("b") != "2" || got.URL.RawQuery != "" {
		t.Errorf("POST routed as %s ?%s form %v", got.Method, got.URL.RawQuery, got.PostForm)
	}
}
