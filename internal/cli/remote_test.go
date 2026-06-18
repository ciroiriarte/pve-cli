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
	mux.HandleFunc("/api2/json/remotes", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"dc-east","type":"pve","nodes":"n1,n2"},{"id":"dc-west","type":"pve","nodes":"n3"}]}`))
	})
	mux.HandleFunc("/api2/json/resources/list", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[
			{"remote":"dc-east","resources":[{"type":"qemu","vmid":100,"name":"web","node":"n1","status":"running"}]},
			{"remote":"dc-west","resources":[{"type":"lxc","vmid":200,"name":"ct1","node":"n3","status":"stopped"}]}
		]}`))
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

func TestRemoteRejectedOnPVE(t *testing.T) {
	// Default provider is pve; remote must refuse.
	srv := fakeServer(t)
	defer srv.Close()
	_, err := runCLI(t, withCreds(srv, "remote", "list")...)
	if err == nil || !strings.Contains(err.Error(), "only available with the PDM provider") {
		t.Fatalf("expected PVE to reject remote list, got %v", err)
	}
}
