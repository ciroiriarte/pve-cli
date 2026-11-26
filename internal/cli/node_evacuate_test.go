package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const gib = int64(1) << 30

// evacFixture is a 3-node cluster for `node evacuate`. pve-01 is the source.
// Memory limit at the default 90%: pve-02 = 28.8GiB (21 used → 7.8 room),
// pve-03 = 28.8GiB (4 used → 24.8 room).
type evacFixture struct {
	guests  []map[string]any // guest entries of /cluster/resources
	ha      []string         // /cluster/ha/resources sids
	pre     map[int]string   // vmid → precondition data JSON override ("501"/"500" = that HTTP error)
	ctConf  map[int]string   // vmid → CT config JSON override
	repl    string           // /cluster/replication entries (JSON, comma-separated)
	postErr map[int]bool     // vmid → migrate POST fails synchronously
	// vmid → migrate POST outcome is unknown to the client: "drop" closes the
	// connection after reading the request, a 3-digit status ("502") answers
	// that status, any other value is sent as a 200 body.
	postOdd map[int]string
	hang    map[int]bool // vmid → migration task never finishes
	slow    map[int]bool // vmid → status poll takes longer than --wait-timeout
	pollErr map[int]bool // vmid → status poll fails (non-terminal)
	// vmid → CT config with ?current=1 (active config; ctConf is the
	// pending-applied view). Unset = same as ctConf.
	ctConfCurrent map[int]string
	liveRunning   map[int]int      // vmid → live running flag override (stale /cluster/resources)
	listErr       map[string]bool  // "qemu"/"lxc" → the node guest list call fails (403)
	listStatus    map[int]string   // vmid → status in the node guest list (overrides the live state)
	fail          map[int]bool     // vmid → migration task ends with an error
	nodes         []map[string]any // override nodes (nil = default 3-node cluster)
	mu            sync.Mutex
	posts         map[int]map[string]string // vmid → POST form
	order         []int                     // POST order
}

func defaultEvacGuests() []map[string]any {
	return []map[string]any{
		{"type": "qemu", "vmid": 100, "name": "big", "node": "pve-01", "status": "running", "maxmem": 16 * gib, "maxcpu": 4},
		{"type": "qemu", "vmid": 101, "name": "mid", "node": "pve-01", "status": "running", "maxmem": 4 * gib, "maxcpu": 2},
		{"type": "lxc", "vmid": 102, "name": "ct", "node": "pve-01", "status": "running", "maxmem": 2 * gib, "maxcpu": 1},
		{"type": "qemu", "vmid": 106, "name": "off", "node": "pve-01", "status": "stopped", "maxmem": 1 * gib, "maxcpu": 1},
		// Guests elsewhere: count toward vCPU allocation, never evacuated.
		{"type": "qemu", "vmid": 200, "name": "other", "node": "pve-02", "status": "running", "maxmem": 1 * gib, "maxcpu": 6},
	}
}

func newEvacFixture(guests []map[string]any) *evacFixture {
	return &evacFixture{guests: guests, pre: map[int]string{}, ctConf: map[int]string{}, postErr: map[int]bool{}, postOdd: map[int]string{},
		hang: map[int]bool{}, slow: map[int]bool{}, pollErr: map[int]bool{}, ctConfCurrent: map[int]string{}, liveRunning: map[int]int{}, listErr: map[string]bool{}, listStatus: map[int]string{},
		fail: map[int]bool{}, posts: map[int]map[string]string{}}
}

func (f *evacFixture) start() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, _ *http.Request) {
		nodes := f.nodes
		if nodes == nil {
			nodes = []map[string]any{
				{"type": "node", "node": "pve-01", "status": "online", "mem": 8 * gib, "maxmem": 64 * gib, "maxcpu": 16},
				{"type": "node", "node": "pve-02", "status": "online", "mem": 21 * gib, "maxmem": 32 * gib, "maxcpu": 8},
				{"type": "node", "node": "pve-03", "status": "online", "mem": 4 * gib, "maxmem": 32 * gib, "maxcpu": 8},
				{"type": "node", "node": "pve-04", "status": "offline"},
			}
		}
		all := append(append([]map[string]any{}, nodes...), f.guests...)
		json.NewEncoder(w).Encode(map[string]any{"data": all})
	})
	mux.HandleFunc("/api2/json/cluster/replication", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"data":[%s]}`, f.repl)
	})
	mux.HandleFunc("/api2/json/storage", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[{"storage":"local-lvm","type":"lvmthin","content":"rootdir,images"},{"storage":"tank","type":"zfspool","nodes":"pve-01,pve-02","content":"rootdir,images"},{"storage":"off","type":"dir","disable":1,"content":"rootdir,images"},{"storage":"isos","type":"dir","content":"iso,vztmpl"},{"storage":"nfs","type":"nfs","shared":1,"nodes":"pve-01","content":"rootdir,images"}]}`))
	})
	mux.HandleFunc("/api2/json/cluster/ha/resources", func(w http.ResponseWriter, _ *http.Request) {
		var out []map[string]string
		for _, sid := range f.ha {
			out = append(out, map[string]string{"sid": sid})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": out})
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api2/json/nodes/pve-01/"), "/")
		// tasks/<upid>/status
		if len(parts) == 3 && parts[0] == "tasks" && parts[2] == "status" {
			fields := strings.Split(parts[1], ":")
			vmid, _ := strconv.Atoi(fields[6])
			if f.slow[vmid] {
				time.Sleep(1500 * time.Millisecond)
			}
			if f.pollErr[vmid] {
				http.Error(w, "proxy error", 595)
				return
			}
			if f.hang[vmid] || f.slow[vmid] {
				w.Write([]byte(`{"data":{"status":"running"}}`))
				return
			}
			if f.fail[vmid] {
				w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"migration aborted"}}`))
				return
			}
			w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
			return
		}
		// {qemu|lxc}: the node's live guest list
		if len(parts) == 1 && (parts[0] == "qemu" || parts[0] == "lxc") {
			if f.listErr[parts[0]] {
				http.Error(w, "Permission check failed", http.StatusForbidden)
				return
			}
			var out []map[string]any
			for _, g := range f.guests {
				if g["type"] != parts[0] || g["node"] != "pve-01" {
					continue
				}
				vmid := g["vmid"].(int)
				st := "stopped"
				if f.running(vmid) == 1 {
					st = "running"
				}
				if s, ok := f.listStatus[vmid]; ok {
					st = s
				}
				out = append(out, map[string]any{"vmid": vmid, "status": st})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": out})
			return
		}
		// lxc/<vmid>/config
		if len(parts) == 3 && parts[0] == "lxc" && parts[2] == "config" {
			vmid, _ := strconv.Atoi(parts[1])
			if c, ok := f.ctConfCurrent[vmid]; ok && r.URL.Query().Get("current") == "1" {
				fmt.Fprintf(w, `{"data":%s}`, c)
				return
			}
			if c, ok := f.ctConf[vmid]; ok {
				fmt.Fprintf(w, `{"data":%s}`, c)
				return
			}
			fmt.Fprintf(w, `{"data":{"rootfs":"local-lvm:vm-%d-disk-0,size=8G"}}`, vmid)
			return
		}
		// {qemu|lxc}/<vmid>/migrate
		if len(parts) == 3 && parts[2] == "migrate" {
			vmid, _ := strconv.Atoi(parts[1])
			if r.Method == http.MethodGet {
				d, ok := f.pre[vmid]
				switch {
				case len(d) == 3 && d[0] >= '4':
					code, _ := strconv.Atoi(d)
					http.Error(w, "precondition unavailable", code)
				case ok:
					fmt.Fprintf(w, `{"data":%s}`, d)
				case parts[0] == "lxc":
					fmt.Fprintf(w, `{"data":{"running":%d,"allowed-nodes":["pve-02","pve-03"],"not-allowed-nodes":{}}}`, f.running(vmid))
				default:
					fmt.Fprintf(w, `{"data":{"running":%d,"allowed_nodes":["pve-02","pve-03"],"not_allowed_nodes":{},"local_disks":[],"local_resources":[]}}`, f.running(vmid))
				}
				return
			}
			if f.postErr[vmid] {
				http.Error(w, "Cannot live-migrate replicated VM", http.StatusInternalServerError)
				return
			}
			r.ParseForm()
			if odd, ok := f.postOdd[vmid]; ok {
				f.mu.Lock()
				f.order = append(f.order, vmid)
				f.mu.Unlock()
				switch odd {
				case "drop":
					conn, _, _ := w.(http.Hijacker).Hijack()
					conn.Close()
				case "502", "503", "504", "595", "596":
					code, _ := strconv.Atoi(odd)
					http.Error(w, "gateway error", code)
				default:
					w.Write([]byte(odd))
				}
				return
			}
			form := map[string]string{}
			for k := range r.PostForm {
				form[k] = r.PostForm.Get(k)
			}
			f.mu.Lock()
			f.posts[vmid] = form
			f.order = append(f.order, vmid)
			f.mu.Unlock()
			fmt.Fprintf(w, `{"data":"UPID:pve-01:00001234:00005678:65000000:qmigrate:%d:root@pam:"}`, vmid)
			return
		}
		http.NotFound(w, r)
	})
	return httptest.NewServer(mux)
}

// running is the default precondition `running` flag: 1 when the guest's
// /cluster/resources status is running, else the liveRunning override.
func (f *evacFixture) running(vmid int) int {
	if v, ok := f.liveRunning[vmid]; ok {
		return v
	}
	for _, g := range f.guests {
		if g["vmid"] == vmid && g["status"] == "running" {
			return 1
		}
	}
	return 0
}

func evacPlan(t *testing.T, out string) map[int]map[string]any {
	t.Helper()
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("plan is not a json array: %v\n%s", err, out)
	}
	byID := map[int]map[string]any{}
	for _, it := range items {
		byID[int(it["vmid"].(float64))] = it
	}
	return byID
}

func TestNodeEvacuatePlanSpreadsLargestFirst(t *testing.T) {
	f := newEvacFixture(defaultEvacGuests())
	srv := f.start()
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "-o", "json")...)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	plan := evacPlan(t, out)
	if len(plan) != 3 {
		t.Fatalf("default plan should hold the 3 running guests only, got %d: %s", len(plan), out)
	}
	// 100 (16GiB) → pve-03 (24.8 room); 101 (4GiB) → pve-03 (8.8 vs 7.8);
	// 102 (2GiB) → pve-02 (7.8 vs 4.8): projected usage is updated per assignment.
	want := map[int][2]string{100: {"pve-03", "online"}, 101: {"pve-03", "online"}, 102: {"pve-02", "restart"}}
	for id, w := range want {
		it := plan[id]
		if it["to"] != w[0] || it["mode"] != w[1] || it["skip"] != false {
			t.Errorf("vmid %d: got to=%v mode=%v skip=%v, want %s/%s", id, it["to"], it["mode"], it["skip"], w[0], w[1])
		}
		for _, k := range []string{"vmid", "name", "kind", "from", "to", "mode", "skip"} {
			if _, ok := it[k]; !ok {
				t.Errorf("vmid %d: plan object missing %q", id, k)
			}
		}
	}
	if len(f.order) != 0 {
		t.Errorf("dry-run must not migrate, got POSTs for %v", f.order)
	}
}

func TestNodeEvacuateAllIncludesStoppedOffline(t *testing.T) {
	f := newEvacFixture(defaultEvacGuests())
	srv := f.start()
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "--all", "-o", "json")...)
	if err != nil {
		t.Fatalf("dry-run --all: %v", err)
	}
	it := evacPlan(t, out)[106]
	if it == nil || it["mode"] != "offline" || it["to"] == nil {
		t.Fatalf("stopped VM 106 should be planned offline, got %v", it)
	}
}

func TestNodeEvacuateTargetForcesDestination(t *testing.T) {
	guests := defaultEvacGuests()[:2] // 100 (16GiB) and 101 (4GiB)
	f := newEvacFixture(guests)
	srv := f.start()
	defer srv.Close()

	// pve-02 has 7.8GiB of room: 101 fits, 100 does not.
	out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--target", "pve-02", "--dry-run", "-o", "json")...)
	if err == nil || ExitCodeFor(err) != ExitValidation {
		t.Fatalf("an unplaceable guest must exit %d, got %v", ExitValidation, err)
	}
	plan := evacPlan(t, out)
	if plan[101]["to"] != "pve-02" {
		t.Errorf("101 should be forced to pve-02: %v", plan[101])
	}
	if plan[100]["skip"] != true || !strings.Contains(plan[100]["reason"].(string), "insufficient memory") {
		t.Errorf("100 should be skipped for capacity: %v", plan[100])
	}

	// --force skips the capacity check.
	out, err = runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--target", "pve-02", "--force", "--dry-run", "-o", "json")...)
	if err != nil {
		t.Fatalf("--force dry-run: %v", err)
	}
	if plan := evacPlan(t, out); plan[100]["to"] != "pve-02" {
		t.Errorf("--force should place 100 on pve-02: %v", plan[100])
	}

	// A bad target is rejected up front.
	for _, bad := range []string{"pve-01", "pve-04", "nope"} {
		if _, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--target", bad, "--dry-run")...); err == nil {
			t.Errorf("--target %s should be rejected", bad)
		}
	}
}

func TestNodeEvacuateInsufficientCapacity(t *testing.T) {
	guests := []map[string]any{
		{"type": "qemu", "vmid": 100, "name": "huge", "node": "pve-01", "status": "running", "maxmem": 40 * gib, "maxcpu": 4},
	}
	f := newEvacFixture(guests)
	srv := f.start()
	defer srv.Close()
	out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "-o", "json")...)
	if err == nil || ExitCodeFor(err) != ExitValidation {
		t.Fatalf("want exit %d, got %v", ExitValidation, err)
	}
	if it := evacPlan(t, out)[100]; it["skip"] != true || !strings.Contains(it["reason"].(string), "insufficient memory") {
		t.Errorf("40GiB guest should not fit: %v", it)
	}

	// A low --max-mem-pct makes even a small guest not fit.
	f.guests[0]["maxmem"] = 2 * gib
	if _, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "--max-mem-pct", "10")...); err == nil {
		t.Error("--max-mem-pct 10 should leave no room")
	}

	// vCPU ratio: pve-02 has 6 vCPUs on 8 cores, pve-03 has 0. A 20-vCPU guest
	// exceeds 2.0 * 8 = 16 on both.
	f.guests[0]["maxcpu"] = 20
	out, err = runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "--vcpu-ratio", "2", "-o", "json")...)
	if err == nil {
		t.Fatal("--vcpu-ratio 2 should reject a 20-vCPU guest")
	}
	if it := evacPlan(t, out)[100]; !strings.Contains(it["reason"].(string), "vCPU") {
		t.Errorf("want a vCPU reason, got %v", it)
	}
}

func TestNodeEvacuateSkips(t *testing.T) {
	guests := []map[string]any{
		{"type": "qemu", "vmid": 103, "name": "ha", "node": "pve-01", "status": "running", "maxmem": gib, "hastate": "started"},
		{"type": "lxc", "vmid": 109, "name": "ha-cfg", "node": "pve-01", "status": "running", "maxmem": gib},
		{"type": "qemu", "vmid": 104, "name": "tpl", "node": "pve-01", "status": "stopped", "maxmem": gib, "template": 1},
		{"type": "qemu", "vmid": 105, "name": "gpu", "node": "pve-01", "status": "running", "maxmem": gib},
		{"type": "qemu", "vmid": 107, "name": "locked", "node": "pve-01", "status": "running", "maxmem": gib, "lock": "backup"},
		{"type": "qemu", "vmid": 108, "name": "iso", "node": "pve-01", "status": "running", "maxmem": gib},
		{"type": "qemu", "vmid": 110, "name": "disk", "node": "pve-01", "status": "running", "maxmem": gib},
		{"type": "qemu", "vmid": 111, "name": "ci", "node": "pve-01", "status": "running", "maxmem": gib},
		{"type": "qemu", "vmid": 112, "name": "nostore", "node": "pve-01", "status": "running", "maxmem": gib},
	}
	f := newEvacFixture(guests)
	f.ha = []string{"ct:109"}
	f.pre[105] = `{"running":1,"allowed_nodes":["pve-02","pve-03"],"local_disks":[],"local_resources":["hostpci0"]}`
	f.pre[108] = `{"running":1,"allowed_nodes":["pve-02","pve-03"],"local_disks":[{"volid":"local:iso/x.iso","cdrom":1}],"local_resources":[]}`
	f.pre[110] = `{"running":1,"allowed_nodes":["pve-02","pve-03"],"local_disks":[{"volid":"local-lvm:vm-110-disk-0","size":1}],"local_resources":[]}`
	// A cloud-init drive is regenerated on the target: not a blocker.
	f.pre[111] = `{"running":1,"allowed_nodes":["pve-02","pve-03"],"local_disks":[{"volid":"local-lvm:vm-111-cloudinit","cdrom":1}],"local_resources":[]}`
	f.pre[112] = `{"running":1,"allowed_nodes":[],"not_allowed_nodes":{"pve-02":{"unavailable_storages":["tank"]},"pve-03":{"unavailable_storages":["tank"]}},"local_disks":[],"local_resources":[]}`
	srv := f.start()
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "--all", "-o", "json")...)
	if err == nil || ExitCodeFor(err) != ExitValidation {
		t.Fatalf("skips must exit %d, got %v", ExitValidation, err)
	}
	plan := evacPlan(t, out)
	wantReason := map[int]string{
		103: "HA-managed", 109: "HA-managed", 104: "template", 105: "local resources: hostpci0",
		107: "locked (backup)", 108: "local CD-ROM ISO", 110: "--with-local-disks", 112: "no allowed destination: pve-02: unavailable storages tank",
	}
	for id, w := range wantReason {
		it := plan[id]
		if it["skip"] != true || !strings.Contains(fmt.Sprint(it["reason"]), w) {
			t.Errorf("vmid %d: want skip with reason containing %q, got %v", id, w, it)
		}
	}
	if plan[111]["skip"] != false || plan[111]["to"] == nil {
		t.Errorf("cloud-init drive must not block migration: %v", plan[111])
	}

	// --with-local-disks lets 110 move.
	out, _ = runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "--with-local-disks", "-o", "json")...)
	if it := evacPlan(t, out)[110]; it["skip"] != false {
		t.Errorf("--with-local-disks should plan 110: %v", it)
	}
}

func TestNodeEvacuateTemplateOnlyIsExpected(t *testing.T) {
	guests := []map[string]any{
		{"type": "qemu", "vmid": 104, "name": "tpl", "node": "pve-01", "status": "stopped", "maxmem": gib, "template": 1},
	}
	f := newEvacFixture(guests)
	srv := f.start()
	defer srv.Close()
	if _, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "--all", "-o", "json")...); err != nil {
		t.Fatalf("a template skip is expected and must not fail: %v", err)
	}
}

func TestNodeEvacuateConfirmGate(t *testing.T) {
	f := newEvacFixture(defaultEvacGuests())
	srv := f.start()
	defer srv.Close()

	// Non-interactive without --yes: refuse, echoing the context, migrate nothing.
	_, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01")...)
	if err == nil {
		t.Fatal("evacuate without --yes in non-interactive mode must refuse")
	}
	msg := err.Error()
	for _, w := range []string{"migrate 3 guest(s) off pve-01", "2 VMs, 1 CTs", "pve-02, pve-03"} {
		if !strings.Contains(msg, w) {
			t.Errorf("prompt should contain %q: %s", w, msg)
		}
	}
	if len(f.order) != 0 {
		t.Fatalf("refused evacuate must not migrate, got %v", f.order)
	}
}

func TestNodeEvacuateRunsMigrations(t *testing.T) {
	f := newEvacFixture(defaultEvacGuests())
	f.pre[101] = `{"running":1,"allowed_nodes":["pve-02","pve-03"],"local_disks":[{"volid":"local-lvm:vm-101-disk-0"}],"local_resources":[]}`
	srv := f.start()
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--yes", "--with-local-disks", "-o", "json")...)
	if err != nil {
		t.Fatalf("evacuate: %v\n%s", err, out)
	}
	// Sequential by default, largest first.
	if fmt.Sprint(f.order) != "[100 101 102]" {
		t.Errorf("migration order = %v, want [100 101 102]", f.order)
	}
	if p := f.posts[100]; p["target"] != "pve-03" || p["online"] != "1" || p["with-local-disks"] != "" {
		t.Errorf("VM 100 params: %v", p)
	}
	if p := f.posts[101]; p["online"] != "1" || p["with-local-disks"] != "1" {
		t.Errorf("VM 101 params: %v", p)
	}
	if p := f.posts[102]; p["target"] != "pve-02" || p["restart"] != "1" || p["timeout"] != "180" || p["online"] != "" {
		t.Errorf("CT 102 params: %v", p)
	}
	for id, it := range evacPlan(t, out) {
		if it["result"] != "migrated" || it["upid"] == nil {
			t.Errorf("vmid %d: %v", id, it)
		}
	}
}

func TestNodeEvacuatePartialFailureExitCode(t *testing.T) {
	f := newEvacFixture(defaultEvacGuests())
	f.fail[101] = true
	srv := f.start()
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--yes", "--parallel", "2", "-o", "json")...)
	if err == nil || ExitCodeFor(err) != ExitTaskFailed {
		t.Fatalf("a failed migration must exit %d, got %v", ExitTaskFailed, err)
	}
	plan := evacPlan(t, out)
	if plan[101]["result"] != "failed" || !strings.Contains(fmt.Sprint(plan[101]["error"]), "migration aborted") {
		t.Errorf("101 should be reported failed: %v", plan[101])
	}
	if plan[100]["result"] != "migrated" || plan[102]["result"] != "migrated" {
		t.Errorf("the other guests should still migrate: %v / %v", plan[100], plan[102])
	}
	if len(f.order) != 3 {
		t.Errorf("all planned guests should be attempted, got %v", f.order)
	}
}

func TestNodeEvacuateSingleNodeNoDestination(t *testing.T) {
	f := newEvacFixture(defaultEvacGuests()[:1])
	f.nodes = []map[string]any{{"type": "node", "node": "pve-01", "status": "online", "mem": gib, "maxmem": 64 * gib, "maxcpu": 16}}
	srv := f.start()
	defer srv.Close()
	out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "-o", "json")...)
	if err == nil {
		t.Fatal("single node: want a non-zero exit")
	}
	if it := evacPlan(t, out)[100]; !strings.Contains(fmt.Sprint(it["reason"]), "no other online node") {
		t.Errorf("want no-destination reason, got %v", it)
	}
}

func TestNodeEvacuateValidation(t *testing.T) {
	f := newEvacFixture(defaultEvacGuests())
	srv := f.start()
	defer srv.Close()
	for _, args := range [][]string{
		{"--parallel", "0"}, {"--parallel", "9"}, {"--max-mem-pct", "0"}, {"--max-mem-pct", "101"}, {"--vcpu-ratio", "-1"},
	} {
		a := append([]string{"node", "evacuate", "pve-01", "--dry-run"}, args...)
		if _, err := runCLI(t, withCreds(srv, a...)...); err == nil {
			t.Errorf("%v should be rejected", args)
		}
	}
	if _, err := runCLI(t, withCreds(srv, "node", "evacuate", "nope", "--dry-run")...); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown node: %v", err)
	}
}

func TestNodeEvacuateRefusedOnPDM(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, err := runCLI(t, withPDMCreds(srv, "node", "evacuate", "pve-01", "--dry-run")...)
	if err == nil || !strings.Contains(err.Error(), "current provider: pdm") {
		t.Fatalf("want a provider-named refusal on PDM, got %v", err)
	}
}
