package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ciroiriarte/pve-cli/internal/protocol"
)

const (
	tfaUser      = "ops@pve"
	tfaPassword  = "s3cret-pw"
	tfaCode      = "123456"
	tfaChallenge = "PVE:!tfa!%7B%22totp%22%3Atrue%7D:6560A2B1::sig"
	tfaTicket    = "PVE:ops@pve:6560A2B1::fullsig"
)

// tfaServer emulates PVE's two-step TFA login: password → NeedTFA challenge,
// then tfa-challenge + password=totp:<code> → full ticket. Renewal (password =
// a full ticket) answers directly, without TFA.
type tfaServer struct {
	mu       sync.Mutex
	posts    []map[string]string
	pdmShape bool // PDM: no NeedTFA flag, PDM:!tfa! body ticket, cookie on success
}

func (s *tfaServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		form := map[string]string{}
		for k := range r.PostForm {
			form[k] = r.PostForm.Get(k)
		}
		s.mu.Lock()
		s.posts = append(s.posts, form)
		s.mu.Unlock()
		if form["username"] != tfaUser {
			http.Error(w, "authentication failure", http.StatusUnauthorized)
			return
		}
		full := func() {
			if s.pdmShape {
				http.SetCookie(w, &http.Cookie{Name: "__Host-PDMAuthCookie", Value: "PDM:ops@pve:full", HttpOnly: true})
				w.Write([]byte(`{"data":{"username":"ops@pve","CSRFPreventionToken":"csrf-full"}}`))
				return
			}
			w.Write([]byte(`{"data":{"username":"ops@pve","ticket":"` + tfaTicket + `","CSRFPreventionToken":"csrf-full"}}`))
		}
		switch {
		case form["tfa-challenge"] != "":
			if form["password"] != "totp:"+tfaCode && form["password"] != "recovery:abcd-ef01" {
				http.Error(w, "authentication failure", http.StatusUnauthorized)
				return
			}
			full()
		case form["password"] == tfaTicket || form["password"] == "PDM:ops@pve:full":
			full() // renewal
		case form["password"] == tfaPassword:
			if s.pdmShape {
				w.Write([]byte(`{"data":{"username":"ops@pve","ticket":"PDM:!tfa!chal::sig","CSRFPreventionToken":"invalid"}}`))
				return
			}
			w.Write([]byte(`{"data":{"username":"ops@pve","ticket":"` + tfaChallenge + `","CSRFPreventionToken":"csrf-partial","NeedTFA":1}}`))
		default:
			http.Error(w, "authentication failure", http.StatusUnauthorized)
		}
	}
}

func newTFA(t *testing.T, s *tfaServer) (*TicketProvider, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/access/ticket", s.handler(t))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	tp, err := NewTicket(srv.URL, tfaUser, tfaPassword, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return tp, srv
}

type memCache struct {
	c     CachedTicket
	ok    bool
	saves int
}

func (m *memCache) Load() (CachedTicket, bool) { return m.c, m.ok }
func (m *memCache) Save(c CachedTicket)        { m.c, m.ok = c, true; m.saves++ }

func applyGet(t *testing.T, tp *TicketProvider) (*http.Request, error) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://x/", nil)
	return req, tp.Apply(req, false)
}

func TestTicketTFAChallengeWithOTP(t *testing.T) {
	s := &tfaServer{}
	tp, _ := newTFA(t, s)
	cache := &memCache{}
	tp.OTP = tfaCode
	tp.Cache = cache

	req, err := applyGet(t, tp)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c, _ := req.Cookie("PVEAuthCookie"); c == nil || c.Value != tfaTicket {
		t.Errorf("want the full ticket as cookie, got %v", req.Cookies())
	}
	if len(s.posts) != 2 {
		t.Fatalf("posts = %d, want 2 (password, then challenge response)", len(s.posts))
	}
	second := s.posts[1]
	if second["tfa-challenge"] != tfaChallenge || second["password"] != "totp:"+tfaCode {
		t.Errorf("challenge response form = %v", second)
	}
	if _, ok := second["otp"]; ok {
		t.Error("the legacy otp param must not be sent with tfa-challenge")
	}
	if !tp.TFA() {
		t.Error("TFA() = false after answering a challenge")
	}
	if !cache.ok || cache.c.Ticket != tfaTicket || cache.c.User != tfaUser {
		t.Errorf("TFA ticket not cached: %+v", cache.c)
	}
}

func TestTicketTFAPDMShape(t *testing.T) {
	s := &tfaServer{pdmShape: true}
	tp, _ := newTFA(t, s)
	tp.OTP = tfaCode
	req, err := applyGet(t, tp)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c, _ := req.Cookie("__Host-PDMAuthCookie"); c == nil || c.Value != "PDM:ops@pve:full" {
		t.Errorf("want the PDM auth cookie, got %v", req.Cookies())
	}
	if len(s.posts) != 2 || s.posts[1]["tfa-challenge"] != "PDM:!tfa!chal::sig" {
		t.Errorf("PDM challenge (no NeedTFA flag) not answered: %v", s.posts)
	}
}

func TestTicketTFAWrongCode(t *testing.T) {
	tp, _ := newTFA(t, &tfaServer{})
	tp.OTP = "654321"
	_, err := applyGet(t, tp)
	var ae *protocol.APIError
	if !errors.As(err, &ae) || ae.Kind != protocol.KindAuth {
		t.Fatalf("want a KindAuth APIError, got %v", err)
	}
	if !strings.Contains(err.Error(), "two-factor authentication failed") {
		t.Errorf("error should say the code was rejected: %v", err)
	}
	for _, secret := range []string{"654321", tfaPassword, tfaChallenge} {
		if strings.Contains(err.Error(), secret) || strings.Contains(ae.RawBody, secret) {
			t.Errorf("error leaks %q: %v", secret, err)
		}
	}
}

func TestTicketTFANoCodeNonInteractive(t *testing.T) {
	s := &tfaServer{}
	tp, _ := newTFA(t, s)
	_, err := applyGet(t, tp)
	var ae *protocol.APIError
	if !errors.As(err, &ae) || ae.Kind != protocol.KindAuth {
		t.Fatalf("want a KindAuth APIError, got %v", err)
	}
	for _, want := range []string{"two-factor", "--otp", "PVE_CLI_OTP"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
	if len(s.posts) != 1 {
		t.Errorf("posts = %d, want 1 (no challenge response without a code)", len(s.posts))
	}
}

func TestTicketTFAPrompt(t *testing.T) {
	tp, _ := newTFA(t, &tfaServer{})
	var asked int
	tp.PromptOTP = func() (string, error) { asked++; return " " + tfaCode + "\n", nil }
	if _, err := applyGet(t, tp); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if asked != 1 {
		t.Errorf("prompted %d times, want 1", asked)
	}
}

func TestTicketTFARecoveryPassthrough(t *testing.T) {
	s := &tfaServer{}
	tp, _ := newTFA(t, s)
	tp.OTP = "recovery:abcd-ef01"
	if _, err := applyGet(t, tp); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := s.posts[1]["password"]; got != "recovery:abcd-ef01" {
		t.Errorf("typed response rewritten: %q", got)
	}
}

// A cached TFA ticket is reused while fresh, renewed (ticket as password, no
// TFA) once older, and abandoned for a password login once expired.
func TestTicketTFACacheReuseRenewExpire(t *testing.T) {
	cases := []struct {
		name      string
		age       time.Duration
		wantPosts int
		wantFirst string // password sent on the first POST
	}{
		{"fresh reused", 10 * time.Minute, 0, ""},
		{"aged renewed", 70 * time.Minute, 1, tfaTicket},
		{"expired relogin", 3 * time.Hour, 1, tfaPassword},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &tfaServer{}
			tp, _ := newTFA(t, s)
			cache := &memCache{ok: true, c: CachedTicket{
				User: tfaUser, Ticket: tfaTicket, CSRF: "csrf-cached",
				Cookies: []CachedCookie{{Name: "PVEAuthCookie", Value: tfaTicket}},
				Issued:  time.Now().Add(-tc.age),
			}}
			tp.Cache = cache
			_, err := applyGet(t, tp)
			if tc.name == "expired relogin" {
				// No code: the password login hits TFA and must fail clearly.
				if err == nil || !strings.Contains(err.Error(), "--otp") {
					t.Fatalf("want a TFA-required error, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if len(s.posts) != tc.wantPosts {
				t.Fatalf("posts = %d, want %d", len(s.posts), tc.wantPosts)
			}
			if tc.wantPosts > 0 && s.posts[0]["password"] != tc.wantFirst {
				t.Errorf("first POST password = %q, want %q", s.posts[0]["password"], tc.wantFirst)
			}
			if tc.name == "aged renewed" && (cache.saves != 1 || time.Since(cache.c.Issued) > time.Minute) {
				t.Errorf("renewed ticket not re-cached: saves=%d issued=%v", cache.saves, cache.c.Issued)
			}
		})
	}
}

// A cached ticket for a different user is never used.
func TestTicketTFACacheUserMismatch(t *testing.T) {
	s := &tfaServer{}
	tp, _ := newTFA(t, s)
	tp.OTP = tfaCode
	tp.Cache = &memCache{ok: true, c: CachedTicket{User: "other@pve", Ticket: "PVE:other", Issued: time.Now()}}
	if _, err := applyGet(t, tp); err != nil {
		t.Fatal(err)
	}
	if len(s.posts) != 2 || s.posts[0]["password"] != tfaPassword {
		t.Errorf("want a password login, got %v", s.posts)
	}
}

// Refresh (used by `pc auth login`) always does a password login.
func TestTicketRefreshBypassesCache(t *testing.T) {
	s := &tfaServer{}
	tp, _ := newTFA(t, s)
	tp.OTP = tfaCode
	tp.Cache = &memCache{ok: true, c: CachedTicket{User: tfaUser, Ticket: tfaTicket, Issued: time.Now()}}
	if err := tp.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.posts) != 2 || s.posts[0]["password"] != tfaPassword {
		t.Errorf("Refresh should log in with the password and answer TFA, got %v", s.posts)
	}
}

// A plain 401 on the password step is a KindAuth error without the password.
func TestTicketBadPasswordIsAuthError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/access/ticket", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "authentication failure", http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tp, _ := NewTicket(srv.URL, tfaUser, "wrong-pw", srv.Client())
	_, err := applyGet(t, tp)
	var ae *protocol.APIError
	if !errors.As(err, &ae) || ae.Kind != protocol.KindAuth {
		t.Fatalf("want KindAuth, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong-pw") {
		t.Errorf("error leaks the password: %v", err)
	}
}
