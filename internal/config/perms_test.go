package config

import (
	"os"
	"path/filepath"
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
