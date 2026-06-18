package cli

import (
	"os"

	"github.com/ciroiriarte/pve-cli/internal/output"
)

// render writes a Tabular to stdout using the app's resolved output options.
func (a *app) render(t output.Tabular) error {
	opts, err := a.outputOptions()
	if err != nil {
		return err
	}
	return output.Render(os.Stdout, t, opts)
}

// isTTY reports whether stdout is an interactive terminal.
func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
