package auth

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// renewServer issues "PVE:t<N>" tickets. Renewals (password = a ticket) get
// renewStatus when it is non-zero.
type renewServer struct {
	mu          sync.Mutex
	renewStatus int
	passwords   []string
	n           int
}

func (s *renewServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/access/ticket", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		pw := r.PostForm.Get("password")
		s.mu.Lock()
		defer s.mu.Unlock()
		s.passwords = append(s.passwords, pw)
		if pw != tfaPassword && s.renewStatus != 0 {
			http.Error(w, "renewal failed", s.renewStatus)
			return
		}
		s.n++
		w.Write([]byte(`{"data":{"ticket":"PVE:t` + string(rune('0'+s.n)) + `","CSRFPreventionToken":"c"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func cookieValue(t *testing.T, tp *TicketProvider) string {
	t.Helper()
	req, err := applyGet(t, tp)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	c, err := req.Cookie("PVEAuthCookie")
	if err != nil {
		t.Fatalf("no auth cookie: %v", err)
	}
	return c.Value
}

// A 5xx while renewing an in-memory ticket keeps the still-valid ticket (no
// password/TFA fallback); an auth rejection of the renewal does fall back.
func TestTicketRenewal5xxKeepsTicket(t *testing.T) {
	s := &renewServer{}
	srv := s.start(t)
	tp, _ := NewTicket(srv.URL, tfaUser, tfaPassword, srv.Client())
	clk := &fakeClock{t: time.Now()}
	tp.now = clk.now
	if got := cookieValue(t, tp); got != "PVE:t1" {
		t.Fatalf("first ticket = %q", got)
	}

	s.renewStatus = http.StatusServiceUnavailable
	clk.advance(61 * time.Minute)
	if got := cookieValue(t, tp); got != "PVE:t1" {
		t.Errorf("after a 503 renewal the old ticket must be kept, got %q", got)
	}
	if last := s.passwords[len(s.passwords)-1]; last != "PVE:t1" {
		t.Errorf("want a renewal attempt with the ticket, last password %q", last)
	}
	for _, pw := range s.passwords[1:] {
		if pw == tfaPassword {
			t.Fatal("a transient renewal failure must not fall back to a password login")
		}
	}

	s.renewStatus = http.StatusUnauthorized
	clk.advance(time.Minute)
	if got := cookieValue(t, tp); got != "PVE:t2" {
		t.Errorf("a rejected renewal must fall back to a password login, got %q", got)
	}
	if last := s.passwords[len(s.passwords)-1]; last != tfaPassword {
		t.Errorf("last password = %q, want the account password", last)
	}
}

// The same for a persisted ticket that needs renewing on load: a 5xx adopts
// the still-valid cached ticket instead of asking for password + TFA.
func TestTicketCachedRenewal5xxAdoptsCachedTicket(t *testing.T) {
	s := &renewServer{renewStatus: http.StatusBadGateway}
	srv := s.start(t)
	tp, _ := NewTicket(srv.URL, tfaUser, tfaPassword, srv.Client())
	tp.Cache = &memCache{ok: true, c: CachedTicket{User: tfaUser, Ticket: "PVE:cached", Issued: time.Now().Add(-70 * time.Minute)}}
	if got := cookieValue(t, tp); got != "PVE:cached" {
		t.Errorf("want the cached ticket adopted after a 502 renewal, got %q", got)
	}
	if len(s.passwords) != 1 || s.passwords[0] != "PVE:cached" {
		t.Errorf("want exactly one renewal attempt, got %q", s.passwords)
	}
}

// failingDeleteCache is a cache whose Delete silently fails (entry stays).
type failingDeleteCache struct{ memCache }

func (f *failingDeleteCache) Delete() { f.deletes++ }

// After Invalidate the provider never reloads the rejected ticket, even when
// the keyring delete failed and Load still returns it.
func TestTicketInvalidateBypassesCacheWhenDeleteFails(t *testing.T) {
	s := &renewServer{}
	srv := s.start(t)
	tp, _ := NewTicket(srv.URL, tfaUser, tfaPassword, srv.Client())
	cache := &failingDeleteCache{memCache{ok: true, c: CachedTicket{User: tfaUser, Ticket: "PVE:stale", Issued: time.Now()}}}
	tp.Cache = cache
	if got := cookieValue(t, tp); got != "PVE:stale" {
		t.Fatalf("want the cached ticket adopted first, got %q", got)
	}
	if !tp.Invalidate() {
		t.Fatal("Invalidate() = false for a cache-adopted ticket")
	}
	if !cache.ok {
		t.Fatal("test setup: the failing delete must leave the entry")
	}
	if got := cookieValue(t, tp); got != "PVE:t1" {
		t.Errorf("re-login must not reload the rejected cached ticket, got %q", got)
	}
}

// A successful request verifies the adopted ticket: a later 401 no longer
// invalidates it. And Invalidate deletes the entry only if it still holds the
// ticket this provider used (another process may have stored a fresh one).
func TestTicketVerifiedAndCompareBeforeDelete(t *testing.T) {
	s := &renewServer{}
	srv := s.start(t)

	tp, _ := NewTicket(srv.URL, tfaUser, tfaPassword, srv.Client())
	cache := &memCache{ok: true, c: CachedTicket{User: tfaUser, Ticket: "PVE:good", Issued: time.Now()}}
	tp.Cache = cache
	cookieValue(t, tp)
	tp.Verified()
	if tp.Invalidate() {
		t.Error("Invalidate() must be false once a request succeeded with the ticket")
	}
	if !cache.ok || cache.deletes != 0 {
		t.Errorf("a verified ticket's cache entry must be kept: %+v", cache)
	}

	tp2, _ := NewTicket(srv.URL, tfaUser, tfaPassword, srv.Client())
	cache2 := &memCache{ok: true, c: CachedTicket{User: tfaUser, Ticket: "PVE:old", Issued: time.Now()}}
	tp2.Cache = cache2
	cookieValue(t, tp2)
	cache2.c.Ticket = "PVE:newer-from-another-process"
	if !tp2.Invalidate() {
		t.Fatal("Invalidate() = false for an unverified cache-adopted ticket")
	}
	if !cache2.ok || cache2.deletes != 0 {
		t.Errorf("a cache entry holding a different ticket must not be deleted: %+v", cache2)
	}
}
