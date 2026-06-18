// Package version holds build metadata and the supported Proxmox version matrix.
package version

import "fmt"

// Build info, injected via -ldflags at build time (see Makefile).
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Supported upstream version matrix, surfaced by `pc version`.
const (
	SupportedPVE = "PVE 8.x+ (best-effort newer minors)"
	SupportedPDM = "PDM 0.x+ (experimental)"
)

// Disclaimer is shown in version/help output. pve-cli is unofficial.
const Disclaimer = "pve-cli is an unofficial, community tool and is not affiliated with or endorsed by Proxmox Server Solutions GmbH."

// String returns a human-readable one-line version string.
func String() string {
	return fmt.Sprintf("pc %s (commit %s, built %s)", Version, Commit, Date)
}
