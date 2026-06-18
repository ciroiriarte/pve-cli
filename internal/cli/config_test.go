package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/ciroiriarte/pve-cli/internal/config"
)

// withTempConfig points PVE_CLI_CONFIG at a temp file for the test.
func withTempConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PVE_CLI_CONFIG", path)
	return path
}

func TestConfigSetGetRoundTrip(t *testing.T) {
	withTempConfig(t)
	if _, err := runCLI(t, "config", "set", "current_context", "home"); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "config", "get", "current_context")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "home" {
		t.Errorf("get current_context = %q, want home", out)
	}
}

func TestAuthLoginWritesProfileAndKeyring(t *testing.T) {
	keyring.MockInit() // in-memory keyring
	path := withTempConfig(t)

	_, err := runCLI(t, "auth", "login", "https://pve1:8006",
		"--token-id", "svc@pve!cli", "--token-secret", "s3cr3t", "--profile", "lab",
		"--fingerprint", "sha256:AB")
	if err != nil {
		t.Fatalf("auth login: %v", err)
	}

	f, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.CurrentContext != "lab" {
		t.Errorf("current_context = %q, want lab", f.CurrentContext)
	}
	p, ok := f.Profiles["lab"]
	if !ok {
		t.Fatal("profile lab not written")
	}
	if p.Server != "https://pve1:8006" || p.Auth.TokenID != "svc@pve!cli" {
		t.Errorf("profile = %+v", p)
	}
	if p.Auth.Secret != "" {
		t.Errorf("secret should not be inline when keyring works, got %q", p.Auth.Secret)
	}
	if p.Auth.SecretRef != "keyring://pve-cli/lab" {
		t.Errorf("secret_ref = %q", p.Auth.SecretRef)
	}
	if got, _ := keyring.Get("pve-cli", "lab"); got != "s3cr3t" {
		t.Errorf("keyring secret = %q, want s3cr3t", got)
	}
}

func TestConfigViewRedactsInlineSecret(t *testing.T) {
	path := withTempConfig(t)
	if err := config.Save(path, &config.File{
		Profiles: map[string]config.Profile{
			"x": {Provider: "pve", Server: "https://h:8006",
				Auth: config.AuthConfig{Type: "token", TokenID: "a!b", Secret: "PLAINTEXT"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "config", "view")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "PLAINTEXT") {
		t.Errorf("config view leaked the secret:\n%s", out)
	}
	if !strings.Contains(out, "redacted") {
		t.Errorf("expected redaction marker:\n%s", out)
	}
}

func TestConfigInit(t *testing.T) {
	path := withTempConfig(t)
	if _, err := runCLI(t, "config", "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config init did not create %s", path)
	}
}
