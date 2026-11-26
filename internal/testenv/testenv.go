// Package testenv holds helpers shared by tests across packages.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// RunIsolated is a package TestMain body: it clears every PVE_CLI_* variable
// (a developer's exported live-check credentials, which AGENTS.md recommends,
// must not leak into tests), points PVE_CLI_CONFIG at a file in a fresh temp
// dir, and switches the keyring to the in-memory mock, so no test in the
// package can read the real config/env or touch the real OS keyring. Tests
// that need other values set them with t.Setenv. Returns m.Run's exit code.
func RunIsolated(m *testing.M) int {
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "PVE_CLI_") {
			os.Unsetenv(k)
		}
	}
	dir, err := os.MkdirTemp("", "pve-cli-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testenv:", err)
		return 1
	}
	defer os.RemoveAll(dir)
	os.Setenv("PVE_CLI_CONFIG", filepath.Join(dir, "config.yaml"))
	keyring.MockInit()
	return m.Run()
}
