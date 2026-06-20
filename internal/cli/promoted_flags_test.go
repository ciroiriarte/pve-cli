package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// #14: promoted first-class flags populate the request, and --set still works as
// an escape hatch for uncurated keys.
func TestBackupJobCreatePromotedFlags(t *testing.T) {
	var got url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/backup", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		w.Write([]byte(`{"data":null}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := runCLI(t, withCreds(srv, "backup", "job", "create",
		"--storage", "nfs", "--schedule", "02:00", "--all", "--mode", "snapshot",
		"--enabled=false", "--set", "compress=zstd")...)
	if err != nil {
		t.Fatalf("backup job create: %v", err)
	}
	want := map[string]string{
		"storage": "nfs", "schedule": "02:00", "mode": "snapshot",
		"all": "1", "enabled": "0", "compress": "zstd",
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("param %q = %q, want %q (all: %v)", k, got.Get(k), v, got)
		}
	}
}

// --set overrides a promoted flag (escape hatch wins), and an unset boolean is
// omitted so the API keeps its default.
func TestBackupJobCreateSetOverridesAndOmitsUnsetBools(t *testing.T) {
	var got url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/backup", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		w.Write([]byte(`{"data":null}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := runCLI(t, withCreds(srv, "backup", "job", "create",
		"--storage", "nfs", "--set", "storage=override")...)
	if err != nil {
		t.Fatalf("backup job create: %v", err)
	}
	if got.Get("storage") != "override" {
		t.Errorf("--set should win over the promoted flag, got storage=%q", got.Get("storage"))
	}
	if _, ok := got["all"]; ok {
		t.Errorf("unset --all must be omitted, got all=%q", got.Get("all"))
	}
}

func TestSDNVnetCreatePromotedTag(t *testing.T) {
	var got url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/sdn/vnets", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		w.Write([]byte(`{"data":null}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := runCLI(t, withCreds(srv, "sdn", "vnet", "create", "v100", "--zone", "dmz", "--tag", "100")...)
	if err != nil {
		t.Fatalf("sdn vnet create: %v", err)
	}
	if got.Get("vnet") != "v100" || got.Get("zone") != "dmz" || got.Get("tag") != "100" {
		t.Errorf("unexpected params: %v", got)
	}
}

// #9: config test-auth resolves and probes /version when online.
func TestConfigTestAuthProbe(t *testing.T) {
	hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/version", func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Write([]byte(`{"data":{"version":"9.1.6"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "config", "test-auth")...)
	if err != nil {
		t.Fatalf("config test-auth: %v", err)
	}
	if hits != 1 {
		t.Fatalf("expected exactly one /version probe, got %d", hits)
	}
	if !strings.Contains(out, "probe:      OK") || !strings.Contains(out, "provider:   pve") {
		t.Errorf("unexpected report:\n%s", out)
	}
}

func TestConfigTestAuthOffline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("--offline must not make any request")
	}))
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "config", "test-auth", "--offline")...)
	if err != nil {
		t.Fatalf("config test-auth --offline: %v", err)
	}
	if !strings.Contains(out, "SKIPPED (--offline)") {
		t.Errorf("expected probe skipped, got:\n%s", out)
	}
}
