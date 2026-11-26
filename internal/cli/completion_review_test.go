package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/config"
)

// completeDirective runs `pc __complete` and returns the suggestions and the
// `:N` directive line.
func completeDirective(t *testing.T, args ...string) ([]string, string) {
	t.Helper()
	out, err := runCLI(t, append([]string{"__complete"}, args...)...)
	if err != nil {
		t.Fatalf("__complete %v: %v", args, err)
	}
	var got []string
	dir := ""
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		switch {
		case strings.HasPrefix(l, ":"):
			dir = l
		case l != "":
			got = append(got, l)
		}
	}
	return got, dir
}

func writeConfig(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PVE_CLI_CONFIG", p)
}

// Slots the live completer doesn't own keep cobra's default (file) completion.
func TestCompleteUnownedSlotKeepsFileCompletion(t *testing.T) {
	isolateCache(t)
	srv := completionServer(t, nil)
	if _, dir := completeDirective(t, withCreds(srv, "storage", "content", "upload", "local", "")...); dir != ":0" {
		t.Errorf("upload <file> directive = %s, want :0 (file completion)", dir)
	}
	got, dir := completeDirective(t, withCreds(srv, "storage", "content", "upload", "")...)
	if dir != ":4" || !reflect.DeepEqual(got, []string{"ceph-rbd\trbd", "local\tdir"}) {
		t.Errorf("upload <storage> = %q %s", got, dir)
	}
}

// Flags declared with PersistentFlags() on a group get completion too.
func TestCompletePersistentFlags(t *testing.T) {
	isolateCache(t)
	srv := completionServer(t, nil)
	for _, args := range [][]string{
		{"vm", "snapshot", "list", "--node", ""},
		{"vm", "tag", "list", "--node", ""},
		{"firewall", "rules", "--node", ""},
		{"ceph", "osd", "list", "--node", ""},
	} {
		if got := complete(t, withCreds(srv, args...)...); !reflect.DeepEqual(got, []string{"pve-01"}) {
			t.Errorf("__complete %v = %q, want [pve-01]", args, got)
		}
	}
	got := complete(t, withCreds(srv, "firewall", "rules", "--vmid", "")...)
	if len(got) == 0 || got[0] != "100\tweb-01" {
		t.Errorf("firewall --vmid completion = %q", got)
	}
}

// The context's pinned remote scopes PDM guest suggestions like resolveGuest;
// an explicit --remote wins.
func TestCompletePDMContextRemote(t *testing.T) {
	isolateCache(t)
	srv := pdmServer(t)
	defer srv.Close()
	writeConfig(t, `current_context: west
contexts:
  west: {profile: pdm1, remote: dc-west}
profiles:
  pdm1:
    provider: pdm
    server: `+srv.URL+`
    auth: {type: token, token_id: "root@pam!cli", secret_ref: "env:PDM_TEST_SECRET"}
`)
	t.Setenv("PDM_TEST_SECRET", "secret")
	if got, want := complete(t, "guest", "start", ""), []string{"200\tct1 @dc-west", "ct1\tvmid 200"}; !reflect.DeepEqual(got, want) {
		t.Errorf("context remote: got %q, want %q", got, want)
	}
	if got, want := complete(t, "guest", "start", "--remote", "dc-east", ""), []string{"100\tweb @dc-east", "web\tvmid 100"}; !reflect.DeepEqual(got, want) {
		t.Errorf("--remote override: got %q, want %q", got, want)
	}
}

// A secret lookup that blocks (a keyring stuck on D-Bus) is inside the budget,
// and the cache key never touches the secret.
func TestCompleteBlockedSecretWithinBudget(t *testing.T) {
	isolateCache(t)
	t.Setenv("PVE_CLI_COMPLETION_TIMEOUT", "200ms")
	release, done := make(chan struct{}), make(chan struct{})
	var keyCalls int32
	orig := completionResolve
	completionResolve = func(f *config.File, ov config.Overrides) (*config.Settings, error) {
		if ov.Secret == "-" {
			atomic.AddInt32(&keyCalls, 1)
			return orig(f, ov)
		}
		<-release // simulated stuck keyring
		close(done)
		return orig(f, ov)
	}
	t.Cleanup(func() {
		close(release)
		<-done
		completionResolve = orig
	})
	start := time.Now()
	got := complete(t, "--server", "https://127.0.0.1:1", "--token-id", "u@pam!c",
		"--token-secret-ref", "keyring://svc/key", "vm", "start", "")
	if el := time.Since(start); el > completionSlack(t) {
		t.Errorf("blocked secret lookup took %v; want ~timeout", el)
	}
	if len(got) != 0 {
		t.Errorf("want no suggestions, got %q", got)
	}
	if atomic.LoadInt32(&keyCalls) != 1 {
		t.Errorf("cache-key resolution must use the placeholder secret")
	}
}

func fingerprintOf(srv *httptest.Server) string {
	sum := sha256.Sum256(srv.Certificate().Raw)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var parts []string
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return "sha256:" + strings.Join(parts, ":")
}

// Completion never prints: not --debug lines, not the insecure warning, not a
// plaintext-secret or PDM inventory warning. It also never mutates process
// state (os.Stderr, the app's debug flag), and an insecure backend is never
// contacted (it would send credentials without the §4 warning).
func TestCompleteNeverPrints(t *testing.T) {
	isolateCache(t)
	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(`{"data":[{"type":"qemu","vmid":100,"name":"web-01","node":"pve-01"}]}`))
	})
	tlsSrv := httptest.NewTLSServer(mux)
	defer tlsSrv.Close()

	pdm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[
			{"remote":"dc-east","resources":[{"type":"pve-qemu","vmid":100,"name":"web","node":"n1"}]},
			{"remote":"dc-down","error":"connection refused","resources":[]}]}`))
	}))
	defer pdm.Close()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	creds := []string{"--token-id", "u@pam!c", "--token-secret-ref", "env:PC_TEST_SECRET"}
	t.Setenv("PC_TEST_SECRET", "secret")

	insecure, _ := completeDirective(t, append([]string{"--server", tlsSrv.URL, "--insecure", "--debug"}, append(creds, "vm", "start", "")...)...)
	pinned, _ := completeDirective(t, append([]string{"--server", tlsSrv.URL, "--tls-fingerprint", fingerprintOf(tlsSrv), "--debug"}, append(creds, "vm", "start", "")...)...)
	pdmGot, _ := completeDirective(t, append([]string{"--server", pdm.URL, "--provider", "pdm", "--debug"}, append(creds, "guest", "start", "")...)...)

	// Plaintext secret in config warns on stderr during normal runs.
	writeConfig(t, "current_context: c\ncontexts:\n  c: {profile: p}\nprofiles:\n  p:\n    server: "+tlsSrv.URL+
		"\n    tls: {fingerprint: \""+fingerprintOf(tlsSrv)+"\"}\n    auth: {type: token, token_id: \"u@pam!c\", secret: plain}\n")
	isolateCache(t)
	cfgGot, _ := completeDirective(t, "vm", "start", "")

	stillW := os.Stderr == w
	os.Stderr = orig
	w.Close()
	errOut, _ := io.ReadAll(r)

	if !stillW {
		t.Error("completion replaced os.Stderr")
	}
	var leaked []string
	for _, l := range strings.Split(strings.TrimSpace(string(errOut)), "\n") {
		if l != "" && !strings.HasPrefix(l, "Completion ended with directive:") { // cobra's own line
			leaked = append(leaked, l)
		}
	}
	if len(leaked) > 0 {
		t.Errorf("completion wrote to stderr: %q", leaked)
	}
	if len(insecure) != 0 {
		t.Errorf("insecure backend must yield no suggestions, got %q", insecure)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("TLS server requests = %d, want 2 (pinned + config; the insecure run must not connect)", n)
	}
	if len(pinned) == 0 || len(cfgGot) == 0 || len(pdmGot) == 0 {
		t.Errorf("expected suggestions: pinned=%q config=%q pdm=%q", pinned, cfgGot, pdmGot)
	}

	// The app's own state is untouched (no debug override, no cached provider).
	srv := completionServer(t, nil)
	a := &app{debug: true, server: srv.URL, tokenID: "u@pam!c", secret: "s"}
	if res := a.completionData(&cobra.Command{}, compGuests); len(res.items) == 0 {
		t.Error("direct completionData returned nothing")
	}
	if !a.debug || a.prov != nil {
		t.Errorf("completion mutated app state: debug=%v prov=%v", a.debug, a.prov)
	}
}

// Only trusted cache files are used: a world-readable file or a symlink is
// ignored (and replaced), and a loose cache dir is tightened to 0700.
func TestCompletePoisonedCacheIgnored(t *testing.T) {
	cacheDir := isolateCache(t)
	var hits int32
	srv := completionServer(t, &hits)
	s, err := config.Resolve(&config.File{}, config.Overrides{Server: srv.URL, TokenID: "u@pam!c", Secret: "-"})
	if err != nil {
		t.Fatal(err)
	}
	path := completionCachePath(s)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(dir, 0o777)
	poison := `{"guests":{"at":"` + time.Now().Format(time.RFC3339Nano) + `","items":[{"id":"666","name":"evil","kind":"qemu"}]}}`

	// 1) right content, wrong mode.
	if err := os.WriteFile(path, []byte(poison), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := complete(t, withCreds(srv, "vm", "start", "")...); len(got) == 0 || strings.Contains(strings.Join(got, " "), "evil") {
		t.Errorf("0644 cache file trusted: %q", got)
	}
	fi, _ := os.Stat(dir)
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("cache dir mode = %v, want tightened to 0700", fi.Mode().Perm())
	}

	// 2) a symlink to a 0600 file with poisoned content.
	target := filepath.Join(cacheDir, "elsewhere.json")
	if err := os.WriteFile(target, []byte(poison), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(path)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if got := complete(t, withCreds(srv, "vm", "start", "")...); strings.Contains(strings.Join(got, " "), "evil") {
		t.Errorf("symlinked cache file trusted: %q", got)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("resource requests = %d, want 2 (both poisoned caches ignored)", n)
	}
	if b, _ := os.ReadFile(target); string(b) != poison {
		t.Error("symlink target was written through")
	}
}
