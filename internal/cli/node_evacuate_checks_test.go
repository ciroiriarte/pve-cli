package cli

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func ctGuest(vmid int, status string) map[string]any {
	return map[string]any{"type": "lxc", "vmid": vmid, "name": fmt.Sprintf("ct%d", vmid), "node": "pve-01", "status": status, "maxmem": gib, "maxcpu": 1}
}

func TestNodeEvacuateCTPreconditions(t *testing.T) {
	f := newEvacFixture([]map[string]any{
		ctGuest(120, "running"), ctGuest(121, "running"), ctGuest(122, "running"), ctGuest(123, "running"),
		ctGuest(124, "running"), ctGuest(125, "running"), ctGuest(126, "running"), ctGuest(127, "stopped"),
	})
	// 120: HA affinity excludes pve-03, so it must land on pve-02 even though
	// pve-03 has more headroom.
	f.pre[120] = `{"running":1,"allowed-nodes":["pve-02"],"not-allowed-nodes":{"pve-03":{"blocking-ha-resources":[{"sid":"vm:300","cause":"resource-affinity"}]}}}`
	// 121: no allowed node at all.
	f.pre[121] = `{"running":1,"allowed-nodes":[],"not-allowed-nodes":{"pve-02":{"blocking-ha-resources":[{"sid":"vm:300"}]},"pve-03":{"blocking-ha-resources":[{"sid":"vm:301"}]}}}`
	// 122: older PVE without the endpoint: only the config checks apply.
	f.pre[122] = "501"
	// 123: a real precondition error is not ignored.
	f.pre[123] = "500"
	// 124: local bind mount, refused by LXC/Migrate.pm.
	f.ctConf[124] = `{"rootfs":"local-lvm:vm-124-disk-0,size=8G","mp0":"/srv/data,mp=/data"}`
	// 125: a shared bind mount is fine; the tank volume limits it to pve-01/pve-02.
	f.ctConf[125] = `{"rootfs":"local-lvm:vm-125-disk-0,size=8G","mp0":"/mnt/nfs,mp=/nfs,shared=1","mp1":"tank:subvol-125-disk-1,mp=/t"}`
	// 126: unused volume on a disabled storage.
	f.ctConf[126] = `{"rootfs":"local-lvm:vm-126-disk-0,size=8G","unused0":"off:126/vm-126-disk-9.raw"}`
	srv := f.start()
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "--all", "-o", "json")...)
	if err == nil || ExitCodeFor(err) != ExitValidation {
		t.Fatalf("want exit %d, got %v", ExitValidation, err)
	}
	plan := evacPlan(t, out)
	if plan[120]["to"] != "pve-02" {
		t.Errorf("120 must honor allowed-nodes: %v", plan[120])
	}
	if plan[122]["skip"] != false || plan[127]["skip"] != false {
		t.Errorf("122 (no endpoint) and 127 should still be planned: %v / %v", plan[122], plan[127])
	}
	if plan[125]["to"] != "pve-02" {
		t.Errorf("125's tank volume restricts it to pve-02: %v", plan[125])
	}
	want := map[int]string{
		121: "no allowed destination: pve-02: blocking ha resources vm:300; pve-03: blocking ha resources vm:301",
		123: "precondition check failed",
		124: "local bind/device mount points: mp0",
		126: "storage off not available",
	}
	for id, w := range want {
		if it := plan[id]; it["skip"] != true || !strings.Contains(fmt.Sprint(it["reason"]), w) {
			t.Errorf("vmid %d: want skip with %q, got %v", id, w, it)
		}
	}
}

func TestNodeEvacuateMappedResources(t *testing.T) {
	f := newEvacFixture([]map[string]any{
		{"type": "qemu", "vmid": 130, "name": "usb-off", "node": "pve-01", "status": "stopped", "maxmem": gib},
		{"type": "qemu", "vmid": 131, "name": "usb-on", "node": "pve-01", "status": "running", "maxmem": gib},
		{"type": "qemu", "vmid": 132, "name": "pci-live", "node": "pve-01", "status": "running", "maxmem": gib},
	})
	f.pre[130] = `{"running":0,"allowed_nodes":["pve-02","pve-03"],"local_disks":[],"local_resources":["usb0"],"mapped-resource-info":{"usb0":{"name":"dongle"}}}`
	f.pre[131] = `{"running":1,"allowed_nodes":["pve-02","pve-03"],"local_disks":[],"local_resources":["usb0"],"mapped-resource-info":{"usb0":{"name":"dongle"}}}`
	f.pre[132] = `{"running":1,"allowed_nodes":["pve-02","pve-03"],"local_disks":[],"local_resources":["hostpci0"],"mapped-resource-info":{"hostpci0":{"name":"gpu","live-migration":1}}}`
	srv := f.start()
	defer srv.Close()
	out, _ := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "--all", "-o", "json")...)
	plan := evacPlan(t, out)
	if plan[130]["skip"] != false {
		t.Errorf("a stopped VM with a mapped device migrates offline: %v", plan[130])
	}
	if plan[131]["skip"] != true || !strings.Contains(fmt.Sprint(plan[131]["reason"]), "not live-migratable: usb0") {
		t.Errorf("a running VM with a non-live mapped device must be skipped: %v", plan[131])
	}
	if plan[132]["skip"] != false {
		t.Errorf("a live-migration-capable mapping must not block: %v", plan[132])
	}
}

func TestNodeEvacuateReplicationTargets(t *testing.T) {
	f := newEvacFixture([]map[string]any{
		{"type": "qemu", "vmid": 140, "name": "repl", "node": "pve-01", "status": "running", "maxmem": gib},
		{"type": "qemu", "vmid": 141, "name": "repl-off", "node": "pve-01", "status": "running", "maxmem": gib},
		{"type": "qemu", "vmid": 142, "name": "repl-stopped", "node": "pve-01", "status": "stopped", "maxmem": gib},
	})
	// 140 replicates to pve-02 (pve-03 has more headroom); 141 only to the
	// offline pve-04; 142 is stopped, so the restriction doesn't apply.
	f.repl = `{"id":"140-0","guest":140,"target":"pve-02"},{"id":"141-0","guest":141,"target":"pve-04"},{"id":"142-0","guest":142,"target":"pve-02"}`
	srv := f.start()
	defer srv.Close()
	out, _ := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "--all", "-o", "json")...)
	plan := evacPlan(t, out)
	if plan[140]["to"] != "pve-02" {
		t.Errorf("running replicated VM must go to its replication target: %v", plan[140])
	}
	if plan[141]["skip"] != true || !strings.Contains(fmt.Sprint(plan[141]["reason"]), "not a replication target") {
		t.Errorf("141 has no online replication target: %v", plan[141])
	}
	if plan[142]["to"] != "pve-03" {
		t.Errorf("stopped replicated VM is not restricted: %v", plan[142])
	}
}

func TestNodeEvacuateSyncPostFailure(t *testing.T) {
	f := newEvacFixture(defaultEvacGuests())
	f.postErr[100] = true
	srv := f.start()
	defer srv.Close()
	out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--yes", "-o", "json")...)
	if err == nil || ExitCodeFor(err) != ExitTaskFailed {
		t.Fatalf("a refused POST must exit %d, got %v", ExitTaskFailed, err)
	}
	plan := evacPlan(t, out)
	if plan[100]["result"] != "failed" || !strings.Contains(fmt.Sprint(plan[100]["error"]), "replicated") || plan[100]["upid"] != nil {
		t.Errorf("100 should fail before any task: %v", plan[100])
	}
	if plan[101]["result"] != "migrated" {
		t.Errorf("the run continues after a refused POST: %v", plan[101])
	}
}

func TestNodeEvacuateCTConfigCurrentAndPending(t *testing.T) {
	f := newEvacFixture([]map[string]any{
		ctGuest(150, "running"), ctGuest(151, "running"), ctGuest(152, "running"), ctGuest(153, "running"),
	})
	// 150: active config has a local bind mount; a pending delete removes it
	// from the pending view, which must not make the CT movable.
	f.ctConfCurrent[150] = `{"rootfs":"local-lvm:vm-150-disk-0","mp0":"/srv/a,mp=/a"}`
	f.ctConf[150] = `{"rootfs":"local-lvm:vm-150-disk-0","delete":"mp0"}`
	// 151: a pending-only bind mount is ignored by PVE (prepare walks the
	// active config; the pending walk skips non-volume mounts).
	f.ctConfCurrent[151] = `{"rootfs":"local-lvm:vm-151-disk-0"}`
	f.ctConf[151] = `{"rootfs":"local-lvm:vm-151-disk-0","mp1":"/srv/b,mp=/b"}`
	// 152: shared=yes with volume= after other options is a shared bind mount.
	f.ctConf[152] = `{"rootfs":"local-lvm:vm-152-disk-0","mp0":"mp=/n,volume=/mnt/nfs,shared=yes"}`
	// 153: a storage the token can't see in /storage doesn't restrict.
	f.ctConf[153] = `{"rootfs":"hidden:vm-153-disk-0"}`
	srv := f.start()
	defer srv.Close()

	out, _ := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "-o", "json")...)
	plan := evacPlan(t, out)
	for id, w := range map[int]string{150: "mount points: mp0"} {
		if it := plan[id]; it["skip"] != true || !strings.Contains(fmt.Sprint(it["reason"]), w) {
			t.Errorf("vmid %d: want skip with %q, got %v", id, w, it)
		}
	}
	if plan[151]["skip"] != false {
		t.Errorf("a pending-only bind mount must not block: %v", plan[151])
	}
	if plan[152]["skip"] != false {
		t.Errorf("a shared=yes bind mount must not block: %v", plan[152])
	}
	if plan[153]["skip"] != false {
		t.Errorf("an invisible storage must not restrict: %v", plan[153])
	}
}

func TestNodeEvacuateWaitTimeoutStopsDispatch(t *testing.T) {
	f := newEvacFixture(defaultEvacGuests())
	f.hang[100] = true // the first (largest) migration never finishes
	srv := f.start()
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--yes", "--parallel", "1", "--wait-timeout", "1", "-o", "json")...)
	if err == nil || ExitCodeFor(err) == ExitOK {
		t.Fatalf("a timed-out migration must exit non-zero, got %v", err)
	}
	if fmt.Sprint(f.order) != "[100]" {
		t.Fatalf("no migration may start after a wait timeout (it could exceed --parallel), got POSTs %v", f.order)
	}
	plan := evacPlan(t, out)
	if plan[100]["result"] != "unknown" || plan[100]["upid"] == nil {
		t.Errorf("100 should be unknown with its upid: %v", plan[100])
	}
	for _, id := range []int{101, 102} {
		if plan[id]["result"] != "not-attempted" {
			t.Errorf("vmid %d should be not-attempted: %v", id, plan[id])
		}
	}
}

// A non-terminal wait failure after the task started (a status-poll error, a
// timeout surfacing as a transport error) leaves the migration's outcome
// unknown: report it so and start nothing else.
func TestNodeEvacuateNonTerminalWaitHalts(t *testing.T) {
	for name, set := range map[string]func(*evacFixture){
		"poll error":     func(f *evacFixture) { f.pollErr[100] = true },
		"slow poll":      func(f *evacFixture) { f.slow[100] = true },
		"never finishes": func(f *evacFixture) { f.hang[100] = true },
	} {
		t.Run(name, func(t *testing.T) {
			f := newEvacFixture(defaultEvacGuests())
			set(f)
			srv := f.start()
			defer srv.Close()
			out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--yes", "--parallel", "1", "--wait-timeout", "1", "-o", "json")...)
			if err == nil || ExitCodeFor(err) != ExitTaskFailed {
				t.Fatalf("want exit %d, got %v", ExitTaskFailed, err)
			}
			if fmt.Sprint(f.order) != "[100]" {
				t.Fatalf("dispatch must halt, got POSTs %v", f.order)
			}
			plan := evacPlan(t, out)
			if plan[100]["result"] != "unknown" || !strings.Contains(fmt.Sprint(plan[100]["error"]), "pc task show UPID:") {
				t.Errorf("100 should be unknown with a task hint: %v", plan[100])
			}
			for _, id := range []int{101, 102} {
				if plan[id]["result"] != "not-attempted" {
					t.Errorf("vmid %d should be not-attempted: %v", id, plan[id])
				}
			}
		})
	}
}

func TestNodeEvacuateStorageContentAndPendingVolumes(t *testing.T) {
	f := newEvacFixture([]map[string]any{
		ctGuest(160, "running"), ctGuest(161, "running"), ctGuest(162, "running"), ctGuest(163, "running"),
		{"type": "qemu", "vmid": 164, "name": "isodisk", "node": "pve-01", "status": "stopped", "maxmem": gib},
	})
	// 160: rootfs on a storage without content rootdir.
	f.ctConf[160] = `{"rootfs":"isos:160/vm-160-disk-0.raw"}`
	// 161: a pending new-disk spec on tank (pve-01,pve-02 only) is skipped by PVE.
	f.ctConfCurrent[161] = `{"rootfs":"local-lvm:vm-161-disk-0"}`
	f.ctConf[161] = `{"rootfs":"local-lvm:vm-161-disk-0","mp0":"tank:8,mp=/t"}`
	// 162: an existing pending volume on tank restricts to pve-02.
	f.ctConfCurrent[162] = `{"rootfs":"local-lvm:vm-162-disk-0"}`
	f.ctConf[162] = `{"rootfs":"local-lvm:vm-162-disk-0","mp0":"tank:subvol-162-disk-1,mp=/t"}`
	// 163: a pending-only volume on shared storage isn't target-checked.
	f.ctConfCurrent[163] = `{"rootfs":"local-lvm:vm-163-disk-0"}`
	f.ctConf[163] = `{"rootfs":"local-lvm:vm-163-disk-0","mp0":"nfs:163/vm-163-disk-1.raw,mp=/n"}`
	// 164: a local VM disk on a storage without content images.
	f.pre[164] = `{"running":0,"allowed_nodes":["pve-02","pve-03"],"local_disks":[{"volid":"isos:164/vm-164-disk-0.qcow2"}],"local_resources":[]}`
	srv := f.start()
	defer srv.Close()

	out, _ := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "--all", "-o", "json")...)
	plan := evacPlan(t, out)
	for id, w := range map[int]string{160: "content type rootdir", 164: "content type images"} {
		if it := plan[id]; it["skip"] != true || !strings.Contains(fmt.Sprint(it["reason"]), w) {
			t.Errorf("vmid %d: want skip with %q, got %v", id, w, it)
		}
	}
	if plan[161]["to"] != "pve-03" {
		t.Errorf("a pending new-disk spec must not restrict (want pve-03, most headroom): %v", plan[161])
	}
	if plan[162]["to"] != "pve-02" {
		t.Errorf("an existing pending volume on tank restricts to pve-02: %v", plan[162])
	}
	if plan[163]["skip"] != false {
		t.Errorf("a pending shared volume must not restrict: %v", plan[163])
	}
}

// /cluster/resources can lag the real power state (seen live right after a
// restart migration): the precondition's running flag wins.
func TestNodeEvacuateStaleStatusUsesPrecondition(t *testing.T) {
	f := newEvacFixture([]map[string]any{
		ctGuest(170, "stopped"),
		{"type": "qemu", "vmid": 171, "name": "vm", "node": "pve-01", "status": "running", "maxmem": gib},
	})
	f.liveRunning[170] = 1 // actually running
	f.liveRunning[171] = 0 // actually stopped
	srv := f.start()
	defer srv.Close()
	out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--all", "--yes", "-o", "json")...)
	if err != nil {
		t.Fatalf("evacuate: %v", err)
	}
	plan := evacPlan(t, out)
	if plan[170]["mode"] != "restart" || plan[170]["status"] != "running" || f.posts[170]["restart"] != "1" {
		t.Errorf("170 is running per the precondition: %v / %v", plan[170], f.posts[170])
	}
	if plan[171]["mode"] != "offline" || f.posts[171]["online"] != "" {
		t.Errorf("171 is stopped per the precondition: %v / %v", plan[171], f.posts[171])
	}
}

// Running-state policy (replication restriction, --online=false) must use the
// precondition's live state, not stale /cluster/resources.
func TestNodeEvacuateStalePolicyUsesLiveState(t *testing.T) {
	f := newEvacFixture([]map[string]any{
		{"type": "qemu", "vmid": 180, "name": "repl", "node": "pve-01", "status": "stopped", "maxmem": gib},
		{"type": "qemu", "vmid": 181, "name": "off", "node": "pve-01", "status": "running", "maxmem": gib},
	})
	f.liveRunning[180] = 1 // actually running → live migration → replication target only
	f.liveRunning[181] = 0 // actually stopped → --online=false doesn't apply
	f.repl = `{"id":"180-0","guest":180,"target":"pve-02"}`
	srv := f.start()
	defer srv.Close()

	out, _ := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "--all", "-o", "json")...)
	if it := evacPlan(t, out)[180]; it["to"] != "pve-02" || it["mode"] != "online" {
		t.Errorf("180 is running live and replicates to pve-02: %v", it)
	}
	out, _ = runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "--all", "--online=false", "-o", "json")...)
	if it := evacPlan(t, out)[181]; it["skip"] != false || it["mode"] != "offline" {
		t.Errorf("181 is stopped live; --online=false must not skip it: %v", it)
	}
}

// Without --all, the running-only filter applies after the precondition: a
// stale "stopped" entry for a running guest must not be left behind.
func TestNodeEvacuateDefaultFilterUsesLiveState(t *testing.T) {
	f := newEvacFixture([]map[string]any{
		{"type": "qemu", "vmid": 190, "name": "stale", "node": "pve-01", "status": "stopped", "maxmem": gib},
		ctGuest(191, "stopped"),
		{"type": "qemu", "vmid": 192, "name": "really-off", "node": "pve-01", "status": "stopped", "maxmem": gib},
	})
	f.liveRunning[190] = 1
	f.liveRunning[191] = 1
	srv := f.start()
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "-o", "json")...)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	plan := evacPlan(t, out)
	if plan[190]["mode"] != "online" || plan[190]["skip"] != false {
		t.Errorf("190 is live running and must be planned: %v", plan[190])
	}
	if plan[191]["mode"] != "restart" || plan[191]["skip"] != false {
		t.Errorf("191 is live running and must be planned: %v", plan[191])
	}
	if _, ok := plan[192]; ok {
		t.Errorf("192 is really stopped and stays out of the default plan: %v", plan[192])
	}

	out, err = runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "--online=false", "-o", "json")...)
	if err == nil || ExitCodeFor(err) != ExitValidation {
		t.Fatalf("a running VM skipped by --online=false must exit %d, got %v", ExitValidation, err)
	}
	if it := evacPlan(t, out)[190]; it["skip"] != true || !strings.Contains(fmt.Sprint(it["reason"]), "--online=false") {
		t.Errorf("190 must be skipped with the --online=false reason: %v", it)
	}
}

// A guest listed running whose precondition says stopped (it stopped between
// listing and planning) leaves the default plan like one stopped at listing;
// with --all it is planned offline.
func TestNodeEvacuatePreconditionStoppedLeavesDefaultPlan(t *testing.T) {
	f := newEvacFixture([]map[string]any{
		{"type": "qemu", "vmid": 195, "name": "vm-stopping", "node": "pve-01", "status": "running", "maxmem": gib},
		ctGuest(196, "running"),
		ctGuest(197, "running"),
	})
	f.listStatus[195], f.listStatus[196] = "running", "running"
	f.liveRunning[195], f.liveRunning[196] = 0, 0 // the preconditions see them stopped
	srv := f.start()
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "-o", "json")...)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	plan := evacPlan(t, out)
	for _, id := range []int{195, 196} {
		if _, ok := plan[id]; ok {
			t.Errorf("vmid %d is stopped per its precondition and must leave the default plan: %v", id, plan[id])
		}
	}
	if plan[197] == nil || plan[197]["mode"] != "restart" {
		t.Errorf("197 is running and must be planned: %v", plan[197])
	}

	out, err = runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "--all", "-o", "json")...)
	if err != nil {
		t.Fatalf("dry-run --all: %v", err)
	}
	plan = evacPlan(t, out)
	for _, id := range []int{195, 196} {
		if it := plan[id]; it == nil || it["mode"] != "offline" || it["skip"] != false || it["status"] != "stopped" {
			t.Errorf("--all: vmid %d must be planned offline: %v", id, it)
		}
	}
}

// A live-list entry with an empty or unknown status is not taken as stopped:
// in default mode it becomes an unknown skip (exit 6).
func TestNodeEvacuateUnknownListStatus(t *testing.T) {
	f := newEvacFixture([]map[string]any{
		{"type": "qemu", "vmid": 210, "name": "blank", "node": "pve-01", "status": "stopped", "maxmem": gib},
		ctGuest(211, "stopped"),
		ctGuest(212, "stopped"),
	})
	f.listStatus[210], f.listStatus[211] = "", "weird"
	srv := f.start()
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "-o", "json")...)
	if err == nil || ExitCodeFor(err) != ExitValidation {
		t.Fatalf("unknown status must exit %d, got %v", ExitValidation, err)
	}
	plan := evacPlan(t, out)
	for _, id := range []int{210, 211} {
		if it := plan[id]; it == nil || it["skip"] != true || !strings.Contains(fmt.Sprint(it["reason"]), "power state unknown") {
			t.Errorf("vmid %d: want an unknown-power-state skip, got %v", id, it)
		}
	}
	if _, ok := plan[212]; ok {
		t.Errorf("212 is confirmed stopped and stays out: %v", plan[212])
	}
}

// Default mode: liveness comes from the node's guest lists. A guest listed
// stopped leaves the plan only when the live list confirms it stopped; if a
// list call fails, stopped-listed guests of that kind stay as skips (exit 6).
func TestNodeEvacuateDefaultUnknownLivenessStays(t *testing.T) {
	guests := func() []map[string]any {
		return []map[string]any{
			// 200: VM listed stopped, live running, precondition 403 → kept as a skip.
			{"type": "qemu", "vmid": 200, "name": "vm-403", "node": "pve-01", "status": "stopped", "maxmem": gib},
			// 201: VM really stopped (precondition would 403) → dropped, never queried.
			{"type": "qemu", "vmid": 201, "name": "vm-off", "node": "pve-01", "status": "stopped", "maxmem": gib},
			// 202: CT listed stopped.
			ctGuest(202, "stopped"),
			// 203: locked CT listed stopped, live running.
			{"type": "lxc", "vmid": 203, "name": "locked", "node": "pve-01", "status": "stopped", "maxmem": gib, "lock": "backup"},
			// 204: HA VM listed stopped, live running → kept, skipped as HA.
			{"type": "qemu", "vmid": 204, "name": "ha", "node": "pve-01", "status": "stopped", "maxmem": gib, "hastate": "started"},
			// 205: CT on PVE without the LXC precondition, listed stopped, live running.
			ctGuest(205, "stopped"),
			// 206: template: never in the default plan.
			{"type": "qemu", "vmid": 206, "name": "tpl", "node": "pve-01", "status": "stopped", "maxmem": gib, "template": 1},
			// 207: CT listed running.
			ctGuest(207, "running"),
		}
	}
	setup := func(f *evacFixture) {
		f.pre[200], f.pre[201], f.pre[202], f.pre[205] = "403", "403", "403", "501"
		f.liveRunning[200], f.liveRunning[203], f.liveRunning[204], f.liveRunning[205] = 1, 1, 1, 1
	}

	t.Run("lists ok", func(t *testing.T) {
		f := newEvacFixture(guests())
		setup(f)
		srv := f.start()
		defer srv.Close()
		out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "-o", "json")...)
		if err == nil || ExitCodeFor(err) != ExitValidation {
			t.Fatalf("want exit %d, got %v", ExitValidation, err)
		}
		plan := evacPlan(t, out)
		for id, w := range map[int]string{200: "precondition check failed", 203: "locked (backup)", 204: "HA-managed"} {
			if it := plan[id]; it == nil || it["skip"] != true || !strings.Contains(fmt.Sprint(it["reason"]), w) {
				t.Errorf("vmid %d: want a skip with %q, got %v", id, w, it)
			}
		}
		if plan[205] == nil || plan[205]["mode"] != "restart" || plan[205]["skip"] != false {
			t.Errorf("205 is live running and must be planned: %v", plan[205])
		}
		for _, id := range []int{201, 202, 206} {
			if _, ok := plan[id]; ok {
				t.Errorf("vmid %d is confirmed stopped and must leave the default plan: %v", id, plan[id])
			}
		}
	})

	t.Run("lxc list fails", func(t *testing.T) {
		f := newEvacFixture(guests())
		setup(f)
		f.listErr["lxc"] = true
		srv := f.start()
		defer srv.Close()
		out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "-o", "json")...)
		if err == nil || ExitCodeFor(err) != ExitValidation {
			t.Fatalf("unconfirmed guests must exit %d, got %v", ExitValidation, err)
		}
		plan := evacPlan(t, out)
		for id, w := range map[int]string{202: "power state unknown", 205: "power state unknown", 203: "locked (backup)"} {
			if it := plan[id]; it == nil || it["skip"] != true || !strings.Contains(fmt.Sprint(it["reason"]), w) {
				t.Errorf("vmid %d: want a skip with %q, got %v", id, w, it)
			}
		}
		if plan[207] == nil || plan[207]["skip"] != false {
			t.Errorf("a CT listed running is still planned: %v", plan[207])
		}
		if _, ok := plan[201]; ok {
			t.Errorf("the qemu list worked, so 201 stays out: %v", plan[201])
		}
	})

	t.Run("qemu list fails", func(t *testing.T) {
		f := newEvacFixture(guests())
		setup(f)
		f.listErr["qemu"] = true
		srv := f.start()
		defer srv.Close()
		out, _ := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--dry-run", "-o", "json")...)
		plan := evacPlan(t, out)
		if it := plan[201]; it == nil || it["skip"] != true || !strings.Contains(fmt.Sprint(it["reason"]), "power state unknown") {
			t.Errorf("201's liveness is unknown: %v", it)
		}
		if _, ok := plan[206]; ok {
			t.Errorf("a template can't be running and stays out: %v", plan[206])
		}
	})
}

// Both halt paths print a "not attempted" line: at admission (--parallel 1)
// and at POST time (a worker admitted before the halt).
func TestNodeEvacuateNotAttemptedStderr(t *testing.T) {
	t.Run("admission", func(t *testing.T) {
		f := newEvacFixture(defaultEvacGuests())
		f.pollErr[100] = true
		srv := f.start()
		defer srv.Close()
		errOut := captureStderr(t, func() {
			runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--yes", "--parallel", "1", "-o", "json")...)
		})
		for _, w := range []string{"VM 101 (mid): not attempted", "CT 102 (ct): not attempted"} {
			if !strings.Contains(errOut, w) {
				t.Errorf("stderr should contain %q:\n%s", w, errOut)
			}
		}
	})
	t.Run("post time", func(t *testing.T) {
		f := newEvacFixture(defaultEvacGuests())
		f.pollErr[100] = true
		srv := f.start()
		defer srv.Close()
		haltedCh := make(chan struct{})
		var once sync.Once
		evacHalted = func() { once.Do(func() { close(haltedCh) }) }
		evacBeforePost = func(vmid int) {
			if vmid != 100 {
				select {
				case <-haltedCh:
				case <-time.After(10 * time.Second):
				}
			}
		}
		defer func() { evacHalted, evacBeforePost = nil, nil }()
		errOut := captureStderr(t, func() {
			runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--yes", "--parallel", "2", "-o", "json")...)
		})
		if !strings.Contains(errOut, "VM 101 (mid) pve-01 -> pve-03 [online]: not attempted") {
			t.Errorf("stderr should report 101 not attempted at POST time:\n%s", errOut)
		}
	})
}

// A worker admitted before a halt must not POST after it (--parallel > 1).
func TestNodeEvacuateParallelNoPostAfterHalt(t *testing.T) {
	f := newEvacFixture(defaultEvacGuests())
	f.pollErr[100] = true // 100's wait ends without a terminal status
	srv := f.start()
	defer srv.Close()

	haltedCh := make(chan struct{})
	var once sync.Once
	evacHalted = func() { once.Do(func() { close(haltedCh) }) }
	evacBeforePost = func(vmid int) {
		if vmid == 100 {
			return
		}
		select { // hold the second worker until the halt is recorded
		case <-haltedCh:
		case <-time.After(10 * time.Second):
		}
	}
	defer func() { evacHalted, evacBeforePost = nil, nil }()

	out, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--yes", "--parallel", "2", "-o", "json")...)
	if err == nil {
		t.Fatal("want non-zero exit")
	}
	if fmt.Sprint(f.order) != "[100]" {
		t.Fatalf("no POST may follow the halt, got %v", f.order)
	}
	plan := evacPlan(t, out)
	if plan[100]["result"] != "unknown" || plan[101]["result"] != "not-attempted" || plan[102]["result"] != "not-attempted" {
		t.Errorf("results: 100=%v 101=%v 102=%v", plan[100]["result"], plan[101]["result"], plan[102]["result"])
	}
}

func TestNodeEvacuateForcePrompt(t *testing.T) {
	f := newEvacFixture(defaultEvacGuests())
	srv := f.start()
	defer srv.Close()
	_, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--force")...)
	if err == nil || !strings.Contains(err.Error(), "capacity checks disabled (--force)") {
		t.Fatalf("the --force prompt must say capacity checks are off: %v", err)
	}
}
