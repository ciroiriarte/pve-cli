package config

import (
	"os"
	"testing"

	"github.com/ciroiriarte/pve-cli/internal/testenv"
)

// TestMain isolates the package from the developer's PVE_CLI_* env, real
// config file and OS keyring.
func TestMain(m *testing.M) { os.Exit(testenv.RunIsolated(m)) }
