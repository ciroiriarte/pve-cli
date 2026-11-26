//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ciroiriarte/pve-cli/internal/config"
)

// The tests below assert POSIX modes/ownership, which the non-unix cache
// checks (completion_cache_other.go) deliberately don't enforce.

func cachePathFor(t *testing.T, server string) string {
	t.Helper()
	s, err := config.Resolve(&config.File{}, config.Overrides{Server: server, TokenID: "u@pam!c", Secret: "-"})
	if err != nil {
		t.Fatal(err)
	}
	return completionCachePath(s)
}

// A config path that blocks on open (a FIFO with no writer) is inside the
// completion budget.
func TestCompleteFIFOConfigWithinBudget(t *testing.T) {
	isolateCache(t)
	t.Setenv("PVE_CLI_COMPLETION_TIMEOUT", "200ms")
	fifo := filepath.Join(t.TempDir(), "config.yaml")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	t.Setenv("PVE_CLI_CONFIG", fifo)
	t.Cleanup(func() { // unblock the abandoned reader
		if f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
	})
	start := time.Now()
	got := complete(t, "vm", "start", "")
	if el := time.Since(start); el > completionSlack(t) {
		t.Errorf("FIFO config: completion took %v; want ~timeout", el)
	}
	if len(got) != 0 {
		t.Errorf("want no suggestions, got %q", got)
	}
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

// A world-readable cache file is ignored, and the loose dir is tightened.
func TestCompletePoisonedCacheModeIgnored(t *testing.T) {
	isolateCache(t)
	var hits int32
	srv := completionServer(t, &hits)
	path := cachePathFor(t, srv.URL)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(dir, 0o777)
	if err := os.WriteFile(path, []byte(poisonCache()), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := complete(t, withCreds(srv, "vm", "start", "")...); len(got) == 0 || strings.Contains(strings.Join(got, " "), "evil") {
		t.Errorf("0644 cache file trusted: %q", got)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("cache dir mode = %v, want tightened to 0700", fi.Mode().Perm())
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("resource requests = %d, want 1", n)
	}
}

// Repairing an unsafe dir purges every cache file in it: while the dir was
// open, backend A's valid 0600 file could have been renamed over backend B's
// key. Refreshing A must not leave that file trusted for B.
func TestCompleteUnsafeDirRepairPurgesAllBackends(t *testing.T) {
	isolateCache(t)
	var hitsA, hitsB int32
	srvA := completionServer(t, &hitsA)
	srvB := completionServer(t, &hitsB)
	_ = complete(t, withCreds(srvA, "vm", "start", "")...)
	_ = complete(t, withCreds(srvB, "vm", "start", "")...)
	pathA, pathB := cachePathFor(t, srvA.URL), cachePathFor(t, srvB.URL)
	dir := filepath.Dir(pathA)
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(pathA, pathB); err != nil { // A's file now answers for B
		t.Fatal(err)
	}
	_ = complete(t, withCreds(srvA, "vm", "start", "")...) // refresh A: repairs the dir
	if _, err := os.Lstat(pathB); !os.IsNotExist(err) {
		t.Errorf("B's (renamed) cache file survived the repair: %v", err)
	}
	_ = complete(t, withCreds(srvB, "vm", "start", "")...)
	if a, b := atomic.LoadInt32(&hitsA), atomic.LoadInt32(&hitsB); a != 2 || b != 2 {
		t.Errorf("requests A=%d B=%d, want 2 and 2 (B refetched after the repair)", a, b)
	}
}
