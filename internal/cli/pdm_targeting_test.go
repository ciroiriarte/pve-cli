package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// pdmTargetServer serves a PDM where dc-east's inventory errored and vmid 100
// lives only on dc-west (so it looks unique on the incomplete listing), and
// vmid 300 collides across dc-west and dc-north.
func pdmTargetServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/resources/list", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[
			{"remote":"dc-east","error":"connection refused","resources":[]},
			{"remote":"dc-west","resources":[
				{"type":"pve-node","node":"n3","status":"online"},
				{"type":"pve-qemu","vmid":100,"name":"west-web","node":"n3","status":"running"},
				{"type":"pve-qemu","vmid":300,"name":"west-db","node":"n3","status":"running"}
			]},
			{"remote":"dc-north","resources":[{"type":"pve-qemu","vmid":300,"name":"north-db","node":"n9","status":"running"}]}
		]}`))
	})
	for _, p := range []string{
		"/api2/json/pve/remotes/dc-west/qemu/100/config",
		"/api2/json/pve/remotes/dc-west/qemu/300/config",
		"/api2/json/pve/remotes/dc-north/qemu/300/config",
	} {
		name := strings.Split(p, "/")[5] // remote id
		mux.HandleFunc(p, func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"data":{"name":"cfg-` + name + `","cores":2}}`))
		})
	}
	return httptest.NewServer(mux)
}

// writeContextConfig writes a config whose current context pins `remote`.
func writeContextConfig(t *testing.T, provider, remote string) {
	t.Helper()
	path := withTempConfig(t)
	cfg := "current_context: corp\n" +
		"contexts:\n  corp: { profile: p, remote: " + remote + " }\n" +
		"profiles:\n  p: { provider: " + provider + ", server: https://unused:8006, auth: { type: token, token_id: u@pam!c } }\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// #37: an errored remote makes the inventory incomplete — a vmid that looks
// unique may also live on the failed remote, so unscoped resolution refuses.
func TestPDMErroredRemoteRefusesUnscoped(t *testing.T) {
	withTempConfig(t)
	srv := pdmTargetServer(t)
	defer srv.Close()

	for _, args := range [][]string{{"vm", "show", "100"}, {"vm", "show", "west-web"}} {
		_, err := runCLI(t, withPDMCreds(srv, args...)...)
		if err == nil || !strings.Contains(err.Error(), "dc-east") || !strings.Contains(err.Error(), "--remote") {
			t.Fatalf("%v: expected refusal naming dc-east and --remote, got %v", args, err)
		}
		if code := ExitCodeFor(err); code != ExitAPIServer {
			t.Errorf("%v: exit code = %d, want %d", args, code, ExitAPIServer)
		}
	}
}

func TestPDMErroredRemoteScoped(t *testing.T) {
	withTempConfig(t)
	srv := pdmTargetServer(t)
	defer srv.Close()

	// Scoped to the healthy remote: resolves.
	out, err := runCLI(t, withPDMCreds(srv, "vm", "show", "100", "--remote", "dc-west")...)
	if err != nil || !strings.Contains(out, "cfg-dc-west") {
		t.Fatalf("scoped to healthy remote: out=%q err=%v", out, err)
	}
	// Scoped to the errored remote: fails, naming its error.
	_, err = runCLI(t, withPDMCreds(srv, "vm", "show", "100", "--remote", "dc-east")...)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("scoped to errored remote: expected inventory error, got %v", err)
	}
}

// Lists keep working but surface the failed remote on stderr.
func TestPDMListWarnsOnErroredRemote(t *testing.T) {
	withTempConfig(t)
	srv := pdmTargetServer(t)
	defer srv.Close()

	for _, args := range [][]string{{"guest", "list"}, {"node", "list"}} {
		old := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w
		out, err := runCLI(t, withPDMCreds(srv, args...)...)
		w.Close()
		os.Stderr = old
		stderr, _ := io.ReadAll(r)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(out, "n3") {
			t.Errorf("%v: healthy remote missing from output:\n%s", args, out)
		}
		if !strings.Contains(string(stderr), "dc-east") || strings.Contains(out, "warning") {
			t.Errorf("%v: expected dc-east warning on stderr only; stderr=%q", args, stderr)
		}
	}
}

// The context's pinned remote applies without --remote; the flag overrides it.
func TestPDMContextRemote(t *testing.T) {
	srv := pdmTargetServer(t)
	defer srv.Close()
	writeContextConfig(t, "pdm", "dc-north")

	// Colliding vmid 300 resolves via the context remote (dc-north), and the
	// errored dc-east doesn't matter because resolution is scoped.
	out, err := runCLI(t, withPDMCreds(srv, "vm", "show", "300")...)
	if err != nil || !strings.Contains(out, "cfg-dc-north") {
		t.Fatalf("context remote: out=%q err=%v", out, err)
	}
	// --remote wins over the context.
	out, err = runCLI(t, withPDMCreds(srv, "vm", "show", "300", "--remote", "dc-west")...)
	if err != nil || !strings.Contains(out, "cfg-dc-west") {
		t.Fatalf("flag override: out=%q err=%v", out, err)
	}
	// vmid 100 isn't on the pinned remote: not found there (no silent fallback).
	if _, err := runCLI(t, withPDMCreds(srv, "vm", "show", "100")...); err == nil || !strings.Contains(err.Error(), `remote "dc-north"`) {
		t.Fatalf("expected not-found on context remote, got %v", err)
	}
}

// A context `remote:` is PDM-only; on PVE it is ignored rather than tripping the
// "--remote is only supported with the PDM provider" error.
func TestPVEIgnoresContextRemote(t *testing.T) {
	srv := bynameServer(t)
	defer srv.Close()
	writeContextConfig(t, "pve", "dc-west")

	for _, args := range [][]string{{"vm", "show", "100"}, {"vm", "show", "web-01"}} {
		out, err := runCLI(t, withCreds(srv, args...)...)
		if err != nil || !strings.Contains(out, "cores") {
			t.Fatalf("%v on PVE with context remote: out=%q err=%v", args, out, err)
		}
	}
}
