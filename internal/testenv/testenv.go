// Package testenv holds helpers shared by tests across packages.
package testenv

import (
	"os"
	"strings"
	"testing"
)

// ClearPVEEnv unsets every PVE_CLI_* variable for the duration of the test
// (restored on cleanup), so a developer's exported live-check credentials
// (AGENTS.md recommends PVE_CLI_TOKEN_SECRET etc.) can't leak into a test.
func ClearPVEEnv(t testing.TB) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "PVE_CLI_") {
			t.Setenv(k, "") // registers restoration of the original value
			os.Unsetenv(k)
		}
	}
}
