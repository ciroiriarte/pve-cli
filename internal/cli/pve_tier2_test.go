package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func tier2Server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/sdn/zones", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"zone":"dmz","type":"vlan"},{"zone":"prod","type":"vxlan"}]}`))
	})
	mux.HandleFunc("/api2/json/cluster/firewall/rules", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"pos":0,"type":"in","action":"ACCEPT","proto":"tcp","dport":"22"}]}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/firewall/rules", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"pos":0,"type":"in","action":"DROP"}]}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/ceph/osd", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"root":{"name":"default"},"flags":"sortbitwise"}}`))
	})
	return httptest.NewServer(mux)
}

func TestPVETier2Commands(t *testing.T) {
	srv := tier2Server(t)
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "sdn", "zone", "list")...)
	if err != nil || !strings.Contains(out, "dmz") {
		t.Fatalf("sdn zone list: out=%q err=%v", out, err)
	}
	out, err = runCLI(t, withCreds(srv, "firewall", "rules")...) // cluster scope
	if err != nil || !strings.Contains(out, "ACCEPT") {
		t.Fatalf("firewall rules (cluster): out=%q err=%v", out, err)
	}
	out, err = runCLI(t, withCreds(srv, "firewall", "rules", "--node", "pve-01")...) // node scope
	if err != nil || !strings.Contains(out, "DROP") {
		t.Fatalf("firewall rules --node: out=%q err=%v", out, err)
	}
	out, err = runCLI(t, withCreds(srv, "ceph", "osd", "list", "--node", "pve-01")...)
	if err != nil || !strings.Contains(out, "sortbitwise") {
		t.Fatalf("ceph osd list --node: out=%q err=%v", out, err)
	}
	// ceph management requires --node
	if _, err := runCLI(t, withCreds(srv, "ceph", "osd", "list")...); err == nil {
		t.Fatalf("expected ceph osd list to require --node")
	}
}
