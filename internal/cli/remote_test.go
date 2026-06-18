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
	// proxied task polling
	mux.HandleFunc("/api2/json/pve/remotes/dc-east/tasks/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"upid":"UPID:n1:...","node":"n1","type":"qmstop","status":"stopped","exitstatus":"OK"}}`))
	})
	// proxied config read
	mux.HandleFunc("/api2/json/pve/remotes/dc-east/qemu/100/config", func(w http.ResponseWriter, r *http.Request) {
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
