package transport

import (
	"io"
	"os"
)

// stderr is where debug logging goes. Overridable in tests.
var stderr io.Writer = os.Stderr

// SetStderr redirects the package's diagnostic output (debug log, insecure-TLS
// warning) and returns a func that restores the previous writer. Shell
// completion uses it so nothing leaks into the terminal mid-completion.
func SetStderr(w io.Writer) (restore func()) {
	old := stderr
	stderr = w
	return func() { stderr = old }
}
