package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStorageList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/storage", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"storage":"local","type":"dir","content":"iso,vztmpl"},{"storage":"ceph","type":"rbd","content":"images"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "storage", "list")...)
	if err != nil {
		t.Fatalf("storage list: %v", err)
	}
	if !strings.Contains(out, "local") || !strings.Contains(out, "ceph") {
		t.Errorf("storage list output:\n%s", out)
	}
}

func TestGuestListUnified(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[
			{"type":"qemu","vmid":100,"name":"web","node":"pve-01","status":"running"},
			{"type":"lxc","vmid":200,"name":"ct1","node":"pve-01","status":"stopped"}
		]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "guest", "list", "-o", "value", "-c", "vmid", "-c", "kind")...)
	if err != nil {
		t.Fatalf("guest list: %v", err)
	}
	if !strings.Contains(out, "100 qemu") || !strings.Contains(out, "200 lxc") {
		t.Errorf("unified guest list should include both kinds:\n%s", out)
	}
}
