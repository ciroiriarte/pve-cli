package cli

import (
	"bytes"
	"strings"
	"testing"
)

// helpText renders `pc <args...> --help` and returns the output.
func helpText(t *testing.T, args ...string) string {
	t.Helper()
	root := NewRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(append(args, "--help"))
	if err := root.Execute(); err != nil {
		t.Fatalf("help for %v: %v", args, err)
	}
	return buf.String()
}

// #16: the table-only global flags are shown on tabular commands and hidden on
// non-tabular ones; --format stays everywhere.
func TestTableFlagsScopedInHelp(t *testing.T) {
	list := helpText(t, "vm", "list")
	if !strings.Contains(list, "--column") || !strings.Contains(list, "--sort") {
		t.Errorf("vm list help should advertise --column/--sort:\n%s", list)
	}

	start := helpText(t, "vm", "start")
	if strings.Contains(start, "--column") || strings.Contains(start, "--sort") {
		t.Errorf("vm start help should hide table flags:\n%s", start)
	}
	if !strings.Contains(start, "--format") {
		t.Errorf("vm start help should still show --format:\n%s", start)
	}

	// Root keeps them discoverable.
	rootHelp := helpText(t)
	if !strings.Contains(rootHelp, "--column") {
		t.Errorf("root help should keep --column discoverable:\n%s", rootHelp)
	}
}

// The flags must stay functional (parseable) on non-tabular commands — hiding is
// only a help-display concern, restored after each render.
func TestTableFlagsRemainFunctionalAfterHelp(t *testing.T) {
	root := NewRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"vm", "start", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("help: %v", err)
	}
	f := root.PersistentFlags().Lookup("column")
	if f == nil {
		t.Fatal("--column flag missing")
	}
	if f.Hidden {
		t.Error("--column was left hidden after help; it must remain functional")
	}
}
