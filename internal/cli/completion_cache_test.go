package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ciroiriarte/pve-cli/internal/config"
)

// completionSlack is the most a bounded completion may take: the configured
// budget (PVE_CLI_COMPLETION_TIMEOUT) plus slack for CI scheduling and -race.
func completionSlack(t *testing.T) time.Duration {
	t.Helper()
	return completionTimeout() + 600*time.Millisecond
}

// A valid 0600 cache file is still ignored when its directory is unsafe (not
// 0700): in a shared writable dir another user could rename one of our own
// files over another key. Reading never chmods; the next write tightens.
func TestCompleteCacheUnsafeDirIgnored(t *testing.T) {
	isolateCache(t)
	var hits int32
	srv := completionServer(t, &hits)
	_ = complete(t, withCreds(srv, "vm", "start", "")...) // populate
	s, err := config.Resolve(&config.File{}, config.Overrides{Server: srv.URL, TokenID: "u@pam!c", Secret: "-"})
	if err != nil {
		t.Fatal(err)
	}
	path := completionCachePath(s)
	dir := filepath.Dir(path)
	if len(readCompCache(path)) == 0 {
		t.Fatal("cache not populated in a 0700 dir")
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if len(readCompCache(path)) != 0 {
		t.Error("cache read from a 0755 dir")
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o755 {
		t.Errorf("read changed dir mode to %v", fi.Mode().Perm())
	}
	_ = complete(t, withCreds(srv, "vm", "start", "")...)
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("resource requests = %d, want 2 (unsafe-dir cache ignored)", n)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("write did not tighten dir: %v", fi.Mode().Perm())
	}
}

// When the cache dir was unsafe, the next write keeps only the freshly fetched
// data set: entries for other keys (possibly planted or renamed in while the
// dir was open) must not become trusted just because the write tightened it.
func TestCompleteCacheUnsafeDirDiscardsOtherKeys(t *testing.T) {
	isolateCache(t)
	var hits int32
	srv := completionServer(t, &hits)
	_ = complete(t, withCreds(srv, "vm", "start", "")...)  // guests
	_ = complete(t, withCreds(srv, "node", "show", "")...) // nodes
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Fatalf("setup requests = %d, want 2", n)
	}
	s, err := config.Resolve(&config.File{}, config.Overrides{Server: srv.URL, TokenID: "u@pam!c", Secret: "-"})
	if err != nil {
		t.Fatal(err)
	}
	path := completionCachePath(s)
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = complete(t, withCreds(srv, "vm", "start", "")...) // unsafe: refetch guests, rewrite
	m := readCompCache(path)
	if _, ok := m[compNodes]; ok {
		t.Errorf("nodes entry survived the unsafe-dir rewrite: %v", m)
	}
	if _, ok := m[compGuests]; !ok {
		t.Errorf("fresh guests entry missing: %v", m)
	}
	_ = complete(t, withCreds(srv, "node", "show", "")...)
	if n := atomic.LoadInt32(&hits); n != 4 {
		t.Errorf("requests = %d, want 4 (nodes refetched, not served from the laundered cache)", n)
	}
}

// --vmid takes numeric ids, so its completion offers ids only (also inside a
// comma list); positional guest args still offer names.
func TestCompleteVMIDFlagIDsOnly(t *testing.T) {
	isolateCache(t)
	srv := completionServer(t, nil)
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"firewall", "rules", "--vmid", ""}, []string{"100\tweb-01", "101\tdb-01", "200\tdns"}},
		{[]string{"tag", "add", "--vmid", "100,"}, []string{"100,100\tweb-01", "100,101\tdb-01", "100,200\tdns"}},
		{[]string{"tag", "add", "--vmid", "we"}, nil},
		{[]string{"snapshot", "list", "--vmid", "d"}, nil},
		{[]string{"vm", "start", "we"}, []string{"web-01\tvmid 100"}},
	}
	for _, c := range cases {
		if got := complete(t, withCreds(srv, c.args...)...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("__complete %v\n got %q\nwant %q", c.args, got, c.want)
		}
	}
}
