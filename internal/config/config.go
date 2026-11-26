// Package config models the pve-cli configuration file (profiles + contexts,
// kubeconfig-style) and resolves effective settings from the documented
// precedence: explicit flag > env var > context > profile defaults > built-in.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// File is the on-disk configuration.
type File struct {
	CurrentContext string             `yaml:"current_context,omitempty"`
	Contexts       map[string]Context `yaml:"contexts,omitempty"`
	Profiles       map[string]Profile `yaml:"profiles,omitempty"`
}

// Context binds a name to a profile and optional per-use overrides.
type Context struct {
	Profile string `yaml:"profile"`
	Remote  string `yaml:"remote,omitempty"` // PDM only
}

// Profile is a connection target.
type Profile struct {
	Provider  string         `yaml:"provider"` // "pve" | "pdm"
	Server    string         `yaml:"server"`
	Auth      AuthConfig     `yaml:"auth"`
	TLS       TLSConfig      `yaml:"tls,omitempty"`
	RateLimit RateLimit      `yaml:"rate_limit,omitempty"`
	Defaults  ProfileDefault `yaml:"defaults,omitempty"`
}

// RateLimit configures client-side request throttling.
type RateLimit struct {
	QPS   float64 `yaml:"qps,omitempty"`
	Burst int     `yaml:"burst,omitempty"`
}

// AuthConfig describes credentials for a profile.
type AuthConfig struct {
	Type      string `yaml:"type"` // "token" | "ticket"
	TokenID   string `yaml:"token_id,omitempty"`
	User      string `yaml:"user,omitempty"`
	SecretRef string `yaml:"secret_ref,omitempty"` // keyring://service/key or env:NAME
	// Secret is discouraged in plain text; load() warns if present.
	Secret string `yaml:"secret,omitempty"`
}

// TLSConfig mirrors transport.TLSConfig in serializable form.
type TLSConfig struct {
	CAFile      string `yaml:"ca_file,omitempty"`
	Fingerprint string `yaml:"fingerprint,omitempty"`
	Verify      *bool  `yaml:"verify,omitempty"`
}

// ProfileDefault holds per-profile defaults.
type ProfileDefault struct {
	Output string `yaml:"output,omitempty"`
}

// DefaultPath returns the config file path, honoring XDG_CONFIG_HOME.
func DefaultPath() string {
	if p := os.Getenv("PVE_CLI_CONFIG"); p != "" {
		return p
	}
	if p := managedPath(); p != "" {
		return p
	}
	return "pve-cli-config.yaml"
}

// managedPath is the default, app-managed config location
// ($XDG_CONFIG_HOME/pve-cli/config.yaml or ~/.config/pve-cli/config.yaml), or
// "" when no home directory is known.
func managedPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "pve-cli", "config.yaml")
}

// Load reads the config file at path. A missing file yields an empty File and
// no error, so the CLI works purely from env vars/flags.
func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &File{}, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var f File
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &f, nil
}

// Save writes the config file to path, creating parent directories.
func Save(path string, f *File) error {
	b, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	return writeConfigFile(path, b)
}

// writeConfigFile writes the credential-bearing config file (it may hold an
// inline plaintext secret when no keyring is available) with mode 0600.
//
// MkdirAll/WriteFile never change the mode of a path that already exists, so
// pre-existing permissive modes left by older versions are tightened here:
//   - the directory is chmod'ed 0700 only when it is the default app-managed
//     location; a PVE_CLI_CONFIG override may live in a user-managed directory
//     (dotfiles repo, $HOME) whose mode isn't ours to change.
//   - the file is written to a 0600 temp file in the same directory, fsynced,
//     and renamed over the symlink-resolved target. That keeps a symlinked
//     config a symlink, replaces a 0644 inode (cutting off readers holding an
//     fd on the old one), and is crash-atomic.
func writeConfigFile(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if mp := managedPath(); os.Getenv("PVE_CLI_CONFIG") == "" && mp != "" &&
		filepath.Clean(path) == filepath.Clean(mp) {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}

	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	} else if !os.IsNotExist(err) {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}
