package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
