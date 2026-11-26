package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

type mutationLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *mutationLog) add(c string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, c)
}

func (l *mutationLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// pdmTargetServer serves a PDM where dc-east's inventory errored and vmid 100
// lives only on dc-west (so it looks unique on the incomplete listing), and
// vmid 300 collides across dc-west and dc-north.
func pdmTargetServer(t *testing.T) *httptest.Server {
	srv, _ := pdmTargetServerCounting(t)
	return srv
}

// pdmTargetServerCounting also counts proxied mutation (POST/DELETE) requests,
// so a test can assert a refused command never sent one, and records their
// paths. Every proxied guest has one old snapshot ("old").
func pdmTargetServerCounting(t *testing.T) (*httptest.Server, *mutationLog) {
	t.Helper()
	log := &mutationLog{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/pve/remotes/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodDelete {
			log.add(r.Method + " " + r.URL.Path)
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/snapshot") {
			w.Write([]byte(`{"data":[{"name":"current"},{"name":"old","snaptime":1000}]}`))
			return
		}
		if strings.Contains(r.URL.Path, "/tasks/") {
			w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
			return
		}
		// PDM returns proxied task ids prefixed with the remote.
		remote := strings.Split(r.URL.Path, "/")[5]
		w.Write([]byte(`{"data":"pve:` + remote + `!UPID:n3:0001:0002:6A00:qmstop:100:root@pam:"}`))
	})
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
	return httptest.NewServer(mux), log
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

// #37 review: on PDM, --node never skips resolution. The guest is resolved
// (scoped when a remote is known, failing closed on an errored remote) and
// --node must match; otherwise no mutation is sent.
func TestPDMNodeFlagStillResolves(t *testing.T) {
	withTempConfig(t)
	srv, log := pdmTargetServerCounting(t)
	defer srv.Close()

	refused := []struct {
		args []string
		want string
		code int
	}{
		// explicit remote whose inventory errored
		{[]string{"vm", "stop", "100", "--node", "n1", "--remote", "dc-east", "--yes", "--no-wait"}, "connection refused", ExitAPIServer},
		// explicit healthy remote, node belongs elsewhere
		{[]string{"vm", "stop", "100", "--node", "n1", "--remote", "dc-west", "--yes", "--no-wait"}, `not "n1"`, ExitValidation},
		// no remote: fails closed on the errored dc-east
		{[]string{"vm", "stop", "100", "--node", "n3", "--yes", "--no-wait"}, "dc-east", ExitAPIServer},
	}
	for _, c := range refused {
		_, err := runCLI(t, withPDMCreds(srv, c.args...)...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: expected error containing %q, got %v", c.args, c.want, err)
		} else if code := ExitCodeFor(err); code != c.code {
			t.Errorf("%v: exit code = %d, want %d", c.args, code, c.code)
		}
	}
	if calls := log.snapshot(); len(calls) != 0 {
		t.Fatalf("refused commands sent mutations: %v", calls)
	}

	// A matching --node on a healthy remote goes through to that remote.
	if _, err := runCLI(t, withPDMCreds(srv, "vm", "stop", "100", "--node", "n3", "--remote", "dc-west", "--yes", "--no-wait")...); err != nil {
		t.Fatalf("matching node: %v", err)
	}
	if calls := log.snapshot(); len(calls) != 1 || calls[0] != "POST /api2/json/pve/remotes/dc-west/qemu/100/stop" {
		t.Fatalf("expected one stop on dc-west, got %v", calls)
	}
}

func TestPDMNodeFlagWithContextRemote(t *testing.T) {
	srv, log := pdmTargetServerCounting(t)
	defer srv.Close()

	// Context remote is the errored remote: refused (exit 4), with or without --node.
	writeContextConfig(t, "pdm", "dc-east")
	for _, args := range [][]string{
		{"vm", "stop", "100", "--yes", "--no-wait"},
		{"vm", "stop", "100", "--node", "n1", "--yes", "--no-wait"},
		{"vm", "show", "west-web"},
	} {
		_, err := runCLI(t, withPDMCreds(srv, args...)...)
		if err == nil || !strings.Contains(err.Error(), "connection refused") || ExitCodeFor(err) != ExitAPIServer {
			t.Errorf("%v with errored context remote: expected exit 4 inventory error, got %v", args, err)
		}
	}

	// Context remote dc-north hosts 300 on n9; --node n3 is a mismatch.
	writeContextConfig(t, "pdm", "dc-north")
	if _, err := runCLI(t, withPDMCreds(srv, "vm", "stop", "300", "--node", "n3", "--yes", "--no-wait")...); err == nil || !strings.Contains(err.Error(), `not "n3"`) {
		t.Errorf("node mismatch on context remote: got %v", err)
	}
	if calls := log.snapshot(); len(calls) != 0 {
		t.Fatalf("refused commands sent mutations: %v", calls)
	}
}

// Commands that build PVE /nodes/... paths from a resolved guest are refused on
// PDM up front, naming the provider, instead of resolving across remotes.
func TestPDMNodePathGuestCommandsRefused(t *testing.T) {
	withTempConfig(t)
	srv, log := pdmTargetServerCounting(t)
	defer srv.Close()
	for _, args := range [][]string{
		{"backup", "create", "300", "--storage", "s", "--no-wait"},
		{"firewall", "rules", "--vmid", "300"},
	} {
		_, err := runCLI(t, withPDMCreds(srv, args...)...)
		if err == nil || !strings.Contains(err.Error(), "current provider: pdm") {
			t.Errorf("%v: expected PVE-only refusal naming the provider, got %v", args, err)
		}
	}
	if calls := log.snapshot(); len(calls) != 0 {
		t.Fatalf("refused commands sent mutations: %v", calls)
	}
}

// A remote-filtered snapshot list/prune surfaces that remote's inventory
// failure (exit 4) instead of silently finding nothing.
func TestPDMSnapshotErroredRemoteFilter(t *testing.T) {
	srv, log := pdmTargetServerCounting(t)
	defer srv.Close()

	check := func(label string, args ...string) {
		t.Helper()
		_, err := runCLI(t, withPDMCreds(srv, args...)...)
		if err == nil || !strings.Contains(err.Error(), "connection refused") || ExitCodeFor(err) != ExitAPIServer {
			t.Errorf("%s %v: expected exit 4 inventory error, got %v", label, args, err)
		}
	}
	withTempConfig(t)
	check("explicit", "snapshot", "list", "--remote", "dc-east")
	check("explicit", "snapshot", "prune", "--older-than", "1d", "--yes", "--remote", "dc-east")
	writeContextConfig(t, "pdm", "dc-east")
	check("context", "snapshot", "list")
	check("context", "snapshot", "prune", "--older-than", "1d", "--yes")
	if calls := log.snapshot(); len(calls) != 0 {
		t.Fatalf("refused prune sent mutations: %v", calls)
	}
}

// PVE keeps the typed --node shortcut: no /cluster/resources lookup at all.
func TestPVENodeShortcutSkipsInventory(t *testing.T) {
	withTempConfig(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"data":null}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/qemu/100/status/current", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":{"status":"running","vmid":100}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "vm", "status", "100", "--node", "pve-01")...)
	if err != nil || !strings.Contains(out, "running") {
		t.Fatalf("vm status --node on PVE with broken inventory: out=%q err=%v", out, err)
	}
}

// A name lookup is scoped by the context remote too.
func TestPDMContextRemoteByName(t *testing.T) {
	srv := pdmTargetServer(t)
	defer srv.Close()
	writeContextConfig(t, "pdm", "dc-north")

	out, err := runCLI(t, withPDMCreds(srv, "vm", "show", "north-db")...)
	if err != nil || !strings.Contains(out, "cfg-dc-north") {
		t.Fatalf("name on context remote: out=%q err=%v", out, err)
	}
	if _, err := runCLI(t, withPDMCreds(srv, "vm", "show", "west-db")...); err == nil || !strings.Contains(err.Error(), `on remote "dc-north"`) {
		t.Fatalf("name off the context remote: expected not-found on dc-north, got %v", err)
	}
}

// Bulk snapshot commands honor the pinned remote, so a pinned prune can't
// delete snapshots on other remotes; --remote still overrides.
func TestPDMSnapshotPruneHonorsContextRemote(t *testing.T) {
	srv, log := pdmTargetServerCounting(t)
	defer srv.Close()
	writeContextConfig(t, "pdm", "dc-north")

	if _, err := runCLI(t, withPDMCreds(srv, "snapshot", "prune", "--older-than", "1d", "--yes")...); err != nil {
		t.Fatalf("prune: %v", err)
	}
	calls := log.snapshot()
	if len(calls) == 0 {
		t.Fatal("pinned prune deleted nothing on dc-north")
	}
	for _, c := range calls {
		if !strings.Contains(c, "/remotes/dc-north/") {
			t.Errorf("pinned prune touched another remote: %s", c)
		}
	}

	out, err := runCLI(t, withPDMCreds(srv, "snapshot", "list", "--remote", "dc-west", "-o", "value", "-c", "remote")...)
	if err != nil || !strings.Contains(out, "dc-west") || strings.Contains(out, "dc-north") {
		t.Fatalf("snapshot list --remote override: out=%q err=%v", out, err)
	}
}

// #47: a PDM-proxied mutation's task handle carries the remote and the full
// prefixed id in `upid`, so `jq -r .upid | xargs pc task wait` keeps routing.
func TestPDMMutationTaskHandleJSON(t *testing.T) {
	srv := pdmTargetServer(t)
	defer srv.Close()
	out, err := runCLI(t, withPDMCreds(srv, "vm", "stop", "100", "--remote", "dc-west", "--yes", "--no-wait", "-o", "json")...)
	if err != nil {
		t.Fatalf("vm stop: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not a json object: %v\n%s", err, out)
	}
	if got["backend"] != "pdm" || got["remote"] != "dc-west" ||
		got["upid"] != "pve:dc-west!UPID:n3:0001:0002:6A00:qmstop:100:root@pam:" || got["display"] == nil {
		t.Errorf("unexpected PDM task handle: %v", got)
	}
}
