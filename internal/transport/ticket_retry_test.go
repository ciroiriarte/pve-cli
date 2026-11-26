package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ciroiriarte/pve-cli/internal/auth"
)

type memTicketCache struct {
	c       auth.CachedTicket
	ok      bool
	deletes int
}

func (m *memTicketCache) Load() (auth.CachedTicket, bool) { return m.c, m.ok }
func (m *memTicketCache) Save(c auth.CachedTicket)        { m.c, m.ok = c, true }
func (m *memTicketCache) Delete()                         { m.c, m.ok = auth.CachedTicket{}, false; m.deletes++ }

func ticketClient(t *testing.T, srv *httptest.Server, cache auth.TicketCache) *Client {
	t.Helper()
	tp, err := auth.NewTicket(srv.URL, "ops@pve", "pw", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	tp.Cache = cache
	c, err := New(Options{BaseURL: srv.URL, Auth: tp, MaxRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A 5xx from /access/ticket is transient and still retried; only
// deterministic login failures (auth) stop the retry loop.
func TestTicketLogin5xxIsRetried(t *testing.T) {
	var logins int
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/access/ticket", func(w http.ResponseWriter, _ *http.Request) {
		logins++
		if logins == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"data":{"ticket":"PVE:ok","CSRFPreventionToken":"c"}}`))
	})
	mux.HandleFunc("/api2/json/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":{}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := ticketClient(t, srv, nil)
	if _, err := c.DoRaw(context.Background(), &Request{Method: http.MethodGet, Path: "/version"}); err != nil {
		t.Fatalf("want success after a retried 503, got %v", err)
	}
	if logins != 2 {
		t.Errorf("logins = %d, want 2", logins)
	}
}

func TestTicketLogin401IsNotRetried(t *testing.T) {
	var logins int
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/access/ticket", func(w http.ResponseWriter, _ *http.Request) {
		logins++
		http.Error(w, "authentication failure", http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := ticketClient(t, srv, nil)
	if _, err := c.DoRaw(context.Background(), &Request{Method: http.MethodGet, Path: "/version"}); err == nil {
		t.Fatal("want an auth error")
	}
	if logins != 1 {
		t.Errorf("logins = %d, want 1 (a rejected login is not retried)", logins)
	}
}

// A 401 on a request authenticated with a stale persisted ticket drops the
// cache entry, logs in afresh, and replays the request once — including a
// non-idempotent one.
func TestStaleCachedTicketReauthAndRetryOnce(t *testing.T) {
	var logins, posts int
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/access/ticket", func(w http.ResponseWriter, _ *http.Request) {
		logins++
		w.Write([]byte(`{"data":{"ticket":"PVE:fresh","CSRFPreventionToken":"c"}}`))
	})
	mux.HandleFunc("/api2/json/nodes/n1/qemu/100/status/start", func(w http.ResponseWriter, r *http.Request) {
		posts++
		if c, err := r.Cookie("PVEAuthCookie"); err != nil || c.Value != "PVE:fresh" {
			http.Error(w, "invalid ticket", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":"UPID:n1:1:2:3:qmstart:100:ops@pve:"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cache := &memTicketCache{ok: true, c: auth.CachedTicket{User: "ops@pve", Ticket: "PVE:stale", Issued: time.Now()}}
	c := ticketClient(t, srv, cache)
	if _, err := c.DoRaw(context.Background(), &Request{Method: http.MethodPost, Path: "/nodes/n1/qemu/100/status/start"}); err != nil {
		t.Fatalf("want success after re-auth, got %v", err)
	}
	if posts != 2 || logins != 1 || cache.deletes != 1 {
		t.Errorf("posts=%d logins=%d deletes=%d, want 2/1/1", posts, logins, cache.deletes)
	}

	// With a verified (non-cache) ticket, a 401 is returned, not replayed.
	posts = 0
	mux2 := http.NewServeMux()
	mux2.HandleFunc("/api2/json/access/ticket", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":{"ticket":"PVE:fresh","CSRFPreventionToken":"c"}}`))
	})
	mux2.HandleFunc("/api2/json/version", func(w http.ResponseWriter, _ *http.Request) {
		posts++
		http.Error(w, "denied", http.StatusUnauthorized)
	})
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()
	c2 := ticketClient(t, srv2, nil)
	if _, err := c2.DoRaw(context.Background(), &Request{Method: http.MethodGet, Path: "/version"}); err == nil {
		t.Fatal("want a 401")
	}
	if posts != 1 {
		t.Errorf("a verified ticket's 401 must not be replayed; requests = %d", posts)
	}
}
