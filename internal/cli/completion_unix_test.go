//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

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
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("FIFO config: completion took %v; want ~timeout", el)
	}
	if len(got) != 0 {
		t.Errorf("want no suggestions, got %q", got)
	}
}
