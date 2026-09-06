package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// capturePost returns a server that records the POST/PUT form of the first
// matching request and replies with a null data payload (a synchronous mutate).
func netMutateServer(t *testing.T, path string, got *url.Values, method *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		*got = r.PostForm
		*method = r.Method
		w.Write([]byte(`{"data":null}`))
	})
	return httptest.NewServer(mux)
}

// #26: node network create maps promoted flags to PVE params, joins --slaves
// into the space-separated form PVE expects, and defaults autostart on.
func TestNodeNetworkCreateBond(t *testing.T) {
	var got url.Values
	var method string
	srv := netMutateServer(t, "/api2/json/nodes/pve-01/network", &got, &method)
	defer srv.Close()

	_, err := runCLI(t, withCreds(srv, "node", "network", "create", "pve-01", "bond0",
		"--type", "bond", "--bond-mode", "active-backup", "--slaves", "eno1,eno2",
		"--comment", "uplink")...)
	if err != nil {
		t.Fatalf("node network create bond: %v", err)
	}
	if method != "POST" {
		t.Errorf("expected POST, got %s", method)
	}
	want := map[string]string{
		"iface": "bond0", "type": "bond", "bond_mode": "active-backup",
		"slaves": "eno1 eno2", "autostart": "1",
		// PVE's node-network schema spells it "comments" (plural) — regression
		// guard for the live-found 400 (verified against a PVE 9.1 node).
		"comments": "uplink",
	}
	if _, ok := got["comment"]; ok {
		t.Errorf("node network must send 'comments' (plural), not 'comment': %v", got)
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("param %q = %q, want %q (all: %v)", k, got.Get(k), v, got)
		}
	}
}

// A VLAN-aware bridge maps --vlan-aware/--vids to the bridge_* API keys and
// joins repeated --bridge-ports; --set is still an escape hatch.
func TestNodeNetworkCreateBridge(t *testing.T) {
	var got url.Values
	var method string
	srv := netMutateServer(t, "/api2/json/nodes/pve-01/network", &got, &method)
	defer srv.Close()

	_, err := runCLI(t, withCreds(srv, "node", "network", "create", "pve-01", "vmbr0",
		"--type", "bridge", "--bridge-ports", "bond0", "--bridge-ports", "eno3",
		"--vlan-aware", "--vids", "2-4094", "--cidr", "192.0.2.10/24",
		"--set", "mtu=9000")...)
	if err != nil {
		t.Fatalf("node network create bridge: %v", err)
	}
	want := map[string]string{
		"iface": "vmbr0", "type": "bridge", "bridge_ports": "bond0 eno3",
		"bridge_vlan_aware": "1", "bridge_vids": "2-4094",
		"cidr": "192.0.2.10/24", "mtu": "9000",
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("param %q = %q, want %q (all: %v)", k, got.Get(k), v, got)
		}
	}
}

// --iface is a hidden compatibility alias for the positional <iface>.
func TestNodeNetworkCreateIfaceFlagCompat(t *testing.T) {
	var got url.Values
	var method string
	srv := netMutateServer(t, "/api2/json/nodes/pve-01/network", &got, &method)
	defer srv.Close()

	_, err := runCLI(t, withCreds(srv, "node", "network", "create", "pve-01",
		"--iface", "vmbr0", "--type", "bridge")...)
	if err != nil {
		t.Fatalf("node network create --iface: %v", err)
	}
	if got.Get("iface") != "vmbr0" {
		t.Errorf("expected iface=vmbr0 via --iface, got %v", got)
	}
}

// --slaves on a non-bond type is rejected client-side (a caught typo).
func TestNodeNetworkCreateRejectsSlavesOnBridge(t *testing.T) {
	srv := netMutateServer(t, "/api2/json/nodes/pve-01/network", &url.Values{}, new(string))
	defer srv.Close()

	_, err := runCLI(t, withCreds(srv, "node", "network", "create", "pve-01", "vmbr0",
		"--type", "bridge", "--slaves", "eno1")...)
	if err == nil || !strings.Contains(err.Error(), "--slaves is only valid for --type bond") {
		t.Fatalf("expected a slaves/type validation error, got %v", err)
	}
}

// apply is confirm-gated: refused non-interactively without --yes, PUT with it.
func TestNodeNetworkApplyConfirmGate(t *testing.T) {
	var got url.Values
	var method string
	srv := netMutateServer(t, "/api2/json/nodes/pve-01/network", &got, &method)
	defer srv.Close()

	// Without --yes (non-interactive) it must refuse and make no request.
	if _, err := runCLI(t, withCreds(srv, "node", "network", "apply", "pve-01")...); err == nil ||
		!strings.Contains(err.Error(), "refusing destructive action") {
		t.Fatalf("expected apply to refuse without --yes, got %v", err)
	}
	if method != "" {
		t.Fatalf("apply issued a request despite refusal (method=%s)", method)
	}
	// With --yes it applies via PUT.
	if _, err := runCLI(t, withCreds(srv, "node", "network", "apply", "pve-01", "--yes")...); err != nil {
		t.Fatalf("node network apply --yes: %v", err)
	}
	if method != "PUT" {
		t.Errorf("expected PUT on apply, got %s", method)
	}
}

// delete is confirm-gated and DELETEs the iface path (name is path-escaped).
func TestNodeNetworkDelete(t *testing.T) {
	var got url.Values
	var method string
	srv := netMutateServer(t, "/api2/json/nodes/pve-01/network/vmbr0.10", &got, &method)
	defer srv.Close()

	if _, err := runCLI(t, withCreds(srv, "node", "network", "delete", "pve-01", "vmbr0.10", "--yes")...); err != nil {
		t.Fatalf("node network delete: %v", err)
	}
	if method != "DELETE" {
		t.Errorf("expected DELETE, got %s", method)
	}
}

// Node network writes are PVE-only; on PDM they fail fast before any request.
func TestNodeNetworkCreateRefusedOnPDM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("no request expected on PDM refusal, got %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, err := runCLI(t, withPDMCreds(srv, "node", "network", "create", "pve-01", "vmbr0", "--type", "bridge")...)
	if err == nil || !strings.Contains(err.Error(), "current provider: pdm") {
		t.Fatalf("expected a PVE-only refusal naming pdm, got %v", err)
	}
}
