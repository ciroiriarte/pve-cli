package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAutoSnapName(t *testing.T) {
	ts := time.Date(2026, 11, 25, 7, 8, 9, 0, time.FixedZone("x", 3*3600))
	if got := autoSnapName("daily", ts); got != "pc-daily-20261125-040809" {
		t.Fatalf("autoSnapName = %q (want UTC timestamp)", got)
	}
	long := strings.Repeat("a", maxSnapLabelLen)
	if err := validateSnapLabel(long); err != nil {
		t.Fatalf("max-length label rejected: %v", err)
	}
	if n := autoSnapName(long, ts); len(n) != 40 || !autoSnapPattern(long).MatchString(n) {
		t.Fatalf("max-length name %q (len %d) should be 40 chars and match its pattern", n, len(n))
	}
	for _, ok := range []string{"auto", "daily", "hourly_2", "a-b", "7d"} {
		if err := validateSnapLabel(ok); err != nil {
			t.Errorf("validateSnapLabel(%q) = %v; want ok", ok, err)
		}
	}
	for _, bad := range []string{"", "a b", "-x", "_x", "a.b", "a/b", long + "a"} {
		if err := validateSnapLabel(bad); err == nil {
			t.Errorf("validateSnapLabel(%q) = nil; want error", bad)
		}
	}
}

func names(s []rotSnap) []string {
	out := []string{}
	for _, x := range s {
		out = append(out, x.Name)
	}
	return out
}

// Rotation must only ever select the label's own managed names, never
// hand-made snapshots or look-alikes.
func TestPlanRotationSelection(t *testing.T) {
	snaps := []rotSnap{
		{"pc-auto-20260103-000000", 3000},
		{"pc-auto-20260101-000000", 1000},
		{"pc-auto-20260102-000000", 2000},
		// look-alikes that must never be selected for label "auto"
		{"pc-auto-other-20260101-000000", 1},
		{"pc-autox-20260101-000000", 1},
		{"pc-auto-latest", 1},
		{"pc-auto-20260101-000000x", 1},
		{"xpc-auto-20260101-000000", 1},
		{"auto-auto-20260101-000000", 1},
		{"autox", 1},
		{"pc-auto-2026010-000000", 1},
		{"preupgrade", 1},
	}
	cases := []struct {
		keep      int
		kept, del []string
	}{
		{0, []string{}, []string{"pc-auto-20260101-000000", "pc-auto-20260102-000000", "pc-auto-20260103-000000"}},
		{1, []string{"pc-auto-20260103-000000"}, []string{"pc-auto-20260101-000000", "pc-auto-20260102-000000"}},
		{2, []string{"pc-auto-20260102-000000", "pc-auto-20260103-000000"}, []string{"pc-auto-20260101-000000"}},
		{3, []string{"pc-auto-20260101-000000", "pc-auto-20260102-000000", "pc-auto-20260103-000000"}, []string{}},
		{10, []string{"pc-auto-20260101-000000", "pc-auto-20260102-000000", "pc-auto-20260103-000000"}, []string{}},
	}
	for _, c := range cases {
		kept, del := planRotation(snaps, "auto", c.keep)
		if !reflect.DeepEqual(names(kept), c.kept) || !reflect.DeepEqual(names(del), c.del) {
			t.Errorf("keep=%d: kept=%v del=%v; want kept=%v del=%v", c.keep, names(kept), names(del), c.kept, c.del)
		}
	}
	// The "auto-other" label sees only its own snapshot.
	kept, del := planRotation(snaps, "auto-other", 0)
	if len(kept) != 0 || !reflect.DeepEqual(names(del), []string{"pc-auto-other-20260101-000000"}) {
		t.Errorf("label auto-other: kept=%v del=%v", names(kept), names(del))
	}
}

// rotServer is a stateful fake PVE with CT 9320 on pve-01: snapshots can be
// created and deleted; every mutation is recorded.
type rotServer struct {
	mu        sync.Mutex
	snaps     []rotSnap
	calls     []string
	createErr bool // POST /snapshot returns HTTP 500
	taskFail  bool // tasks end with a non-OK exit status
	params    map[string]string
}

func (s *rotServer) mutations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *rotServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[{"type":"lxc","vmid":9320,"name":"pcsnap","node":"pve-01","status":"stopped"},
			{"type":"qemu","vmid":100,"name":"web","node":"pve-01","status":"stopped"}]}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/tasks/", func(w http.ResponseWriter, _ *http.Request) {
		if s.taskFail {
			w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"snapshot feature is not available"}}`))
			return
		}
		w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
	})
	upid := `{"data":"UPID:pve-01:0001:0002:6A00:vzsnapshot:9320:root@pam!t:"}`
	mux.HandleFunc("/api2/json/nodes/pve-01/lxc/9320/snapshot", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			out := []map[string]any{{"name": "current", "description": "You are here!"}}
			for _, x := range s.snaps {
				out = append(out, map[string]any{"name": x.Name, "snaptime": x.Snaptime})
			}
			b, _ := json.Marshal(map[string]any{"data": out})
			w.Write(b)
		case http.MethodPost:
			r.ParseForm()
			s.calls = append(s.calls, "POST "+r.PostForm.Get("snapname"))
			s.params = map[string]string{}
			for k := range r.PostForm {
				s.params[k] = r.PostForm.Get(k)
			}
			if s.createErr {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"data":null,"message":"snapshot failed"}`))
				return
			}
			if !s.taskFail {
				s.snaps = append(s.snaps, rotSnap{r.PostForm.Get("snapname"), time.Now().Unix()})
			}
			w.Write([]byte(upid))
		}
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/lxc/9320/snapshot/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/api2/json/nodes/pve-01/lxc/9320/snapshot/")
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, "DELETE "+name)
		for i, x := range s.snaps {
			if x.Name == name {
				s.snaps = append(s.snaps[:i], s.snaps[i+1:]...)
				break
			}
		}
		w.Write([]byte(upid))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func seedSnaps() []rotSnap {
	return []rotSnap{
		{"pc-auto-20260101-000000", 1000},
		{"pc-auto-20260102-000000", 2000},
		{"pc-auto-20260103-000000", 3000},
		{"handmade", 500},
		{"pc-autox-20260101-000000", 600},
		{"pc-daily-20260101-000000", 700},
	}
}

func decodeRotate(t *testing.T, out string) snapshotRotateResult {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("expected a native JSON object, got %q (%v)", out, err)
	}
	for _, k := range []string{"vmid", "kind", "label", "keep", "dry_run", "kept", "deleted"} {
		if _, ok := obj[k]; !ok {
			t.Errorf("json summary missing %q: %s", k, out)
		}
	}
	var r snapshotRotateResult
	json.Unmarshal([]byte(out), &r)
	return r
}

func TestSnapshotCreateKeepRotates(t *testing.T) {
	s := &rotServer{snaps: seedSnaps()}
	srv := s.start(t)
	out, err := runCLI(t, withCreds(srv, "ct", "snapshot", "create", "9320", "--keep", "2", "-o", "json")...)
	if err != nil {
		t.Fatalf("create --keep 2: %v", err)
	}
	r := decodeRotate(t, out)
	if !autoSnapPattern("auto").MatchString(r.Created) {
		t.Fatalf("created name %q is not a managed name", r.Created)
	}
	if r.VMID != 9320 || r.Kind != "ct" || r.Keep != 2 || r.DryRun {
		t.Errorf("summary fields: %+v", r)
	}
	if !reflect.DeepEqual(r.Kept, []string{"pc-auto-20260103-000000", r.Created}) {
		t.Errorf("kept = %v", r.Kept)
	}
	wantDel := []string{"pc-auto-20260101-000000", "pc-auto-20260102-000000"}
	if !reflect.DeepEqual(r.Deleted, wantDel) {
		t.Errorf("deleted = %v; want %v", r.Deleted, wantDel)
	}
	want := []string{"POST " + r.Created, "DELETE pc-auto-20260101-000000", "DELETE pc-auto-20260102-000000"}
	if got := s.mutations(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mutations = %v; want %v (create first, then oldest-first deletes, nothing else)", got, want)
	}
	if s.params["description"] == "" {
		t.Errorf("managed snapshot should carry a marker description: %v", s.params)
	}
	// The hand-made and look-alike snapshots survived.
	left := names(s.snaps)
	for _, n := range []string{"handmade", "pc-autox-20260101-000000", "pc-daily-20260101-000000"} {
		if !contains(left, n) {
			t.Errorf("%s was removed; remaining %v", n, left)
		}
	}
}

func TestSnapshotRotateDryRun(t *testing.T) {
	s := &rotServer{snaps: seedSnaps()}
	srv := s.start(t)
	out, err := runCLI(t, withCreds(srv, "ct", "snapshot", "rotate", "9320", "--keep", "2", "--dry-run", "-o", "json")...)
	if err != nil {
		t.Fatalf("rotate --dry-run: %v", err)
	}
	if m := s.mutations(); len(m) != 0 {
		t.Fatalf("dry-run sent mutations: %v", m)
	}
	r := decodeRotate(t, out)
	if !r.DryRun || r.Created == "" {
		t.Errorf("dry-run summary: %+v", r)
	}
	if !reflect.DeepEqual(r.Deleted, []string{"pc-auto-20260101-000000", "pc-auto-20260102-000000"}) {
		t.Errorf("dry-run deleted = %v", r.Deleted)
	}
	// prune-only dry run keeps the newest 2 existing
	out, err = runCLI(t, withCreds(srv, "ct", "snapshot", "rotate", "9320", "--keep", "2", "--no-create", "--dry-run", "-o", "json")...)
	if err != nil {
		t.Fatalf("rotate --no-create --dry-run: %v", err)
	}
	r = decodeRotate(t, out)
	if r.Created != "" || !reflect.DeepEqual(r.Deleted, []string{"pc-auto-20260101-000000"}) {
		t.Errorf("no-create dry-run: %+v", r)
	}
	if m := s.mutations(); len(m) != 0 {
		t.Fatalf("dry-run sent mutations: %v", m)
	}
}

func TestSnapshotRotateNoCreateKeepZero(t *testing.T) {
	s := &rotServer{snaps: seedSnaps()}
	srv := s.start(t)
	if _, err := runCLI(t, withCreds(srv, "guest", "snapshot", "rotate", "9320", "--keep", "0", "--no-create")...); err != nil {
		t.Fatalf("rotate --keep 0 --no-create: %v", err)
	}
	want := []string{"DELETE pc-auto-20260101-000000", "DELETE pc-auto-20260102-000000", "DELETE pc-auto-20260103-000000"}
	if got := s.mutations(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mutations = %v; want %v", got, want)
	}
}

// A failed create — HTTP error or failed task — must prune nothing.
func TestSnapshotRotateCreateFailureNoPrune(t *testing.T) {
	for _, mode := range []string{"http", "task"} {
		s := &rotServer{snaps: seedSnaps(), createErr: mode == "http", taskFail: mode == "task"}
		srv := s.start(t)
		_, err := runCLI(t, withCreds(srv, "ct", "snapshot", "rotate", "9320", "--keep", "1")...)
		if err == nil {
			t.Fatalf("%s: expected an error from the failed create", mode)
		}
		for _, c := range s.mutations() {
			if strings.HasPrefix(c, "DELETE") {
				t.Fatalf("%s: pruned after a failed create: %v", mode, s.mutations())
			}
		}
	}
}

func TestSnapshotCreateKeepValidation(t *testing.T) {
	s := &rotServer{snaps: seedSnaps()}
	srv := s.start(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"ct", "snapshot", "create", "9320", "mysnap", "--keep", "2"}, "not managed by label"},
		{[]string{"ct", "snapshot", "create", "9320", "pc-daily-20260101-000000", "--keep", "2"}, "not managed by label"},
		{[]string{"ct", "snapshot", "create", "9320", "--keep", "0"}, "--keep 0"},
		{[]string{"ct", "snapshot", "create", "9320", "--keep", "-1"}, "--keep must be"},
		{[]string{"ct", "snapshot", "create", "9320", "--keep", "2", "--label", "bad label"}, "invalid"},
		{[]string{"ct", "snapshot", "create", "9320", "--keep", "2", "--vmstate"}, "only supported for VMs"},
		{[]string{"ct", "snapshot", "create", "9320"}, "name is required"},
		{[]string{"ct", "snapshot", "create", "9320", "x", "--label", "daily"}, "only applies with --keep"},
		{[]string{"ct", "snapshot", "rotate", "9320"}, "keep"},
	}
	for _, c := range cases {
		_, err := runCLI(t, withCreds(srv, c.args...)...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: expected error containing %q, got %v", c.args, c.want, err)
		}
	}
	if m := s.mutations(); len(m) != 0 {
		t.Fatalf("refused commands sent mutations: %v", m)
	}
	// A matching explicit name is accepted.
	name := "pc-daily-20260201-000000"
	if _, err := runCLI(t, withCreds(srv, "ct", "snapshot", "create", "9320", name, "--keep", "5", "--label", "daily")...); err != nil {
		t.Fatalf("matching explicit name: %v", err)
	}
	if got := s.mutations(); !reflect.DeepEqual(got, []string{"POST " + name}) {
		t.Fatalf("mutations = %v", got)
	}
}

// Plain create (no --keep) is unchanged apart from the new pass-through flags.
func TestSnapshotCreatePlainPassThrough(t *testing.T) {
	s := &rotServer{}
	srv := s.start(t)
	if _, err := runCLI(t, withCreds(srv, "ct", "snapshot", "create", "9320", "before", "--description", "pre")...); err != nil {
		t.Fatalf("plain create: %v", err)
	}
	if s.params["snapname"] != "before" || s.params["description"] != "pre" || s.params["vmstate"] != "" {
		t.Fatalf("params = %v", s.params)
	}
}

// On PDM, rotation goes through resolveGuest: a colliding vmid is refused
// before any mutation, and --remote routes create/list to that remote while
// leaving the non-managed "old" snapshot alone.
func TestSnapshotRotatePDMGuard(t *testing.T) {
	withTempConfig(t)
	srv, log := pdmTargetServerCounting(t)
	defer srv.Close()
	_, err := runCLI(t, withPDMCreds(srv, "vm", "snapshot", "rotate", "300", "--keep", "1")...)
	if err == nil || ExitCodeFor(err) != ExitAPIServer {
		t.Fatalf("colliding vmid: expected conflict, got %v", err)
	}
	if calls := log.snapshot(); len(calls) != 0 {
		t.Fatalf("refused rotate sent mutations: %v", calls)
	}
	out, err := runCLI(t, withPDMCreds(srv, "vm", "snapshot", "rotate", "300", "--keep", "1", "--remote", "dc-west", "-o", "json")...)
	if err != nil {
		t.Fatalf("scoped rotate: %v", err)
	}
	calls := log.snapshot()
	if len(calls) != 1 || calls[0] != "POST /api2/json/pve/remotes/dc-west/qemu/300/snapshot" {
		t.Fatalf("expected one proxied create on dc-west, got %v", calls)
	}
	r := decodeRotate(t, out)
	if r.Remote != "dc-west" || len(r.Deleted) != 0 {
		t.Errorf("summary: %+v", r)
	}
}
