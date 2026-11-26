package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ciroiriarte/pve-cli/internal/auth"
)

const pbsTFATicket = "PBS:ops@pve:6560A2B1::full"

// newPBSTFAServer emulates PBS's two-factor ticket login: the ticket is only
// returned in the body (no Set-Cookie), and API calls authenticate with
// PBSAuthCookie — a ticket replayed as PVEAuthCookie is rejected.
func newPBSTFAServer(t *testing.T) (*httptest.Server, func() string) {
	t.Helper()
	var mu sync.Mutex
	var logins []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/access/ticket", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f := r.PostForm
		mu.Lock()
		defer mu.Unlock()
		full := `{"data":{"username":"ops@pve","ticket":"` + pbsTFATicket + `","CSRFPreventionToken":"c"}}`
		switch {
		case f.Get("username") != totpUser:
			http.Error(w, "authentication failure", http.StatusUnauthorized)
		case f.Get("tfa-challenge") != "":
			logins = append(logins, "challenge")
			if f.Get("password") != "totp:"+totpCode {
				http.Error(w, "authentication failure", http.StatusUnauthorized)
				return
			}
			w.Write([]byte(full))
		case f.Get("password") == pbsTFATicket:
			logins = append(logins, "renew")
			w.Write([]byte(full))
		case f.Get("password") == totpPW:
			logins = append(logins, "password")
			w.Write([]byte(`{"data":{"username":"ops@pve","ticket":"PBS:!tfa!x:6560A2B1::s","NeedTFA":1}}`))
		default:
			http.Error(w, "authentication failure", http.StatusUnauthorized)
		}
	})
	mux.HandleFunc("/api2/json/version", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("PBSAuthCookie"); err != nil || c.Value != pbsTFATicket {
			http.Error(w, "authentication failure", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":{"version":"4.2.0","release":"4.2","repoid":"abc"}}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(logins, ",")
	}
}

// `pc auth login --provider pbs --user … --otp` must cache the ticket under
// PBSAuthCookie in a pbs-scoped keyring entry, and the runtime client must
// replay it with PBSAuthCookie — no second login, no new TOTP prompt.
func TestPBSTicketLoginCachesPBSCookie(t *testing.T) {
	totpEnv(t)
	noTTY(t)
	ts, loginLog := newPBSTFAServer(t)

	if _, err := runCLI(t, "auth", "login", ts.URL, "--provider", "pbs", "--user", totpUser,
		"--password-ref", "env:PC_TEST_TOTP_PW", "--profile", "pbstfa", "--otp", totpCode); err != nil {
		t.Fatalf("auth login --provider pbs --otp: %v", err)
	}
	if got := loginLog(); got != "password,challenge" {
		t.Errorf("logins = %q, want password,challenge", got)
	}

	c, ok := auth.NewKeyringTicketCache(keyringService, "pbs", ts.URL, totpUser).Load()
	if !ok || c.Ticket != pbsTFATicket {
		t.Fatalf("PBS ticket not cached under the pbs key: %+v", c)
	}
	if len(c.Cookies) != 1 || c.Cookies[0].Name != "PBSAuthCookie" {
		t.Errorf("cached cookies = %+v, want a single PBSAuthCookie", c.Cookies)
	}
	if _, ok := auth.NewKeyringTicketCache(keyringService, "pve", ts.URL, totpUser).Load(); ok {
		t.Error("a pbs ticket must not be cached under the pve key")
	}

	out, err := runCLI(t, "raw", "version")
	if err != nil {
		t.Fatalf("raw version with the cached PBS ticket: %v", err)
	}
	if !strings.Contains(out, "4.2.0") {
		t.Errorf("unexpected output: %q", out)
	}
	if got := loginLog(); got != "password,challenge" {
		t.Errorf("the cached ticket should be reused without logging in; logins = %q", got)
	}
}
