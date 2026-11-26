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
	if d := managedDescription("before upgrade", "daily"); d != "before upgrade [pc-rotate:daily]" {
		t.Errorf("managedDescription with user text = %q", d)
	}
	if d := managedDescription("", "auto"); !strings.Contains(d, "[pc-rotate:auto]") {
		t.Errorf("managedDescription default = %q", d)
	}
}

func names(s []rotSnap) []string {
	out := []string{}
	for _, x := range s {
		out = append(out, x.Name)
	}
	return out
}

// managed builds a snapshot that carries label's ownership marker.
func managed(name string, snaptime int64, label string) rotSnap {
	return rotSnap{Name: name, Snaptime: snaptime, Description: managedDescription("", label)}
}

// Rotation must only ever select the label's own managed snapshots (name AND
// marker), never hand-made snapshots or look-alikes.
func TestPlanRotationSelection(t *testing.T) {
	m := managedDescription("", "auto")
	snaps := []rotSnap{
		managed("pc-auto-20260103-000000", 3000, "auto"),
		managed("pc-auto-20260101-000000", 1000, "auto"),
		managed("pc-auto-20260102-000000", 2000, "auto"),
		// look-alike names, even with the "auto" marker, never match
		{"pc-auto-other-20260101-000000", 1, m},
		{"pc-autox-20260101-000000", 1, m},
		{"pc-auto-latest", 1, m},
		{"pc-auto-20260101-000000x", 1, m},
		{"xpc-auto-20260101-000000", 1, m},
		{"auto-auto-20260101-000000", 1, m},
		{"pc-auto-2026010-000000", 1, m},
		{"preupgrade", 1, m},
		// a managed-looking name without the marker is hand-made
		{"pc-auto-20250101-000000", 1, "hand made"},
		{"pc-auto-20250102-000000", 1, ""},
		// another label's marker on an "auto" name
		{"pc-auto-20250103-000000", 1, managedDescription("", "auto-x")},
		managed("pc-auto-other-20260101-000001", 1, "auto-other"),
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
	// The "auto-other" label sees only its own marked snapshot.
	kept, del := planRotation(snaps, "auto-other", 0)
	if len(kept) != 0 || !reflect.DeepEqual(names(del), []string{"pc-auto-other-20260101-000001"}) {
		t.Errorf("label auto-other: kept=%v del=%v", names(kept), names(del))
	}
}

// Equal snaptimes fall back to the name (which embeds the timestamp).
func TestPlanRotationTieBreak(t *testing.T) {
	snaps := []rotSnap{
		managed("pc-auto-20260102-000000", 5000, "auto"),
		managed("pc-auto-20260101-000000", 5000, "auto"),
		managed("pc-auto-20260103-000000", 5000, "auto"),
	}
	kept, del := planRotation(snaps, "auto", 1)
	if !reflect.DeepEqual(names(kept), []string{"pc-auto-20260103-000000"}) ||
		!reflect.DeepEqual(names(del), []string{"pc-auto-20260101-000000", "pc-auto-20260102-000000"}) {
		t.Fatalf("tie-break: kept=%v del=%v", names(kept), names(del))
	}
}

// rotServer is a stateful fake PVE with CT 9320 and VM 100 on pve-01 (sharing
// one snapshot store): snapshots can be created and deleted; every mutation is
// recorded.
type rotServer struct {
	mu         sync.Mutex
	snaps      []rotSnap
	calls      []string
	createErr  bool   // POST /snapshot returns HTTP 500
	taskFail   bool   // tasks end with a non-OK exit status
	failDelete string // DELETE of this snapshot returns HTTP 500
	taskStuck  bool   // tasks never finish
	params     map[string]string
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
		if s.taskStuck {
			w.Write([]byte(`{"data":{"status":"running"}}`))
			return
		}
		if s.taskFail {
			w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"snapshot feature is not available"}}`))
			return
		}
		w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
	})
	for _, base := range []string{"/api2/json/nodes/pve-01/lxc/9320", "/api2/json/nodes/pve-01/qemu/100"} {
		base := base
		upid := `{"data":"UPID:pve-01:0001:0002:6A00:snapshot:1:root@pam!t:"}`
		mux.HandleFunc(base+"/snapshot", func(w http.ResponseWriter, r *http.Request) {
			s.mu.Lock()
			defer s.mu.Unlock()
			switch r.Method {
			case http.MethodGet:
				out := []map[string]any{{"name": "current", "description": "You are here!"}}
				for _, x := range s.snaps {
					out = append(out, map[string]any{"name": x.Name, "snaptime": x.Snaptime, "description": x.Description})
				}
				b, _ := json.Marshal(map[string]any{"data": out})
				w.Write(b)
			case http.MethodPost:
				r.ParseForm()
				s.calls = append(s.calls, "POST "+base[len("/api2/json/nodes/pve-01/"):]+" "+r.PostForm.Get("snapname"))
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
					s.snaps = append(s.snaps, rotSnap{r.PostForm.Get("snapname"), time.Now().Unix(), r.PostForm.Get("description")})
				}
				w.Write([]byte(upid))
			}
		})
		mux.HandleFunc(base+"/snapshot/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				http.NotFound(w, r)
				return
			}
			name := strings.TrimPrefix(r.URL.Path, base+"/snapshot/")
			s.mu.Lock()
			defer s.mu.Unlock()
			s.calls = append(s.calls, "DELETE "+name)
			if name == s.failDelete {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"data":null,"message":"snapshot is locked"}`))
				return
			}
			for i, x := range s.snaps {
				if x.Name == name {
					s.snaps = append(s.snaps[:i], s.snaps[i+1:]...)
					break
				}
			}
			w.Write([]byte(upid))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func seedSnaps() []rotSnap {
	return []rotSnap{
		managed("pc-auto-20260101-000000", 1000, "auto"),
		managed("pc-auto-20260102-000000", 2000, "auto"),
		managed("pc-auto-20260103-000000", 3000, "auto"),
		{"handmade", 500, "hand made"},
		{"pc-auto-20250101-000000", 400, "made by hand, looks managed"},
		managed("pc-autox-20260101-000000", 600, "autox"),
		managed("pc-daily-20260101-000000", 700, "daily"),
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
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	return r
}

func TestSnapshotCreateKeepRotates(t *testing.T) {
	s := &rotServer{snaps: seedSnaps()}
	srv := s.start(t)
	out, err := runCLI(t, withCreds(srv, "ct", "snapshot", "create", "9320", "--keep", "2", "--yes", "-o", "json")...)
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
	want := []string{"POST lxc/9320 " + r.Created, "DELETE pc-auto-20260101-000000", "DELETE pc-auto-20260102-000000"}
	if got := s.mutations(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mutations = %v; want %v (create first, then oldest-first deletes, nothing else)", got, want)
	}
	if !strings.Contains(s.params["description"], "[pc-rotate:auto]") {
		t.Errorf("managed snapshot must carry the ownership marker: %v", s.params)
	}
	left := names(s.snaps)
	for _, n := range []string{"handmade", "pc-auto-20250101-000000", "pc-autox-20260101-000000", "pc-daily-20260101-000000"} {
		if !contains(left, n) {
			t.Errorf("%s was removed; remaining %v", n, left)
		}
	}
}

// The marker is appended to a user description, not replaced by it.
func TestSnapshotCreateKeepAppendsMarker(t *testing.T) {
	s := &rotServer{}
	srv := s.start(t)
	if _, err := runCLI(t, withCreds(srv, "ct", "snapshot", "create", "9320", "--keep", "3", "--label", "daily", "--description", "nightly")...); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := s.params["description"]; got != "nightly [pc-rotate:daily]" {
		t.Fatalf("description = %q", got)
	}
}

// A hand-made snapshot with a managed-looking name but no marker survives
// even --keep 0 --no-create.
func TestSnapshotRotateIgnoresUnmarkedLookalike(t *testing.T) {
	s := &rotServer{snaps: []rotSnap{{"pc-auto-20260101-000000", 1000, "manual"}, managed("pc-auto-20260102-000000", 2000, "auto")}}
	srv := s.start(t)
	if _, err := runCLI(t, withCreds(srv, "ct", "snapshot", "rotate", "9320", "--keep", "0", "--no-create", "--yes")...); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if got := s.mutations(); !reflect.DeepEqual(got, []string{"DELETE pc-auto-20260102-000000"}) {
		t.Fatalf("mutations = %v; the unmarked snapshot must not be deleted", got)
	}
}

// #24 review: a managed snapshot with a future snaptime (clock skew) must not
// cause the snapshot just created to be pruned; the new one is pinned.
func TestSnapshotRotateNeverPrunesNewSnapshot(t *testing.T) {
	future := time.Now().Add(24 * time.Hour).Unix()
	s := &rotServer{snaps: []rotSnap{managed("pc-auto-20260101-000000", future, "auto")}}
	srv := s.start(t)
	out, err := runCLI(t, withCreds(srv, "ct", "snapshot", "rotate", "9320", "--keep", "1", "--yes", "-o", "json")...)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	r := decodeRotate(t, out)
	if !reflect.DeepEqual(r.Kept, []string{r.Created}) || !reflect.DeepEqual(r.Deleted, []string{"pc-auto-20260101-000000"}) {
		t.Fatalf("summary: %+v", r)
	}
	for _, c := range s.mutations() {
		if c == "DELETE "+r.Created {
			t.Fatalf("pruned the snapshot just created: %v", s.mutations())
		}
	}
	// The dry-run plan pins the planned entry the same way.
	s2 := &rotServer{snaps: []rotSnap{managed("pc-auto-20260101-000000", future, "auto")}}
	out, err = runCLI(t, withCreds(s2.start(t), "ct", "snapshot", "rotate", "9320", "--keep", "1", "--dry-run", "-o", "json")...)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	r = decodeRotate(t, out)
	if !reflect.DeepEqual(r.Kept, []string{r.Created}) || !reflect.DeepEqual(r.Deleted, []string{"pc-auto-20260101-000000"}) {
		t.Fatalf("dry-run summary: %+v", r)
	}
}

// decision #2: deletions are confirm-gated; non-interactive without --yes
// refuses before anything is created.
func TestSnapshotRotateConfirmGate(t *testing.T) {
	s := &rotServer{snaps: seedSnaps()}
	srv := s.start(t)
	for _, args := range [][]string{
		{"ct", "snapshot", "create", "9320", "--keep", "2"},
		{"ct", "snapshot", "rotate", "9320", "--keep", "2"},
		{"ct", "snapshot", "rotate", "9320", "--keep", "0", "--no-create"},
	} {
		_, err := runCLI(t, withCreds(srv, args...)...)
		if err == nil || !strings.Contains(err.Error(), "--yes") || !strings.Contains(err.Error(), "ct 9320 (pcsnap) on pve-01") {
			t.Errorf("%v: expected a confirm refusal naming the target, got %v", args, err)
		}
	}
	if m := s.mutations(); len(m) != 0 {
		t.Fatalf("refused runs changed something: %v", m)
	}
	// Nothing to delete -> no prompt needed.
	s2 := &rotServer{}
	if _, err := runCLI(t, withCreds(s2.start(t), "ct", "snapshot", "rotate", "9320", "--keep", "2")...); err != nil {
		t.Fatalf("rotate with nothing to prune should not prompt: %v", err)
	}
	if m := s2.mutations(); len(m) != 1 || !strings.HasPrefix(m[0], "POST ") {
		t.Fatalf("mutations = %v", m)
	}
}

func TestSnapshotRotateDryRun(t *testing.T) {
	s := &rotServer{snaps: seedSnaps()}
	srv := s.start(t)
	out, err := runCLI(t, withCreds(srv, "ct", "snapshot", "rotate", "9320", "--keep", "2", "--dry-run", "-o", "json")...)
	if err != nil {
		t.Fatalf("rotate --dry-run: %v", err)
	}
	r := decodeRotate(t, out)
	if !r.DryRun || r.Created == "" {
		t.Errorf("dry-run summary: %+v", r)
	}
	if !reflect.DeepEqual(r.Deleted, []string{"pc-auto-20260101-000000", "pc-auto-20260102-000000"}) {
		t.Errorf("dry-run deleted = %v", r.Deleted)
	}
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
	if _, err := runCLI(t, withCreds(srv, "guest", "snapshot", "rotate", "9320", "--keep", "0", "--no-create", "--yes")...); err != nil {
		t.Fatalf("rotate --keep 0 --no-create: %v", err)
	}
	want := []string{"DELETE pc-auto-20260101-000000", "DELETE pc-auto-20260102-000000", "DELETE pc-auto-20260103-000000"}
	if got := s.mutations(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mutations = %v; want %v", got, want)
	}
}

// `pc guest snapshot` on a VM routes to the qemu path.
func TestGuestSnapshotRotateVM(t *testing.T) {
	s := &rotServer{snaps: []rotSnap{managed("pc-auto-20260101-000000", 1000, "auto")}}
	srv := s.start(t)
	out, err := runCLI(t, withCreds(srv, "guest", "snapshot", "rotate", "100", "--keep", "1", "--vmstate", "--yes", "-o", "json")...)
	if err != nil {
		t.Fatalf("guest snapshot rotate 100: %v", err)
	}
	r := decodeRotate(t, out)
	m := s.mutations()
	if r.Kind != "vm" || len(m) != 2 || m[0] != "POST qemu/100 "+r.Created || m[1] != "DELETE pc-auto-20260101-000000" {
		t.Fatalf("summary %+v, mutations %v", r, m)
	}
	if s.params["vmstate"] != "1" {
		t.Errorf("vmstate not passed for a VM: %v", s.params)
	}
}

// One failing delete doesn't stop the rest; it is reported and exits non-zero.
func TestSnapshotRotateDeleteFailureContinues(t *testing.T) {
	s := &rotServer{snaps: seedSnaps(), failDelete: "pc-auto-20260101-000000"}
	srv := s.start(t)
	out, err := runCLI(t, withCreds(srv, "ct", "snapshot", "rotate", "9320", "--keep", "0", "--no-create", "--yes", "-o", "json")...)
	if err == nil || !strings.Contains(err.Error(), "1 of 3") {
		t.Fatalf("expected a partial-failure error, got %v", err)
	}
	r := decodeRotate(t, out)
	if !reflect.DeepEqual(r.Failed, []string{"pc-auto-20260101-000000"}) ||
		!reflect.DeepEqual(r.Deleted, []string{"pc-auto-20260102-000000", "pc-auto-20260103-000000"}) {
		t.Fatalf("summary: %+v", r)
	}
}

// A failed create — HTTP error or failed task — must prune nothing.
func TestSnapshotRotateCreateFailureNoPrune(t *testing.T) {
	for _, mode := range []string{"http", "task"} {
		s := &rotServer{snaps: seedSnaps(), createErr: mode == "http", taskFail: mode == "task"}
		srv := s.start(t)
		_, err := runCLI(t, withCreds(srv, "ct", "snapshot", "rotate", "9320", "--keep", "1", "--yes")...)
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
		{[]string{"ct", "snapshot", "create", "9320", "x", "--vmstate"}, "only supported for VMs"},
		{[]string{"ct", "snapshot", "rotate", "9320", "--keep", "1", "--no-create", "--vmstate"}, "only supported for VMs"},
		{[]string{"guest", "snapshot", "rotate", "100", "--keep", "1", "--no-create", "--vmstate"}, "--no-create"},
		{[]string{"ct", "snapshot", "rotate", "9320", "--keep", "1", "--no-create", "--description", "x"}, "--no-create"},
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
	name := "pc-daily-20260201-000000"
	if _, err := runCLI(t, withCreds(srv, "ct", "snapshot", "create", "9320", name, "--keep", "5", "--label", "daily")...); err != nil {
		t.Fatalf("matching explicit name: %v", err)
	}
	if got := s.mutations(); !reflect.DeepEqual(got, []string{"POST lxc/9320 " + name}) {
		t.Fatalf("mutations = %v", got)
	}
}

// Plain create (no --keep) writes no marker, so it is never managed.
func TestSnapshotCreatePlainPassThrough(t *testing.T) {
	s := &rotServer{}
	srv := s.start(t)
	if _, err := runCLI(t, withCreds(srv, "ct", "snapshot", "create", "9320", "pc-auto-20260101-000000", "--description", "pre")...); err != nil {
		t.Fatalf("plain create: %v", err)
	}
	if s.params["snapname"] != "pc-auto-20260101-000000" || s.params["description"] != "pre" || s.params["vmstate"] != "" {
		t.Fatalf("params = %v", s.params)
	}
}

// On PDM, rotation goes through resolveGuest: a colliding vmid is refused
// before any mutation, and --remote routes create/list to that remote while
// leaving the unmanaged "old" snapshot alone.
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

// The marker must be the description's terminal token (after trimming
// trailing whitespace/newlines); a marker followed by text doesn't count.
func TestHasMarker(t *testing.T) {
	cases := map[string]bool{
		"pc auto-snapshot [pc-rotate:auto]":           true,
		"pc auto-snapshot [pc-rotate:auto]\n":         true,
		"pc auto-snapshot [pc-rotate:auto]  \t\r\n":   true,
		"nightly run\nsecond line [pc-rotate:auto]\n": true,
		"[pc-rotate:auto] but edited afterwards":      false,
		"[pc-rotate:auto]\nmore notes":                false,
		"pc auto-snapshot [pc-rotate:auto-x]":         false,
		"pc auto-snapshot [pc-rotate:xauto]":          false,
		"":                                            false,
	}
	for desc, want := range cases {
		if got := hasMarker(desc, "auto"); got != want {
			t.Errorf("hasMarker(%q) = %v; want %v", desc, got, want)
		}
	}
	if d := managedDescription("nightly [pc-rotate:auto]\n", "auto"); d != "nightly [pc-rotate:auto]" {
		t.Errorf("marker appended twice: %q", d)
	}
	snaps := []rotSnap{
		{"pc-auto-20260101-000000", 1, "[pc-rotate:auto] edited"},
		{"pc-auto-20260102-000000", 2, "line one\nline two [pc-rotate:auto]\n"},
	}
	_, del := planRotation(snaps, "auto", 0)
	if !reflect.DeepEqual(names(del), []string{"pc-auto-20260102-000000"}) {
		t.Fatalf("selected %v; only the snapshot ending in the marker is managed", names(del))
	}
}

func TestConfirmList(t *testing.T) {
	if got := confirmList([]string{"a", "b"}, 5); got != "a, b" {
		t.Errorf("short list = %q", got)
	}
	if got := confirmList([]string{"a", "b", "c", "d", "e", "f", "g"}, 5); got != "a, b, c, d, e and 2 more" {
		t.Errorf("capped list = %q", got)
	}
}

// A delete that hits --wait-timeout leaves its task running (holding the
// lock), so the rest are not attempted and are reported as such.
func TestSnapshotRotateDeleteTimeoutStops(t *testing.T) {
	s := &rotServer{snaps: seedSnaps(), taskStuck: true}
	srv := s.start(t)
	out, err := runCLI(t, withCreds(srv, "ct", "snapshot", "rotate", "9320", "--keep", "0", "--no-create", "--yes", "--wait-timeout", "1", "-o", "json")...)
	if err == nil || !strings.Contains(err.Error(), "2 not attempted") {
		t.Fatalf("expected a timeout error with not-attempted count, got %v", err)
	}
	if m := s.mutations(); !reflect.DeepEqual(m, []string{"DELETE pc-auto-20260101-000000"}) {
		t.Fatalf("mutations = %v; want only the first delete", m)
	}
	r := decodeRotate(t, out)
	if !reflect.DeepEqual(r.Failed, []string{"pc-auto-20260101-000000"}) ||
		!reflect.DeepEqual(r.NotAttempted, []string{"pc-auto-20260102-000000", "pc-auto-20260103-000000"}) {
		t.Fatalf("summary: %+v", r)
	}
}
