package cli

import (
	"encoding/json"
	"io"
	"os"

	"github.com/ciroiriarte/pve-cli/internal/output"
)

// printJSON writes v as indented JSON followed by a newline.
func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

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

// isInputTTY reports whether STDIN is an interactive terminal. Prompts must gate
// on this (not isTTY, which is stdout) so `pc … > file` — stdout redirected,
// stdin still a terminal — still prompts, and a piped/closed stdin never makes
// the tool block waiting for input it can't get.
func isInputTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
