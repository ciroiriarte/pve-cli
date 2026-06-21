package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func bynameServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	// Two VMs and a CT; "web-01" exists as both a VM (100) and a CT (300).
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[
			{"type":"qemu","vmid":100,"name":"web-01","node":"pve-01","status":"running"},
			{"type":"qemu","vmid":205,"name":"db-01","node":"pve-02","status":"running"},
			{"type":"lxc","vmid":300,"name":"web-01","node":"pve-01","status":"running"}
		]}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/qemu/100/config", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":{"name":"web-01","cores":2}}`))
	})
	return httptest.NewServer(mux)
}

// #20: `pc vm show <name>` resolves the name to its vmid. The kind scopes the
// search, so the VM "web-01" (100) is chosen over the CT "web-01" (300).
func TestResolveGuestByName(t *testing.T) {
	srv := bynameServer(t)
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "vm", "show", "web-01")...)
	if err != nil {
		t.Fatalf("vm show web-01: %v", err)
	}
	if !strings.Contains(out, "cores") {
		t.Errorf("expected the VM config (resolved 100), got:\n%s", out)
	}
}

// A name shared across guests of the same scope is an actionable conflict.
func TestResolveGuestByNameAmbiguous(t *testing.T) {
	srv := bynameServer(t)
	defer srv.Close()

	// `pc guest` has no kind, so both web-01 (VM 100, CT 300) match.
	_, err := runCLI(t, withCreds(srv, "guest", "show", "web-01")...)
	if err == nil || !strings.Contains(err.Error(), "vmids: 100, 300") {
		t.Fatalf("expected an ambiguity error listing both vmids, got %v", err)
	}
}

func TestResolveGuestByNameNotFound(t *testing.T) {
	srv := bynameServer(t)
	defer srv.Close()

	_, err := runCLI(t, withCreds(srv, "vm", "show", "ghost")...)
	if err == nil || !strings.Contains(err.Error(), `no VM named "ghost" found`) {
		t.Fatalf("expected not-found for an unknown name, got %v", err)
	}
}
