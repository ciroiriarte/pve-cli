package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ciroiriarte/pve-cli/internal/config"
)

// #8: `pc auth login --user` writes a ticket-auth profile. A pinned fingerprint
// keeps login offline (no TOFU probe); --password avoids the interactive prompt.
func TestAuthLoginTicketWritesTicketProfile(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PVE_CLI_CONFIG", cfg)

	fp := "sha256:" + strings.Repeat("ab", 32)
	_, err := runCLI(t, "auth", "login", "https://pve.example:8006",
		"--user", "root@pam", "--password", "pw", "--fingerprint", fp, "--profile", "p1")
	if err != nil {
		t.Fatalf("auth login --user: %v", err)
	}

	f, err := config.Load(cfg)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	prof, ok := f.Profiles["p1"]
	if !ok {
		t.Fatal("profile p1 not written")
	}
	if prof.Auth.Type != "ticket" {
		t.Errorf("auth type = %q, want ticket", prof.Auth.Type)
	}
	if prof.Auth.User != "root@pam" {
		t.Errorf("user = %q, want root@pam", prof.Auth.User)
	}
	if prof.Auth.TokenID != "" {
		t.Errorf("ticket profile must not carry a token id, got %q", prof.Auth.TokenID)
	}
	if f.CurrentContext != "p1" {
		t.Errorf("current context = %q, want p1", f.CurrentContext)
	}
}

func TestAuthLoginTokenWritesTokenProfile(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PVE_CLI_CONFIG", cfg)

	fp := "sha256:" + strings.Repeat("ab", 32)
	_, err := runCLI(t, "auth", "login", "https://pve.example:8006",
		"--token-id", "svc@pve!cli", "--token-secret", "s", "--fingerprint", fp)
	if err != nil {
		t.Fatalf("auth login --token-id: %v", err)
	}
	f, _ := config.Load(cfg)
	prof := f.Profiles["default"]
	if prof.Auth.Type != "token" || prof.Auth.TokenID != "svc@pve!cli" {
		t.Errorf("expected token profile, got %+v", prof.Auth)
	}
}

func TestAuthLoginRequiresExactlyOneMethod(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PVE_CLI_CONFIG", cfg)

	_, err := runCLI(t, "auth", "login", "https://pve.example:8006")
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected 'exactly one' error with no method, got %v", err)
	}
}
