package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// showMergeServer serves a VM whose /config has NO `cpu` model string while its
// /status/current reports a `cpu` utilization float — the exact collision from
// issue #27. Both endpoints also carry a `balloon` key with different meanings.
func showMergeServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[
			{"type":"qemu","vmid":100,"name":"web-01","node":"pve-01","status":"running"}
		]}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/qemu/100/config", func(w http.ResponseWriter, _ *http.Request) {
		// No top-level cpu model here — this is what let the status float leak.
		w.Write([]byte(`{"data":{"name":"web-01","cores":4,"balloon":8192}}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/qemu/100/status/current", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":{"status":"running","cpu":0.00104581058719836,"mem":8589934592,"balloon":8589934592,"uptime":3600}}`))
	})
	return httptest.NewServer(mux)
}

// #27: `show` must not flatten /status/current into the config namespace, where
// the runtime `cpu` float leaks in (or shadows the config `cpu` model string).
// Live status lives under a dedicated `status` key so the two responses never
// clash.
func TestGuestShowNestsStatusNoLeak(t *testing.T) {
	srv := showMergeServer(t)
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "vm", "show", "100", "--format", "json")...)
	if err != nil {
		t.Fatalf("vm show 100: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("expected a JSON object, got %s (%v)", out, err)
	}

	// Config keys stay at the top level, untouched.
	if obj["cores"] != float64(4) {
		t.Errorf("expected top-level .cores == 4, got %v", obj["cores"])
	}
	// The config balloon (MiB) must not be overwritten by the status balloon (bytes).
	if obj["balloon"] != float64(8192) {
		t.Errorf("expected config .balloon == 8192 preserved, got %v", obj["balloon"])
	}
	// The status float must NOT have leaked into the top-level cpu key.
	if v, ok := obj["cpu"]; ok {
		t.Errorf("runtime cpu utilization leaked to top-level .cpu = %v (issue #27)", v)
	}

	// Live runtime lives under .status.
	st, ok := obj["status"].(map[string]any)
	if !ok {
		t.Fatalf("expected live status nested under .status, got %v", obj["status"])
	}
	if st["cpu"] != 0.00104581058719836 {
		t.Errorf("expected .status.cpu utilization float, got %v", st["cpu"])
	}
	if st["status"] != "running" {
		t.Errorf("expected .status.status == running, got %v", st["status"])
	}
	if st["balloon"] != float64(8589934592) {
		t.Errorf("expected .status.balloon (bytes) == 8589934592, got %v", st["balloon"])
	}
}
