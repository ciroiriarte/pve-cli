package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// nodeAutoServer has an offline node (pve-00) and an online one (pve-01). Only
// pve-01 serves the storage-status endpoint, so a request that omits --node must
// auto-resolve to the online node — if it picked pve-00 the call would 404.
func nodeAutoServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[
			{"type":"node","node":"pve-00","status":"offline"},
			{"type":"node","node":"pve-01","status":"online"}
		]}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/storage/local/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":{"active":1,"avail":1000,"total":2000}}`))
	})
	return httptest.NewServer(mux)
}

func TestStorageStatusAutoResolvesOnlineNode(t *testing.T) {
	srv := nodeAutoServer(t)
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "storage", "status", "local")...)
	if err != nil {
		t.Fatalf("storage status without --node: %v", err)
	}
	if !strings.Contains(out, "active") {
		t.Errorf("expected status output, got:\n%s", out)
	}
}

func TestStorageStatusExplicitNodeStillWorks(t *testing.T) {
	srv := nodeAutoServer(t)
	defer srv.Close()

	// An explicit --node must skip auto-resolution and target exactly that node.
	out, err := runCLI(t, withCreds(srv, "storage", "status", "local", "--node", "pve-01")...)
	if err != nil {
		t.Fatalf("storage status --node pve-01: %v", err)
	}
	if !strings.Contains(out, "active") {
		t.Errorf("expected status output, got:\n%s", out)
	}
}
