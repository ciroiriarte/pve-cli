package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ciroiriarte/pve-cli/internal/config"
	"github.com/zalando/go-keyring"
)

// captureStderr runs fn with os.Stderr redirected and returns what it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	b, _ := io.ReadAll(r)
	return string(b)
}

// withStdin feeds s to os.Stdin for the duration of the test.
func withStdin(t *testing.T, s string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(s); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; r.Close() })
}

const loginFP = "sha256:AB"

func loginKeyringSecret(t *testing.T, profile string) string {
	t.Helper()
	v, err := keyring.Get(keyringService, profile)
	if err != nil {
		t.Fatalf("keyring get: %v", err)
	}
	return v
}

// #39: `auth login --token-secret-ref` resolves the secret off-argv, silently.
func TestAuthLoginTokenSecretRef(t *testing.T) {
	keyring.MockInit()
	t.Setenv("PVE_CLI_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("PC_TEST_TOKEN", "from-env")

	var err error
	stderr := captureStderr(t, func() {
		_, err = runCLI(t, "auth", "login", "https://pve1:8006",
			"--token-id", "svc@pve!cli", "--token-secret-ref", "env:PC_TEST_TOKEN",
			"--profile", "ref", "--fingerprint", loginFP)
	})
	if err != nil {
		t.Fatalf("auth login --token-secret-ref: %v", err)
	}
	if got := loginKeyringSecret(t, "ref"); got != "from-env" {
		t.Errorf("stored secret = %q, want from-env", got)
	}
	if strings.Contains(stderr, "plaintext") {
		t.Errorf("ref form must not warn, stderr: %q", stderr)
	}
}

// #39: `auth login --password -` reads the password from stdin.
func TestAuthLoginPasswordStdin(t *testing.T) {
	keyring.MockInit()
	t.Setenv("PVE_CLI_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	withStdin(t, "pw-from-stdin\n")

	if _, err := runCLI(t, "auth", "login", "https://pve1:8006",
		"--user", "root@pam", "--password", "-", "--profile", "stdin",
		"--fingerprint", loginFP); err != nil {
		t.Fatalf("auth login --password -: %v", err)
	}
	if got := loginKeyringSecret(t, "stdin"); got != "pw-from-stdin" {
		t.Errorf("stored password = %q, want pw-from-stdin", got)
	}
}

// #39: --password-ref works for ticket auth, and the value/ref pair is
// mutually exclusive.
func TestAuthLoginPasswordRefAndExclusive(t *testing.T) {
	keyring.MockInit()
	t.Setenv("PVE_CLI_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("PC_TEST_PW", "pw-env")

	if _, err := runCLI(t, "auth", "login", "https://pve1:8006",
		"--user", "root@pam", "--password-ref", "env:PC_TEST_PW", "--profile", "pref",
		"--fingerprint", loginFP); err != nil {
		t.Fatalf("auth login --password-ref: %v", err)
	}
	if got := loginKeyringSecret(t, "pref"); got != "pw-env" {
		t.Errorf("stored password = %q, want pw-env", got)
	}
	_, err := runCLI(t, "auth", "login", "https://pve1:8006",
		"--user", "root@pam", "--password", "x", "--password-ref", "env:PC_TEST_PW",
		"--fingerprint", loginFP)
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}

// #39: a plaintext argv secret still works but warns on stderr.
func TestAuthLoginPlaintextSecretWarns(t *testing.T) {
	keyring.MockInit()
	t.Setenv("PVE_CLI_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))

	var err error
	stderr := captureStderr(t, func() {
		_, err = runCLI(t, "auth", "login", "https://pve1:8006",
			"--token-id", "svc@pve!cli", "--token-secret", "plain",
			"--profile", "plain", "--fingerprint", loginFP)
	})
	if err != nil {
		t.Fatalf("auth login --token-secret: %v", err)
	}
	if got := loginKeyringSecret(t, "plain"); got != "plain" {
		t.Errorf("stored secret = %q, want plain", got)
	}
	if !strings.Contains(stderr, "warning: plaintext --token-secret") {
		t.Errorf("expected plaintext warning on stderr, got %q", stderr)
	}
}

// #39: the global --token-secret-ref authenticates without a plaintext secret
// on argv; the plaintext global --token-secret still works but warns.
func TestGlobalTokenSecretRef(t *testing.T) {
	srv := fakeServer(t)
	defer srv.Close()
	t.Setenv("PVE_CLI_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("PC_TEST_TOKEN", "secret")

	var err error
	stderr := captureStderr(t, func() {
		_, err = runCLI(t, "--server", srv.URL, "--token-id", "u@pam!c",
			"--token-secret-ref", "env:PC_TEST_TOKEN", "raw", "version")
	})
	if err != nil {
		t.Fatalf("raw version with --token-secret-ref: %v", err)
	}
	if strings.Contains(stderr, "plaintext") {
		t.Errorf("ref form must not warn, stderr: %q", stderr)
	}

	stderr = captureStderr(t, func() {
		_, err = runCLI(t, withCreds(srv, "raw", "version")...)
	})
	if err != nil {
		t.Fatalf("raw version with --token-secret: %v", err)
	}
	if !strings.Contains(stderr, "warning: plaintext --token-secret") {
		t.Errorf("expected plaintext warning on stderr, got %q", stderr)
	}

	_, err = runCLI(t, "--server", srv.URL, "--token-id", "u@pam!c", "--token-secret", "x",
		"--token-secret-ref", "env:PC_TEST_TOKEN", "raw", "version")
	if err == nil || !strings.Contains(err.Error(), "token-secret") {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}

// #39: `--token-secret -` with blank stdin must not save an empty credential.
func TestAuthLoginRejectsEmptyStdinSecret(t *testing.T) {
	keyring.MockInit()
	t.Setenv("PVE_CLI_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	withStdin(t, "\n")

	_, err := runCLI(t, "auth", "login", "https://pve1:8006",
		"--token-id", "svc@pve!cli", "--token-secret", "-", "--profile", "empty",
		"--fingerprint", loginFP)
	if err == nil || !strings.Contains(err.Error(), "empty credential") {
		t.Fatalf("expected empty-credential error, got %v", err)
	}
	if _, kerr := keyring.Get(keyringService, "empty"); kerr == nil {
		t.Error("an empty credential was stored in the keyring")
	}
}

// `printf ” | pc auth login … --token-secret -` (stdin EOF, no data) reports
// the empty credential instead of a bare "EOF".
func TestAuthLoginEOFStdinIsEmptyCredential(t *testing.T) {
	t.Setenv("PVE_CLI_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	for _, args := range [][]string{
		{"--token-id", "svc@pve!cli", "--token-secret", "-"},
		{"--user", "root@pam"}, // omitted + non-TTY stdin → reads stdin
	} {
		withStdin(t, "")
		_, err := runCLI(t, append([]string{"auth", "login", "https://pve1:8006",
			"--profile", "eof", "--fingerprint", loginFP}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "empty credential") {
			t.Fatalf("%v: expected empty-credential error, got %v", args, err)
		}
	}
}

// #39: the global plaintext --token-secret warning fires on provider-free paths
// too (test-auth --offline never builds a provider).
func TestGlobalPlaintextSecretWarnsWithoutProvider(t *testing.T) {
	t.Setenv("PVE_CLI_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	var err error
	stderr := captureStderr(t, func() {
		_, err = runCLI(t, "--server", "https://pve1:8006", "--token-id", "u@pam!c",
			"--token-secret", "x", "config", "test-auth", "--offline")
	})
	if err != nil {
		t.Fatalf("test-auth --offline: %v", err)
	}
	if strings.Count(stderr, "warning: plaintext --token-secret") != 1 {
		t.Errorf("expected exactly one plaintext warning, got %q", stderr)
	}
}

// #34: test-auth reports the source that actually won (env over a profile
// keyring ref), not just what the profile declares.
func TestConfigTestAuthReportsWinningSecretSource(t *testing.T) {
	keyring.MockInit()
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PVE_CLI_CONFIG", cfg)
	if err := config.Save(cfg, &config.File{
		CurrentContext: "lab",
		Contexts:       map[string]config.Context{"lab": {Profile: "lab"}},
		Profiles: map[string]config.Profile{"lab": {
			Server: "https://pve1:8006",
			Auth:   config.AuthConfig{Type: "token", TokenID: "u@pam!c", SecretRef: "keyring://pve-cli/lab"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PVE_CLI_TOKEN_SECRET", "envsecret")
	out, err := runCLI(t, "config", "test-auth", "--offline")
	if err != nil {
		t.Fatalf("test-auth --offline: %v", err)
	}
	if !strings.Contains(out, "secret:     env PVE_CLI_TOKEN_SECRET") {
		t.Errorf("expected env as the secret source, got:\n%s", out)
	}
}
