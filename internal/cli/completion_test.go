package cli

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// completionServer serves /cluster/resources (2 VMs, 1 CT, an online and an
// offline node) and /storage, counting resource requests.
func completionServer(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		if r.URL.Query().Get("type") == "node" {
			w.Write([]byte(`{"data":[
				{"type":"node","node":"pve-01","status":"online"},
				{"type":"node","node":"pve-02","status":"offline"}]}`))
			return
		}
		w.Write([]byte(`{"data":[
			{"type":"qemu","vmid":101,"name":"db-01","node":"pve-01","status":"stopped"},
			{"type":"qemu","vmid":100,"name":"web-01","node":"pve-01","status":"running"},
			{"type":"lxc","vmid":200,"name":"dns","node":"pve-02","status":"running"},
			{"type":"node","node":"pve-01","status":"online"}]}`))
	})
	mux.HandleFunc("/api2/json/storage", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"storage":"local","type":"dir"},{"storage":"ceph-rbd","type":"rbd"}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// complete runs `pc __complete <args...>` and returns the suggestion lines
// (without cobra's trailing `:<directive>` line).
func complete(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := runCLI(t, append([]string{"__complete"}, args...)...)
	if err != nil {
		t.Fatalf("__complete %v: %v", args, err)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" && !strings.HasPrefix(l, ":") {
			got = append(got, l)
		}
	}
	return got
}

// isolateCache points the completion cache root at a fresh temp dir for this
// test (and restores the package default afterwards).
func isolateCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old, _ := cacheRootOverride.Load().(string)
	cacheRootOverride.Store(dir)
	t.Cleanup(func() { cacheRootOverride.Store(old) })
	return dir
}

func TestCompleteGuestsVMIDsAndNamesByKind(t *testing.T) {
	isolateCache(t)
	srv := completionServer(t, nil)

	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"vm", "start", ""}, []string{"100\tweb-01", "101\tdb-01", "db-01\tvmid 101", "web-01\tvmid 100"}},
		{[]string{"ct", "start", ""}, []string{"200\tdns", "dns\tvmid 200"}},
		{[]string{"guest", "show", ""}, []string{"100\tweb-01", "101\tdb-01", "200\tdns", "db-01\tvmid 101", "dns\tvmid 200", "web-01\tvmid 100"}},
		// prefix filtering, on ids and on names
		{[]string{"vm", "show", "we"}, []string{"web-01\tvmid 100"}},
		{[]string{"vm", "show", "10"}, []string{"100\tweb-01", "101\tdb-01"}},
		{[]string{"guest", "status", "2"}, []string{"200\tdns"}},
		// --node narrows the candidates
		{[]string{"guest", "start", "--node", "pve-02", ""}, []string{"200\tdns", "dns\tvmid 200"}},
		// a second positional (snapshot name) gets nothing
		{[]string{"vm", "snapshot", "rollback", "100", ""}, nil},
		// vm/ct create take a new id: no suggestions
		{[]string{"vm", "create", ""}, nil},
		// tag add: first arg is the guest
		{[]string{"ct", "tag", "add", ""}, []string{"200\tdns", "dns\tvmid 200"}},
		// comma-list --vmid completes the last element
		{[]string{"tag", "add", "--vmid", "100,20"}, []string{"100,200\tdns"}},
	}
	for _, c := range cases {
		got := complete(t, withCreds(srv, c.args...)...)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("__complete %v\n got %q\nwant %q", c.args, got, c.want)
		}
	}
}

func TestCompleteNodesAndStorages(t *testing.T) {
	isolateCache(t)
	srv := completionServer(t, nil)
	for _, args := range [][]string{
		{"node", "show", ""},
		{"vm", "list", "--node", ""},
		{"vm", "migrate", "100", "--target-node", ""},
	} {
		if got, want := complete(t, withCreds(srv, args...)...), []string{"pve-01"}; !reflect.DeepEqual(got, want) {
			t.Errorf("__complete %v = %q, want %q (offline pve-02 excluded)", args, got, want)
		}
	}
	got := complete(t, withCreds(srv, "storage", "status", "")...)
	if want := []string{"ceph-rbd\trbd", "local\tdir"}; !reflect.DeepEqual(got, want) {
		t.Errorf("storage completion = %q, want %q", got, want)
	}
}

func TestCompletePDMRemotes(t *testing.T) {
	isolateCache(t)
	srv := pdmServer(t)
	defer srv.Close()
	for _, args := range [][]string{
		{"remote", "show", ""},
		{"vm", "start", "--remote", ""},
	} {
		got := complete(t, withPDMCreds(srv, args...)...)
		if want := []string{"dc-east\tpve", "dc-west\tpve"}; !reflect.DeepEqual(got, want) {
			t.Errorf("__complete %v = %q, want %q", args, got, want)
		}
	}
	// --remote scopes guest suggestions; the remote shows in the description.
	got := complete(t, withPDMCreds(srv, "guest", "start", "--remote", "dc-west", "")...)
	if want := []string{"200\tct1 @dc-west", "ct1\tvmid 200"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pdm guest completion = %q, want %q", got, want)
	}
}

func TestCompleteSlowServerTimesOutEmpty(t *testing.T) {
	isolateCache(t)
	t.Setenv("PVE_CLI_COMPLETION_TIMEOUT", "300ms")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()
	start := time.Now()
	got := complete(t, withCreds(srv, "vm", "start", "")...)
	if el := time.Since(start); el > completionSlack(t) {
		t.Errorf("completion took %v against a slow server; want ~timeout", el)
	}
	if len(got) != 0 {
		t.Errorf("want no suggestions on timeout, got %q", got)
	}
}

func TestCompleteUnreachableServerEmpty(t *testing.T) {
	isolateCache(t)
	t.Setenv("PVE_CLI_COMPLETION_TIMEOUT", "300ms")

	// Refused connection.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := ln.Addr().String()
	ln.Close()

	// Accepts TCP but never speaks TLS: the handshake hangs until the timeout.
	black, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer black.Close()
	go func() {
		for {
			c, err := black.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()

	for _, server := range []string{"https://" + closedAddr, "https://" + black.Addr().String()} {
		start := time.Now()
		got := complete(t, "--server", server, "--token-id", "u@pam!c", "--token-secret", "s", "vm", "start", "")
		if el := time.Since(start); el > completionSlack(t) {
			t.Errorf("%s: completion took %v", server, el)
		}
		if len(got) != 0 {
			t.Errorf("%s: want no suggestions, got %q", server, got)
		}
	}
}

func TestCompleteNoCredentialsEmpty(t *testing.T) {
	isolateCache(t)
	if got := complete(t, "vm", "start", ""); len(got) != 0 {
		t.Errorf("want no suggestions without a configured backend, got %q", got)
	}
}

func TestCompleteCacheHit(t *testing.T) {
	cacheDir := isolateCache(t)
	var hits int32
	srv := completionServer(t, &hits)

	first := complete(t, withCreds(srv, "vm", "start", "")...)
	second := complete(t, withCreds(srv, "vm", "show", "w")...)
	_ = complete(t, withCreds(srv, "ct", "stop", "")...)
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("resource requests = %d, want 1 (later tabs served from cache)", n)
	}
	if len(first) == 0 || !reflect.DeepEqual(second, []string{"web-01\tvmid 100"}) {
		t.Fatalf("cached completion wrong: first=%q second=%q", first, second)
	}

	files, _ := filepath.Glob(filepath.Join(cacheDir, "pve-cli", "completion-*.json"))
	if len(files) != 1 {
		t.Fatalf("cache files = %v, want one", files)
	}
	fi, err := os.Stat(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 { // POSIX modes only
		t.Errorf("cache mode = %v, want 0600", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(files[0]); strings.Contains(string(b), "secret") {
		t.Error("cache must not contain the token secret")
	}

	// A different server/identity gets its own cache entry.
	other := completionServer(t, &hits)
	_ = complete(t, withCreds(other, "vm", "start", "")...)
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("resource requests = %d after switching server, want 2", n)
	}
}
