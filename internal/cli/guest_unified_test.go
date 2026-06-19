package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// unifiedGuestServer models a cluster with a VM (100) and a CT (101). Proxmox
// uses one id namespace, so a vmid is unambiguous — the unified `pc guest`
// verbs must resolve the type and route to the matching API path.
func unifiedGuestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[
			{"type":"qemu","vmid":100,"name":"web","node":"pve-01","status":"stopped"},
			{"type":"lxc","vmid":101,"name":"dns","node":"pve-01","status":"running"}
		]}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/qemu/100/status/start", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":"UPID:pve-01:0001:0002:6A00:qmstart:100:root@pam!t:"}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/lxc/101/status/start", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":"UPID:pve-01:0005:0006:6A00:vzstart:101:root@pam!t:"}`))
	})
	return httptest.NewServer(mux)
}

func TestGuestStartRoutesByKind(t *testing.T) {
	srv := unifiedGuestServer(t)
	defer srv.Close()

	// A VM id resolves to the qemu endpoint.
	out, err := runCLI(t, withCreds(srv, "guest", "start", "100", "--no-wait")...)
	if err != nil {
		t.Fatalf("guest start 100 (vm): %v", err)
	}
	if !strings.Contains(out, "qmstart") {
		t.Errorf("expected a qemu start task, got:\n%s", out)
	}

	// A CT id resolves to the lxc endpoint — proving the unified command detects
	// the type and builds the right path (the core of #4). If it had routed to
	// qemu, this mux would 404 and the call would fail.
	out, err = runCLI(t, withCreds(srv, "guest", "start", "101", "--no-wait")...)
	if err != nil {
		t.Fatalf("guest start 101 (ct): %v", err)
	}
	if !strings.Contains(out, "vzstart") {
		t.Errorf("expected an lxc start task, got:\n%s", out)
	}
}

// A wrong-type assertion is still enforced on the typed trees: `pc ct start 100`
// where 100 is a VM must be rejected, even though `pc guest start 100` works.
func TestTypedCommandStillEnforcesKind(t *testing.T) {
	srv := unifiedGuestServer(t)
	defer srv.Close()
	_, err := runCLI(t, withCreds(srv, "ct", "start", "100", "--no-wait")...)
	if err == nil || !strings.Contains(err.Error(), "is a qemu, not a container") {
		t.Fatalf("expected wrong-kind rejection, got %v", err)
	}
}
