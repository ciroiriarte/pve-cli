package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/ciroiriarte/pve-cli/internal/auth"
	"github.com/ciroiriarte/pve-cli/internal/config"
)

// auth login refuses a cleartext non-loopback server before any I/O (no
// TOFU probe, no password/OTP prompt, no ticket POST) and saves nothing.
func TestAuthLoginRefusesCleartextServer(t *testing.T) {
	totpEnv(t)
	var prompted bool
	old := otpPrompter
	otpPrompter = func(string) func() (string, error) {
		prompted = true
		return func() (string, error) { return totpCode, nil }
	}
	t.Cleanup(func() { otpPrompter = old })

	// 192.0.2.0/24 is TEST-NET-1: any connection attempt would hang or fail
	// slowly, so a fast "refusing" error shows none was made.
	for _, extra := range [][]string{
		{"--user", totpUser, "--password-ref", "env:PC_TEST_TOTP_PW", "--otp", totpCode},
		{"--token-id", "svc@pve!cli", "--token-secret-ref", "env:PC_TEST_TOTP_PW"},
	} {
		start := time.Now()
		args := append([]string{"auth", "login", "http://192.0.2.10:8006", "--profile", "plain"}, extra...)
		_, err := runCLI(t, args...)
		if err == nil || !strings.Contains(err.Error(), "refusing to send credentials") {
			t.Fatalf("%v: want a cleartext refusal, got %v", extra, err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%v: refusal took %v — a connection was attempted", extra, d)
		}
	}
	if prompted {
		t.Error("the OTP prompt must not be reached")
	}
	f, _ := config.Load(config.DefaultPath())
	if _, ok := f.Profiles["plain"]; ok {
		t.Error("a refused login must not save the profile")
	}
}

// A recovery key on argv warns (it stays valid until used); a TOTP code
// (expires in seconds) does not.
func TestAuthLoginRecoveryKeyOnArgvWarns(t *testing.T) {
	totpEnv(t)
	noTTY(t)
	ts := newTOTPServer(t)
	var err error
	stderr := captureStderr(t, func() { _, err = runCLI(t, totpLogin(ts, "--otp", "recovery:abcd")...) })
	if err == nil {
		t.Fatal("the fake server rejects recovery keys; want an error")
	}
	if !strings.Contains(stderr, "plaintext --otp recovery key") {
		t.Errorf("want a recovery-key warning, stderr: %q", stderr)
	}
	stderr = captureStderr(t, func() { _, err = runCLI(t, totpLogin(ts, "--otp", totpCode)...) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "plaintext --otp") {
		t.Errorf("a TOTP code must not warn, stderr: %q", stderr)
	}
}

// A stale cached TFA ticket (server rejects it) is dropped; the command logs
// in again via PVE_CLI_OTP, replays the request once, and succeeds.
func TestRuntimeStaleCachedTicketRelogin(t *testing.T) {
	totpEnv(t)
	noTTY(t)
	ts := newTOTPServer(t)
	if _, err := runCLI(t, totpLogin(ts)...); err != nil { // offline profile save
		t.Fatal(err)
	}
	cache := auth.NewKeyringTicketCache(keyringService, "pve", ts.URL, totpUser)
	cache.Save(auth.CachedTicket{User: totpUser, Ticket: "PVE:stale", Issued: time.Now()})

	t.Setenv("PVE_CLI_OTP", totpCode)
	if _, err := runCLI(t, "raw", "version"); err != nil {
		t.Fatalf("raw version with a stale cached ticket: %v", err)
	}
	if got := ts.loginLog(); got != "password,challenge" {
		t.Errorf("logins = %q, want password,challenge", got)
	}
	if c, ok := cache.Load(); !ok || c.Ticket != totpTicket {
		t.Errorf("stale cache entry not replaced: %+v", c)
	}
}
