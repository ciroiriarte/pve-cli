package config

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// Normal runs must keep warning about a plaintext secret in config: to the
// Overrides.Warn writer when set, else to os.Stderr.
func TestResolvePlaintextSecretWarning(t *testing.T) {
	var buf bytes.Buffer
	if _, err := Resolve(baseFile(), Overrides{Warn: &buf}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "plaintext secret in config") {
		t.Errorf("Warn writer got %q", buf.String())
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	_, rerr := Resolve(baseFile(), Overrides{})
	os.Stderr = orig
	w.Close()
	out, _ := io.ReadAll(r)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(out), "plaintext secret in config") {
		t.Errorf("nil Warn must fall back to stderr, got %q", out)
	}
}
