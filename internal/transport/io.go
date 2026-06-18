package transport

import (
	"io"
	"os"
)

// stderr is where debug logging goes. Overridable in tests.
var stderr io.Writer = os.Stderr
