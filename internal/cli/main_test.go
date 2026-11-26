package cli

import (
	"os"
	"testing"

	"github.com/ciroiriarte/pve-cli/internal/testenv"
)

// TestMain isolates the package from the developer's PVE_CLI_* env, real
// config file, OS keyring and user cache dir (on every OS; os.UserCacheDir
// ignores XDG_CACHE_HOME on macOS/Windows).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pve-cli-cache-")
	if err != nil {
		os.Exit(1)
	}
	cacheRootOverride.Store(dir)
	code := testenv.RunIsolated(m)
	os.RemoveAll(dir)
	os.Exit(code)
}
