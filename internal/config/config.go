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
	Provider string         `yaml:"provider"` // "pve" | "pdm"
	Server   string         `yaml:"server"`
	Auth     AuthConfig     `yaml:"auth"`
	TLS      TLSConfig      `yaml:"tls,omitempty"`
	Defaults ProfileDefault `yaml:"defaults,omitempty"`
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
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "pve-cli-config.yaml"
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
