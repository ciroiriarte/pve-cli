//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// Repairing an unsafe dir is race-free: concurrent readers never see a file
// that was in the dir while it was open, and every writer ends up with a
// usable 0700 dir. (Tightening in place and purging afterwards would let a
// reader find the dir "safe" before the purge.)
func TestCompleteUnsafeDirRepairConcurrent(t *testing.T) {
	for round := 0; round < 20; round++ {
		root := isolateCache(t)
		dir := filepath.Join(root, "pve-cli")
		if err := os.MkdirAll(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(dir, 0o777)
		planted := filepath.Join(dir, "completion-planted.json")
		if err := os.WriteFile(planted, []byte(poisonCache()), 0o600); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var leaked int32
		for i := 0; i < 8; i++ {
			wg.Add(2)
			go func(i int) {
				defer wg.Done()
				writeCompCache(filepath.Join(dir, "completion-w"+strconv.Itoa(i)+".json"), compNodes, []compEntry{{ID: "n"}})
			}(i)
			go func() {
				defer wg.Done()
				if _, ok := readCompCache(planted)[compGuests]; ok {
					atomic.AddInt32(&leaked, 1)
				}
			}()
		}
		wg.Wait()
		if n := atomic.LoadInt32(&leaked); n > 0 {
			t.Fatalf("round %d: %d readers saw the planted file during repair", round, n)
		}
		if !cacheDirSafe(dir) {
			t.Fatalf("round %d: dir not safe after repair", round)
		}
		if _, err := os.Lstat(planted); !os.IsNotExist(err) {
			t.Fatalf("round %d: planted file survived: %v", round, err)
		}
	}
}
