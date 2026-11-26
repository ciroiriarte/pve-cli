package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ciroiriarte/pve-cli/internal/testenv"
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

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
}

// mkPermissive creates dir (0755) holding path (0644), as an older version left them.
func mkPermissive(t *testing.T, dir, path string) {
	t.Helper()
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
}

func wantPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != want {
		t.Errorf("%s perm = %o, want %o", path, perm, want)
	}
}

// #33: MkdirAll/WriteFile never change the mode of an existing path, so an
// install upgraded from an older version kept a 0755 dir and 0644 file. At the
// default managed location, Save and SetValue must tighten both.
func TestWriteTightensPreexistingPerms(t *testing.T) {
	skipOnWindows(t)
	for name, write := range map[string]func(string) error{
		"Save":     func(p string) error { return Save(p, &File{CurrentContext: "x"}) },
		"SetValue": func(p string) error { return SetValue(p, "current_context", "x") },
	} {
		t.Run(name, func(t *testing.T) {
			testenv.ClearPVEEnv(t)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			path := DefaultPath()
			dir := filepath.Dir(path)
			mkPermissive(t, dir, path)
			if err := write(path); err != nil {
				t.Fatalf("write: %v", err)
			}
			wantPerm(t, dir, 0o700)
			wantPerm(t, path, 0o600)
		})
	}
}

// #33: with a PVE_CLI_CONFIG override the directory may be user-managed (e.g.
// a dotfiles repo that happens to be named pve-cli): only the file is tightened.
func TestWriteOverrideLeavesDirMode(t *testing.T) {
	skipOnWindows(t)
	testenv.ClearPVEEnv(t)
	for name, dir := range map[string]string{
		"named pve-cli": filepath.Join(t.TempDir(), "dotfiles", "pve-cli"),
		"foreign":       t.TempDir(),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, "config.yaml")
			t.Setenv("PVE_CLI_CONFIG", path)
			mkPermissive(t, dir, path)
			if err := Save(path, &File{CurrentContext: "x"}); err != nil {
				t.Fatalf("Save: %v", err)
			}
			wantPerm(t, dir, 0o755)
			wantPerm(t, path, 0o600)
		})
	}
}

// #33: a symlinked config (dotfile managers) stays a symlink; the write lands
// on its target, which ends up 0600 even if it was 0644.
func TestWriteKeepsSymlinkedConfig(t *testing.T) {
	skipOnWindows(t)
	testenv.ClearPVEEnv(t)
	root := t.TempDir()
	realDir := filepath.Join(root, "dotfiles")
	target := filepath.Join(realDir, "pc.yaml")
	mkPermissive(t, realDir, target)
	link := filepath.Join(root, "pve-cli", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PVE_CLI_CONFIG", link)

	if err := Save(link, &File{CurrentContext: "via-link"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	li, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if li.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config symlink was replaced by a regular file (mode %v)", li.Mode())
	}
	wantPerm(t, target, 0o600)
	f, err := Load(target)
	if err != nil {
		t.Fatal(err)
	}
	if f.CurrentContext != "via-link" {
		t.Errorf("target not updated: current_context = %q", f.CurrentContext)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(realDir)
	if len(entries) != 1 {
		t.Errorf("expected only the config in %s, got %d entries", realDir, len(entries))
	}
}
