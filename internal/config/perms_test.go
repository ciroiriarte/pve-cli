package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// APP-02: the config directory holds the credential file, so Save must create
// it 0700 (not world-traversable) and the file 0600.
func TestSaveCreatesDir0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub", "pve-cli")
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, &File{CurrentContext: "x"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("config dir perm = %o, want 700", perm)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file perm = %o, want 600", perm)
	}
}

// #33: MkdirAll/WriteFile never change the mode of an existing path, so an
// install upgraded from an older version kept a 0755 dir and 0644 file. Both
// Save and SetValue must tighten pre-existing permissive modes.
func TestWriteTightensPreexistingPerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	for name, write := range map[string]func(string) error{
		"Save":     func(p string) error { return Save(p, &File{CurrentContext: "x"}) },
		"SetValue": func(p string) error { return SetValue(p, "current_context", "x") },
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "pve-cli")
			path := filepath.Join(dir, "config.yaml")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := write(path); err != nil {
				t.Fatalf("write: %v", err)
			}
			di, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if perm := di.Mode().Perm(); perm != 0o700 {
				t.Errorf("config dir perm = %o, want 700", perm)
			}
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if perm := fi.Mode().Perm(); perm != 0o600 {
				t.Errorf("config file perm = %o, want 600", perm)
			}
		})
	}
}

// #33: only the app's own "pve-cli" dir is chmod'ed — a PVE_CLI_CONFIG pointing
// into some other directory must not have that parent's mode changed.
func TestWriteLeavesForeignParentDirMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pc.yaml")
	if err := Save(path, &File{CurrentContext: "x"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o755 {
		t.Errorf("foreign parent dir perm = %o, want 755 (untouched)", perm)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file perm = %o, want 600", perm)
	}
}
