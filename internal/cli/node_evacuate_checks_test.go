package cli

import (
	"fmt"
	"strings"
	"testing"
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
	// 151: a pending bind mount (not yet active) also blocks.
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
	for id, w := range map[int]string{150: "mount points: mp0", 151: "mount points: mp1"} {
		if it := plan[id]; it["skip"] != true || !strings.Contains(fmt.Sprint(it["reason"]), w) {
			t.Errorf("vmid %d: want skip with %q, got %v", id, w, it)
		}
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

func TestNodeEvacuateForcePrompt(t *testing.T) {
	f := newEvacFixture(defaultEvacGuests())
	srv := f.start()
	defer srv.Close()
	_, err := runCLI(t, withCreds(srv, "node", "evacuate", "pve-01", "--force")...)
	if err == nil || !strings.Contains(err.Error(), "capacity checks disabled (--force)") {
		t.Fatalf("the --force prompt must say capacity checks are off: %v", err)
	}
}
