package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func pdmServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	// Real PDM shapes: remotes live under /remotes/remote with nodes as an
	// array of "host:port,fingerprint=..." strings; resource types are prefixed
	// (pve-qemu / pve-lxc / pve-node).
	mux.HandleFunc("/api2/json/remotes/remote", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[
			{"id":"dc-east","type":"pve","nodes":["n1.lab:8006,fingerprint=AA","n2.lab:8006,fingerprint=BB"]},
			{"id":"dc-west","type":"pve","nodes":["n3.lab:8006,fingerprint=CC"]}
		]}`))
	})
	mux.HandleFunc("/api2/json/resources/list", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[
			{"remote":"dc-east","resources":[{"type":"pve-qemu","vmid":100,"name":"web","node":"n1","status":"running"}]},
			{"remote":"dc-west","resources":[{"type":"pve-lxc","vmid":200,"name":"ct1","node":"n3","status":"stopped"}]}
		]}`))
	})
	// proxied power action on the guest's remote
	mux.HandleFunc("/api2/json/pve/remotes/dc-east/qemu/100/stop", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":"UPID:n1:0001:0002:6A00:qmstop:100:root@pam:"}`))
	})
	mux.HandleFunc("/api2/json/pve/remotes/dc-east/qemu/100/resume", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":"UPID:n1:0001:0002:6A00:qmresume:100:root@pam:"}`))
	})
	// proxied snapshot (list/create), migrate, and status
	// PDM returns proxied task ids in the prefixed "pve:<remote>!UPID:..." form.
	mux.HandleFunc("/api2/json/pve/remotes/dc-east/qemu/100/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Write([]byte(`{"data":"pve:dc-east!UPID:n1:0001:0002:6A00:qmsnapshot:100:root@pam:"}`))
			return
		}
		w.Write([]byte(`{"data":[{"name":"current","description":"You are here!"},{"name":"snapA"}]}`))
	})
	mux.HandleFunc("/api2/json/pve/remotes/dc-east/qemu/100/migrate", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":"pve:dc-east!UPID:n2:0001:0002:6A00:qmigrate:100:root@pam:"}`))
	})
	mux.HandleFunc("/api2/json/pve/remotes/dc-east/qemu/100/status", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"qmpstatus":"running","name":"web","cpus":2}}`))
	})
	// PDM control-plane domains (read samples)
	mux.HandleFunc("/api2/json/ceph/clusters", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"cluster":"ceph-a","name":"ceph-a"}]}`))
	})
	mux.HandleFunc("/api2/json/access/users", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"userid":"root@pam","enable":1},{"userid":"ops@pve","enable":1}]}`))
	})
	mux.HandleFunc("/api2/json/subscriptions/keys", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"key":"pdm-abc","status":"active"}]}`))
	})
	mux.HandleFunc("/api2/json/pbs/remotes", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"backup-1","type":"pbs"}]}`))
	})
	// proxied task polling
	mux.HandleFunc("/api2/json/pve/remotes/dc-east/tasks/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"upid":"UPID:n1:...","node":"n1","type":"qmstop","status":"stopped","exitstatus":"OK"}}`))
	})
	// proxied config read — real PDM requires the mandatory `state` enum param
	// (unlike direct PVE); reject if it is missing or not "active".
	mux.HandleFunc("/api2/json/pve/remotes/dc-east/qemu/100/config", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "active" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"data":null,"errors":{"state":"parameter is missing and it is not optional."}}`))
			return
		}
		w.Write([]byte(`{"data":{"cores":2,"name":"web","net0":"virtio,bridge=vmbr0"}}`))
	})
	return httptest.NewServer(mux)
}

func withPDMCreds(srv *httptest.Server, args ...string) []string {
	return append([]string{
		"--server", srv.URL, "--provider", "pdm",
		"--token-id", "root@pam!cli", "--token-secret", "secret",
	}, args...)
}

func TestRemoteListPDM(t *testing.T) {
	srv := pdmServer(t)
	defer srv.Close()
	out, err := runCLI(t, withPDMCreds(srv, "remote", "list")...)
	if err != nil {
		t.Fatalf("remote list: %v", err)
	}
	if !strings.Contains(out, "dc-east") || !strings.Contains(out, "dc-west") {
		t.Errorf("remote list output:\n%s", out)
	}
}

func TestPDMCrossRemoteGuestList(t *testing.T) {
	srv := pdmServer(t)
	defer srv.Close()
	out, err := runCLI(t, withPDMCreds(srv, "guest", "list", "-o", "value", "-c", "vmid", "-c", "remote")...)
	if err != nil {
		t.Fatalf("guest list (pdm): %v", err)
	}
	if !strings.Contains(out, "100 dc-east") || !strings.Contains(out, "200 dc-west") {
		t.Errorf("cross-remote guest list should tag remotes:\n%s", out)
	}
}

func TestPDMVmStopViaProxyWaitsOnTask(t *testing.T) {
	srv := pdmServer(t)
	defer srv.Close()
	// stop resolves the guest's remote (dc-east), POSTs the proxied stop, then
	// polls the proxied task status to completion (--wait default).
	out, err := runCLI(t, withPDMCreds(srv, "vm", "stop", "100", "--yes")...)
	if err != nil {
		t.Fatalf("pdm vm stop: %v", err)
	}
	_ = out // success = task polled to OK; no error
}

func TestPDMVmStopNoWaitReturnsTask(t *testing.T) {
	srv := pdmServer(t)
	defer srv.Close()
	out, err := runCLI(t, withPDMCreds(srv, "vm", "stop", "100", "--yes", "--no-wait")...)
	if err != nil {
		t.Fatalf("pdm vm stop --no-wait: %v", err)
	}
	if !strings.Contains(out, "UPID:n1") || !strings.Contains(out, "qmstop") {
		t.Errorf("expected proxied stop task id in output:\n%s", out)
	}
}

func TestPDMVmShowViaProxy(t *testing.T) {
	srv := pdmServer(t)
	defer srv.Close()
	out, err := runCLI(t, withPDMCreds(srv, "vm", "show", "100")...)
	if err != nil {
		t.Fatalf("pdm vm show: %v", err)
	}
	if !strings.Contains(out, "cores") {
		t.Errorf("expected config (cores) via PDM proxy:\n%s", out)
	}
}

func TestPDMRemoteFlagDisambiguates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/remotes/remote", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"dc-east","type":"pve","nodes":["n1:8006"]},{"id":"dc-west","type":"pve","nodes":["n3:8006"]}]}`))
	})
	// vmid 100 exists on BOTH remotes — ambiguous without --remote.
	mux.HandleFunc("/api2/json/resources/list", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[
			{"remote":"dc-east","resources":[{"type":"pve-qemu","vmid":100,"name":"east-web","node":"n1","status":"running"}]},
			{"remote":"dc-west","resources":[{"type":"pve-qemu","vmid":100,"name":"west-web","node":"n3","status":"running"}]}
		]}`))
	})
	mux.HandleFunc("/api2/json/pve/remotes/dc-west/qemu/100/config", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "active" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"data":{"name":"west-web","cores":4}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Without --remote: a clear conflict that points at the flag.
	if _, err := runCLI(t, withPDMCreds(srv, "vm", "show", "100")...); err == nil || !strings.Contains(err.Error(), "use --remote") {
		t.Fatalf("expected ambiguity conflict suggesting --remote, got %v", err)
	}
	// With --remote: resolves to that remote's guest.
	out, err := runCLI(t, withPDMCreds(srv, "vm", "show", "100", "--remote", "dc-west")...)
	if err != nil {
		t.Fatalf("vm show --remote dc-west: %v", err)
	}
	if !strings.Contains(out, "west-web") {
		t.Errorf("expected dc-west guest config, got:\n%s", out)
	}
}

func TestPDMVmResumeViaProxy(t *testing.T) {
	srv := pdmServer(t)
	defer srv.Close()
	// resume is a PDM-proxied power action; it must resolve the remote, POST the
	// proxied resume, and poll the task to completion.
	if _, err := runCLI(t, withPDMCreds(srv, "vm", "resume", "100")...); err != nil {
		t.Fatalf("pdm vm resume: %v", err)
	}
}

func TestParseTaskArgPDMPrefixed(t *testing.T) {
	// PDM emits "pve:<remote>!UPID:..."; the parser must extract the remote, keep
	// the full prefixed id as UPID (PDM's task path wants it), and tag backend pdm.
	h, err := parseTaskArg("pve:MP02!UPID:node1:0001:0002:6A00:qmstart:100:root@pam!tok:")
	if err != nil {
		t.Fatalf("parseTaskArg: %v", err)
	}
	if h.Remote != "MP02" || h.Backend != "pdm" || !strings.HasPrefix(h.UPID, "pve:MP02!UPID:") {
		t.Fatalf("bad PDM task handle: %+v", h)
	}
	// A bare UPID still parses as a PVE task.
	bare, err := parseTaskArg("UPID:node1:0001:0002:6A00:qmstart:100:root@pam:")
	if err != nil || bare.Remote != "" || bare.Backend != "pve" {
		t.Fatalf("bare UPID should parse as PVE: %+v err=%v", bare, err)
	}
}

func TestPDMGuestOpsViaProxy(t *testing.T) {
	srv := pdmServer(t)
	defer srv.Close()
	// snapshot create, migrate, and status are PDM-supported operations routed
	// through the /pve/remotes/{remote} proxy.
	if _, err := runCLI(t, withPDMCreds(srv, "vm", "snapshot", "create", "100", "snapA")...); err != nil {
		t.Fatalf("pdm snapshot create: %v", err)
	}
	out, err := runCLI(t, withPDMCreds(srv, "vm", "snapshot", "list", "100")...)
	if err != nil || !strings.Contains(out, "snapA") {
		t.Fatalf("pdm snapshot list: out=%q err=%v", out, err)
	}
	if _, err := runCLI(t, withPDMCreds(srv, "vm", "migrate", "100", "--target-node", "n2")...); err != nil {
		t.Fatalf("pdm migrate: %v", err)
	}
	out, err = runCLI(t, withPDMCreds(srv, "vm", "status", "100")...)
	if err != nil || !strings.Contains(out, "qmpstatus") {
		t.Fatalf("pdm status: out=%q err=%v", out, err)
	}
}

func TestPDMControlPlaneDomains(t *testing.T) {
	srv := pdmServer(t)
	defer srv.Close()
	cases := []struct{ args, want string }{
		{"ceph clusters", "ceph-a"},
		{"access user list", "ops@pve"},
		{"subscription key list", "pdm-abc"},
		{"pbs remotes", "backup-1"},
	}
	for _, c := range cases {
		out, err := runCLI(t, withPDMCreds(srv, append([]string{}, splitArgs(c.args)...)...)...)
		if err != nil || !strings.Contains(out, c.want) {
			t.Errorf("%q: want %q, got out=%q err=%v", c.args, c.want, out, err)
		}
	}
	// gated to PDM: refused on the PVE provider
	pve := fakeServer(t)
	defer pve.Close()
	if _, err := runCLI(t, withCreds(pve, "ceph", "clusters")...); err == nil || !strings.Contains(err.Error(), "requires the PDM provider") {
		t.Fatalf("expected PVE to refuse ceph, got %v", err)
	}
}

func splitArgs(s string) []string { return strings.Fields(s) }

func TestGuestExtrasRefusedOnPDM(t *testing.T) {
	srv := pdmServer(t)
	defer srv.Close()
	// PDM's proxy doesn't expose agent/disk-mgmt endpoints; these must refuse
	// cleanly (before any API call) rather than surface a raw 404.
	for _, args := range [][]string{
		{"vm", "agent", "osinfo", "100"},
		{"vm", "resize", "100", "--disk", "scsi0", "--size", "+1G"},
		{"vm", "cloudinit", "100"},
	} {
		if _, err := runCLI(t, withPDMCreds(srv, args...)...); err == nil || !strings.Contains(err.Error(), "not available via PDM") {
			t.Errorf("%v: expected PDM refusal, got %v", args, err)
		}
	}
}

func TestRemoteRejectedOnPVE(t *testing.T) {
	// Default provider is pve; remote must refuse.
	srv := fakeServer(t)
	defer srv.Close()
	_, err := runCLI(t, withCreds(srv, "remote", "list")...)
	if err == nil || !strings.Contains(err.Error(), "only available with the PDM provider") {
		t.Fatalf("expected PVE to reject remote list, got %v", err)
	}
}

func TestPDMProvisioningRefused(t *testing.T) {
	srv := pdmServer(t)
	defer srv.Close()
	_, err := runCLI(t, withPDMCreds(srv, "vm", "clone", "100", "999")...)
	if err == nil || !strings.Contains(err.Error(), "not available via PDM") {
		t.Fatalf("expected clone refusal on PDM, got %v", err)
	}
}
