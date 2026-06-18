// Command pc is the pve-cli binary: a remote-first CLI for Proxmox VE.
package main

import (
	"os"

	"github.com/ciroiriarte/pve-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
