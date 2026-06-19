package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #12: the raw/api escape hatches must not mutate without a confirm. In
// non-interactive mode confirm() refuses unless --yes is given.
func TestApiWriteRequiresConfirm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("endpoint must not be hit without confirmation")
	}))
	defer srv.Close()

	_, err := runCLI(t, withCreds(srv, "api", "DELETE", "/nodes/pve-01/qemu/100")...)
	if err == nil || !strings.Contains(err.Error(), "refusing destructive action") {
		t.Fatalf("expected confirm refusal for api DELETE, got %v", err)
	}
}

func TestApiWriteWithYesProceeds(t *testing.T) {
	hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/pve-01/qemu/100", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			hits++
		}
		w.Write([]byte(`{"data":null}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, err := runCLI(t, withCreds(srv, "api", "DELETE", "/nodes/pve-01/qemu/100", "--yes")...); err != nil {
		t.Fatalf("api DELETE --yes: %v", err)
	}
	if hits != 1 {
		t.Fatalf("expected the DELETE to be issued once with --yes, got %d", hits)
	}
}

func TestApiGetNeedsNoConfirm(t *testing.T) {
	hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Write([]byte(`{"data":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// No --yes, non-interactive: a GET must still go through untouched.
	if _, err := runCLI(t, withCreds(srv, "api", "GET", "/cluster/resources")...); err != nil {
		t.Fatalf("api GET: %v", err)
	}
	if hits != 1 {
		t.Fatalf("expected GET to be issued without a prompt, got %d", hits)
	}
}

// #6: sdn apply reloads networking cluster-wide and must be confirm-gated.
func TestSdnApplyRequiresConfirm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("sdn apply must not PUT without confirmation")
	}))
	defer srv.Close()

	_, err := runCLI(t, withCreds(srv, "sdn", "apply")...)
	if err == nil || !strings.Contains(err.Error(), "refusing destructive action") {
		t.Fatalf("expected confirm refusal for sdn apply, got %v", err)
	}
}
