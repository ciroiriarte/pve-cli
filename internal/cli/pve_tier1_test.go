package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func tier1Server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/pools", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"poolid":"prod","comment":"production"},{"poolid":"dev"}]}`))
	})
	mux.HandleFunc("/api2/json/cluster/ha/status/current", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"quorum","type":"quorum","status":"OK","node":"pve-01"}]}`))
	})
	// resize resolves the guest's node via /cluster/resources, then PUTs resize.
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"type":"qemu","vmid":100,"name":"web","node":"pve-01","status":"running"}]}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/qemu/100/resize", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.Write([]byte(`{"data":null}`))
	})
	return httptest.NewServer(mux)
}

func TestPVETier1Commands(t *testing.T) {
	srv := tier1Server(t)
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "pool", "list")...)
	if err != nil || !strings.Contains(out, "prod") {
		t.Fatalf("pool list: out=%q err=%v", out, err)
	}
	out, err = runCLI(t, withCreds(srv, "ha", "status")...)
	if err != nil || !strings.Contains(out, "quorum") {
		t.Fatalf("ha status: out=%q err=%v", out, err)
	}
	// resize is a synchronous PUT; success = no error.
	if _, err := runCLI(t, withCreds(srv, "vm", "resize", "100", "--disk", "scsi0", "--size", "+1G")...); err != nil {
		t.Fatalf("vm resize: %v", err)
	}
	// missing required flags must error before any request.
	if _, err := runCLI(t, withCreds(srv, "vm", "resize", "100")...); err == nil {
		t.Fatalf("expected resize to require --disk/--size")
	}
}
