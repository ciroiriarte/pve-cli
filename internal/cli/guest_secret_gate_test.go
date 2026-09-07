package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func oneVMServer(t *testing.T, capture map[string]*url.Values) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[{"type":"qemu","vmid":100,"name":"web","node":"pve-01","status":"running"}]}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/qemu/100/unlink", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		v := r.PostForm
		capture["unlink"] = &v
		w.Write([]byte(`{"data":null}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/qemu/100/agent/set-user-password", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		v := r.PostForm
		capture["setpw"] = &v
		w.Write([]byte(`{"data":null}`))
	})
	return httptest.NewServer(mux)
}

// #1: unlink detaches disks (and --force deletes the image) — it must be
// confirm-gated like every other destructive verb, refusing without --yes.
func TestGuestUnlinkConfirmGate(t *testing.T) {
	capture := map[string]*url.Values{}
	srv := oneVMServer(t, capture)
	defer srv.Close()

	// Without --yes (non-interactive) it must refuse and issue no request.
	_, err := runCLI(t, withCreds(srv, "vm", "unlink", "100", "--disks", "scsi1")...)
	if err == nil || !strings.Contains(err.Error(), "refusing destructive action") {
		t.Fatalf("expected unlink to refuse without --yes, got %v", err)
	}
	if capture["unlink"] != nil {
		t.Fatalf("unlink issued a request despite refusal: %v", capture["unlink"])
	}
	// With --yes it proceeds.
	if _, err := runCLI(t, withCreds(srv, "vm", "unlink", "100", "--disks", "scsi1", "--yes")...); err != nil {
		t.Fatalf("unlink --yes: %v", err)
	}
	if capture["unlink"] == nil || capture["unlink"].Get("idlist") != "scsi1" {
		t.Fatalf("expected unlink idlist=scsi1, got %v", capture["unlink"])
	}
}

// #3: agent set-password resolves the password off-argv via --password-ref, so
// the secret never appears on the command line.
func TestGuestSetPasswordFromRef(t *testing.T) {
	capture := map[string]*url.Values{}
	srv := oneVMServer(t, capture)
	defer srv.Close()

	t.Setenv("PC_GUEST_PW", "s3kret")
	_, err := runCLI(t, withCreds(srv, "vm", "agent", "set-password", "100",
		"--user", "root", "--password-ref", "env:PC_GUEST_PW")...)
	if err != nil {
		t.Fatalf("set-password --password-ref: %v", err)
	}
	if capture["setpw"] == nil || capture["setpw"].Get("password") != "s3kret" || capture["setpw"].Get("username") != "root" {
		t.Fatalf("expected password resolved from env, got %v", capture["setpw"])
	}
	// --password and --password-ref are mutually exclusive.
	if _, err := runCLI(t, withCreds(srv, "vm", "agent", "set-password", "100",
		"--user", "root", "--password", "x", "--password-ref", "env:PC_GUEST_PW")...); err == nil ||
		!strings.Contains(err.Error(), "password") {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}
