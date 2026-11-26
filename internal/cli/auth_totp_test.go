package cli

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/ciroiriarte/pve-cli/internal/auth"
	"github.com/ciroiriarte/pve-cli/internal/config"
)

const (
	totpUser   = "ops@pve"
	totpPW     = "pw-Zq81"
	totpCode   = "246810"
	totpTicket = "PVE:ops@pve:6560A2B1::full"
)

// totpServer emulates PVE's TFA login and a /version endpoint that only
// accepts the full (TFA-confirmed) ticket.
type totpServer struct {
	*httptest.Server
	mu     sync.Mutex
	logins []string // "password" | "challenge" | "renew"
}

func newTOTPServer(t *testing.T) *totpServer {
	t.Helper()
	ts := &totpServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/access/ticket", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f := r.PostForm
		ts.mu.Lock()
		defer ts.mu.Unlock()
		full := `{"data":{"username":"ops@pve","ticket":"` + totpTicket + `","CSRFPreventionToken":"c"}}`
		switch {
		case f.Get("username") != totpUser:
			http.Error(w, "authentication failure", http.StatusUnauthorized)
		case f.Get("tfa-challenge") != "":
			ts.logins = append(ts.logins, "challenge")
			if f.Get("password") != "totp:"+totpCode {
				http.Error(w, "authentication failure", http.StatusUnauthorized)
				return
			}
			w.Write([]byte(full))
		case f.Get("password") == totpTicket:
			ts.logins = append(ts.logins, "renew")
			w.Write([]byte(full))
		case f.Get("password") == totpPW:
			ts.logins = append(ts.logins, "password")
			w.Write([]byte(`{"data":{"username":"ops@pve","ticket":"PVE:!tfa!x:6560A2B1::s","NeedTFA":1}}`))
		default:
			http.Error(w, "authentication failure", http.StatusUnauthorized)
		}
	})
	mux.HandleFunc("/api2/json/version", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("PVEAuthCookie"); err != nil || c.Value != totpTicket {
			http.Error(w, "authentication failure", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":{"version":"9.1.6","release":"9.1","repoid":"abc"}}`))
	})
	ts.Server = httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func (ts *totpServer) loginLog() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return strings.Join(ts.logins, ",")
}

// noTTY makes the two-factor prompt unavailable (non-interactive run).
func noTTY(t *testing.T) {
	t.Helper()
	old := otpPrompter
	otpPrompter = func(string) func() (string, error) { return nil }
	t.Cleanup(func() { otpPrompter = old })
}

func totpEnv(t *testing.T) {
	t.Helper()
	keyring.MockInit()
	t.Setenv("PVE_CLI_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("PVE_CLI_OTP", "")
	t.Setenv("PC_TEST_TOTP_PW", totpPW)
}

func totpLogin(ts *totpServer, extra ...string) []string {
	return append([]string{"auth", "login", ts.URL, "--user", totpUser,
		"--password-ref", "env:PC_TEST_TOTP_PW", "--profile", "tfa"}, extra...)
}

// --otp completes the challenge at login, caches the TFA ticket, and later
// commands reuse it without a code or a new login.
func TestAuthLoginOTPFlagCachesTicket(t *testing.T) {
	totpEnv(t)
	noTTY(t)
	ts := newTOTPServer(t)

	var err error
	stderr := captureStderr(t, func() { _, err = runCLI(t, totpLogin(ts, "--otp", totpCode)...) })
	if err != nil {
		t.Fatalf("auth login --otp: %v", err)
	}
	if got := ts.loginLog(); got != "password,challenge" {
		t.Errorf("logins = %q, want password,challenge", got)
	}
	if !strings.Contains(stderr, "two-factor login OK") {
		t.Errorf("stderr should confirm the TFA login: %q", stderr)
	}
	if strings.Contains(stderr, totpCode) || strings.Contains(stderr, totpPW) {
		t.Errorf("stderr leaks the code or password: %q", stderr)
	}
	c, ok := auth.NewKeyringTicketCache(keyringService, "pve", ts.URL, totpUser).Load()
	if !ok || c.Ticket != totpTicket {
		t.Fatalf("TFA ticket not cached in the keyring: %+v", c)
	}

	out, err := runCLI(t, "raw", "version")
	if err != nil {
		t.Fatalf("raw version with the cached ticket: %v", err)
	}
	if !strings.Contains(out, "9.1.6") {
		t.Errorf("unexpected output: %q", out)
	}
	if got := ts.loginLog(); got != "password,challenge" {
		t.Errorf("a fresh cached ticket should be reused without logging in; logins = %q", got)
	}

	// test-auth reports the cached two-factor ticket.
	out, err = runCLI(t, "config", "test-auth")
	if err != nil {
		t.Fatalf("test-auth: %v", err)
	}
	if !strings.Contains(out, "tfa:        cached two-factor ticket") {
		t.Errorf("test-auth should report the cached TFA ticket:\n%s", out)
	}
}

func TestAuthLoginOTPFromEnv(t *testing.T) {
	totpEnv(t)
	noTTY(t)
	t.Setenv("PVE_CLI_OTP", totpCode)
	ts := newTOTPServer(t)
	if _, err := runCLI(t, totpLogin(ts)...); err != nil {
		t.Fatalf("auth login with PVE_CLI_OTP: %v", err)
	}
	if got := ts.loginLog(); got != "password,challenge" {
		t.Errorf("logins = %q, want password,challenge", got)
	}
}

func TestAuthLoginOTPRef(t *testing.T) {
	totpEnv(t)
	noTTY(t)
	t.Setenv("PC_TEST_CODE", totpCode)
	ts := newTOTPServer(t)
	if _, err := runCLI(t, totpLogin(ts, "--otp-ref", "env:PC_TEST_CODE")...); err != nil {
		t.Fatalf("auth login --otp-ref: %v", err)
	}
	if got := ts.loginLog(); got != "password,challenge" {
		t.Errorf("logins = %q, want password,challenge", got)
	}
}

// A rejected code fails the login with the auth exit code and saves nothing.
func TestAuthLoginWrongOTP(t *testing.T) {
	totpEnv(t)
	noTTY(t)
	ts := newTOTPServer(t)
	var err error
	stderr := captureStderr(t, func() { _, err = runCLI(t, totpLogin(ts, "--otp", "999999")...) })
	if err == nil || !strings.Contains(err.Error(), "two-factor authentication failed") {
		t.Fatalf("want a rejected-code error, got %v", err)
	}
	if code := ExitCodeFor(err); code != ExitAuthConfig {
		t.Errorf("exit code = %d, want %d", code, ExitAuthConfig)
	}
	for _, s := range []string{"999999", totpPW} {
		if strings.Contains(err.Error(), s) || strings.Contains(stderr, s) {
			t.Errorf("output leaks %q: err=%v stderr=%q", s, err, stderr)
		}
	}
	f, _ := config.Load(config.DefaultPath())
	if _, ok := f.Profiles["tfa"]; ok {
		t.Error("a failed login must not save the profile")
	}
}

// Interactive login: the code is prompted when the server asks for it.
func TestAuthLoginOTPPrompt(t *testing.T) {
	totpEnv(t)
	ts := newTOTPServer(t)
	old := otpPrompter
	var label string
	otpPrompter = func(user string) func() (string, error) {
		label = user
		return func() (string, error) { return totpCode, nil }
	}
	t.Cleanup(func() { otpPrompter = old })
	if _, err := runCLI(t, totpLogin(ts)...); err != nil {
		t.Fatalf("auth login with a prompted code: %v", err)
	}
	if label != totpUser {
		t.Errorf("prompt for %q, want %q", label, totpUser)
	}
	if got := ts.loginLog(); got != "password,challenge" {
		t.Errorf("logins = %q, want password,challenge", got)
	}
}

// Runtime ticket acquisition: with no cached ticket and no terminal, a TFA
// user gets a clear auth error naming --otp / PVE_CLI_OTP (exit 2); with
// PVE_CLI_OTP set, the command completes the challenge itself.
func TestRuntimeTicketTFA(t *testing.T) {
	totpEnv(t)
	noTTY(t)
	ts := newTOTPServer(t)
	// Non-interactive login without a code stays offline (no challenge seen).
	if _, err := runCLI(t, totpLogin(ts)...); err != nil {
		t.Fatalf("auth login: %v", err)
	}
	if got := ts.loginLog(); got != "" {
		t.Fatalf("non-interactive login without a code should not contact the server; logins = %q", got)
	}

	_, err := runCLI(t, "raw", "version")
	if err == nil {
		t.Fatal("want a TFA-required error")
	}
	for _, want := range []string{"two-factor", "pc auth login", "--otp", "PVE_CLI_OTP"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), totpPW) {
		t.Errorf("error leaks the password: %v", err)
	}
	if code := ExitCodeFor(err); code != ExitAuthConfig {
		t.Errorf("exit code = %d, want %d", code, ExitAuthConfig)
	}

	t.Setenv("PVE_CLI_OTP", totpCode)
	if _, err := runCLI(t, "raw", "version"); err != nil {
		t.Fatalf("raw version with PVE_CLI_OTP: %v", err)
	}
	if got := ts.loginLog(); got != "password,password,challenge" {
		t.Errorf("logins = %q", got)
	}
	if _, ok := auth.NewKeyringTicketCache(keyringService, "pve", ts.URL, totpUser).Load(); !ok {
		t.Error("runtime TFA login should cache the ticket")
	}
}
