package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const pbsLongLogLines = 120

// pbsLongLog emulates PBS's task-log paging for the prune task: `limit`
// defaults to 50 lines and 0 means "all lines".
func pbsLongLog(q url.Values) string {
	limit := 50
	if v := q.Get("limit"); v != "" {
		limit, _ = strconv.Atoi(v)
	}
	start, _ := strconv.Atoi(q.Get("start"))
	var lines []string
	for n := start + 1; n <= pbsLongLogLines; n++ {
		if limit > 0 && len(lines) == limit {
			break
		}
		text := fmt.Sprintf("would remove vm/100/snapshot-%03d", n)
		if n == pbsLongLogLines {
			text = "TASK OK"
		}
		lines = append(lines, fmt.Sprintf(`{"n":%d,"t":%q}`, n, text))
	}
	return `{"data":[` + strings.Join(lines, ",") + `]}`
}

// #19 review: --set must not override keys that have a flag — `--dry-run
// --set dry-run=0` would otherwise prune for real with no prompt, and
// --set ns/backup-* could change the scope behind the confirmation.
func TestPBSFlaggedSetKeysRefused(t *testing.T) {
	srv := pbsServer(t)
	cases := [][]string{
		{"datastore", "prune", "store1", "--keep-last", "1", "--dry-run", "--set", "dry-run=0"},
		{"datastore", "prune", "store1", "--keep-last", "1", "--dry-run", "--set", "ns=prod"},
		{"datastore", "prune", "store1", "--keep-last", "1", "--dry-run", "--set", "backup-type=vm", "--set", "backup-id=100"},
		{"datastore", "prune", "store1", "--keep-last", "1", "--yes", "--set", "keep-last=0"},
		{"datastore", "prune", "store1", "--keep-last", "1", "--yes", "--set", "max-depth=0"},
		{"datastore", "prune", "store1", "--keep-last", "1", "--yes", "--set", "store=other"},
		{"datastore", "verify", "start", "store1", "--yes", "--set", "ns=prod"},
		{"datastore", "verify", "start", "store1", "--yes", "--set", "backup-id=100"},
		{"datastore", "verify", "start", "store1", "--yes", "--set", "ignore-verified=1"},
	}
	for _, args := range cases {
		_, err := runCLI(t, withPBSCreds(srv, args...)...)
		if err == nil || !strings.Contains(err.Error(), "is not allowed") {
			t.Errorf("%v: want a --set refusal, got %v", args, err)
		}
	}
	for _, p := range []string{"/admin/datastore/store1/prune-datastore", "/admin/datastore/store1/prune", "/admin/datastore/store1/verify"} {
		if n := srv.count("POST", p); n != 0 {
			t.Errorf("POST %s sent %d times despite the refusal", p, n)
		}
	}

	// Keys without a flag still pass through.
	var h map[string]any
	runJSON(t, &h, withPBSCreds(srv, "datastore", "verify", "start", "store1", "--yes", "--set", "read-threads=2")...)
	if c, _ := srv.last("POST", "/admin/datastore/store1/verify"); c.Form.Get("read-threads") != "2" {
		t.Errorf("unflagged --set key not forwarded: %v", c.Form)
	}
}

// #19 review: PBS task logs default to 50 lines; an unset limit must fetch
// them all (limit=0), for `pc task log` and the dry-run prune preview.
func TestPBSTaskLogNotTruncated(t *testing.T) {
	srv := pbsServer(t)
	out, err := runCLI(t, withPBSCreds(srv, "task", "log", pbsPruneUPID)...)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out, "\n"); got != pbsLongLogLines {
		t.Errorf("task log printed %d lines, want %d", got, pbsLongLogLines)
	}
	if c, _ := srv.last("GET", "/nodes/localhost/tasks/"+pbsPruneUPID+"/log"); c.Query.Get("limit") != "0" {
		t.Errorf("task log limit = %q, want 0 (all lines)", c.Query.Get("limit"))
	}

	out, err = runCLI(t, withPBSCreds(srv, "datastore", "prune", "store1", "--keep-last", "2", "--dry-run")...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "snapshot-119") || !strings.Contains(out, "TASK OK") {
		t.Errorf("dry-run preview truncated:\n%s", out)
	}

	// An explicit --limit is still honored.
	out, _ = runCLI(t, withPBSCreds(srv, "task", "log", pbsPruneUPID, "--limit", "10")...)
	if got := strings.Count(out, "\n"); got != 10 {
		t.Errorf("task log --limit 10 printed %d lines", got)
	}
}

// #19 review: -o JSON (any case) is machine output — the dry-run task log must
// not be mixed into it.
func TestPBSDryRunUppercaseJSONStaysClean(t *testing.T) {
	srv := pbsServer(t)
	out, err := runCLI(t, withPBSCreds(srv, "datastore", "prune", "store1", "--keep-last", "2", "--dry-run", "-o", "JSON")...)
	if err != nil {
		t.Fatal(err)
	}
	var h map[string]any
	if err := json.Unmarshal([]byte(out), &h); err != nil || h["upid"] != pbsPruneUPID {
		t.Errorf("-o JSON output is not just the task handle (%v):\n%s", err, out)
	}
}

// writeConfig writes a config file and points PVE_CLI_CONFIG at it.
func writeConfig(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PVE_CLI_CONFIG", p)
}

// #19 review: a mis-cased provider is normalized, an unknown one doesn't lock
// out the local commands that repair the config, and a typo'd profile reports
// "profile not found" rather than a provider mismatch.
func TestProviderGateConfigEdgeCases(t *testing.T) {
	srv := pbsServer(t)

	// provider: PBS works like pbs.
	writeConfig(t, "current_context: b\ncontexts:\n  b: {profile: b}\nprofiles:\n  b:\n    provider: PBS\n    server: "+srv.URL+"\n    auth: {type: token, token_id: 'root@pam!cli', secret: secret}\n")
	if _, err := runCLI(t, "datastore", "list"); err != nil {
		t.Errorf("provider: PBS should select pbs: %v", err)
	}
	if _, err := runCLI(t, "--provider", "PBS", "vm", "list"); err == nil || !strings.Contains(err.Error(), "current provider: pbs") {
		t.Errorf("--provider PBS vm list: %v", err)
	}

	// An unknown provider: local commands still run; remote ones get the
	// provider factory's "unknown provider" error, not a gate refusal.
	writeConfig(t, "current_context: b\ncontexts:\n  b: {profile: b}\n  other: {profile: b}\nprofiles:\n  b:\n    provider: proxmox\n    server: "+srv.URL+"\n    auth: {type: token, token_id: 'root@pam!cli', secret: secret}\n")
	for _, args := range [][]string{{"version"}, {"context", "use", "other"}, {"config", "view"}} {
		if _, err := runCLI(t, args...); err != nil {
			t.Errorf("%v with an unknown provider: %v", args, err)
		}
	}
	if _, err := runCLI(t, "datastore", "list"); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("datastore list with an unknown provider: %v", err)
	}

	// A typo'd --profile surfaces the real error.
	if _, err := runCLI(t, "--profile", "typo", "datastore", "list"); err == nil || !strings.Contains(err.Error(), `profile "typo" not found`) {
		t.Errorf("--profile typo: %v", err)
	}
}

// Every top-level command against the gate on each provider: unannotated
// commands run on pve/pdm only, backend-agnostic ones everywhere, and
// datastore on pbs only. Guards AGENTS.md §14's "annotate it" rule.
func TestProviderGateAllTopLevelCommands(t *testing.T) {
	root := NewRootCmd()
	pbsOK := map[string]bool{"datastore": true, "task": true, "raw": true, "api": true, "config": true, "context": true, "auth": true, "version": true, "plugin": true}
	for _, c := range root.Commands() {
		for _, prov := range []string{"pve", "pdm", "pbs"} {
			want := c.Name() != "datastore"
			if prov == "pbs" {
				want = pbsOK[c.Name()]
			}
			err := providerGate(c, prov)
			if got := err == nil; got != want {
				t.Errorf("pc %s on %s: allowed=%v, want %v (err %v)", c.Name(), prov, got, want, err)
			}
			if err != nil && !strings.Contains(err.Error(), "current provider: "+prov) {
				t.Errorf("pc %s on %s: error should name the provider: %v", c.Name(), prov, err)
			}
		}
	}
}
