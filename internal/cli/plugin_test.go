package cli

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writePlugin(t *testing.T, dir, name, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("plugin exec test uses a shell script")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestPluginDispatchRunsAndPropagates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PVE_CLI_PLUGIN_DIR", dir)
	writePlugin(t, dir, "pc-hello", "#!/bin/sh\necho \"hello $1\"\nexit 7\n")

	// Capture stdout (execPlugin inherits os.Stdout).
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	root := NewRootCmd()
	handled, code := dispatchPlugin(root, []string{"hello", "world"})

	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	if !handled {
		t.Fatal("expected plugin to be dispatched")
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7 (propagated from plugin)", code)
	}
	if !strings.Contains(string(out), "hello world") {
		t.Errorf("plugin stdout not propagated: %q", out)
	}
}

func TestPluginListFindsPlugin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PVE_CLI_PLUGIN_DIR", dir)
	writePlugin(t, dir, "pc-backupall", "#!/bin/sh\ntrue\n")

	out, err := runCLI(t, "plugin", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "backupall") {
		t.Errorf("plugin list missing discovered plugin:\n%s", out)
	}
}

func TestBuiltinTakesPrecedenceOverPlugin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PVE_CLI_PLUGIN_DIR", dir)
	// A plugin shadowing a built-in must never be dispatched.
	writePlugin(t, dir, "pc-version", "#!/bin/sh\necho SHADOW\n")

	root := NewRootCmd()
	handled, _ := dispatchPlugin(root, []string{"version"})
	if handled {
		t.Error("built-in 'version' must take precedence over a pc-version plugin")
	}
}

func TestUnknownNonPluginNotHandled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PVE_CLI_PLUGIN_DIR", dir)
	root := NewRootCmd()
	handled, _ := dispatchPlugin(root, []string{"definitely-not-a-thing"})
	if handled {
		t.Error("no plugin exists; dispatch should not handle it")
	}
}
