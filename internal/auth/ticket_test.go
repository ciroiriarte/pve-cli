package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTicketProviderLoginAndCSRF(t *testing.T) {
	var logins int
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/access/ticket", func(w http.ResponseWriter, r *http.Request) {
		logins++
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("username") != "root@pam" || r.PostForm.Get("password") != "pw" {
			http.Error(w, "bad creds", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":{"ticket":"PVE:tkt123","CSRFPreventionToken":"csrf456"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tp, err := NewTicket(srv.URL, "root@pam", "pw", srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	// Read request: cookie set, no CSRF header.
	getReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/x", nil)
	if err := tp.Apply(getReq, false); err != nil {
		t.Fatal(err)
	}
	if c, _ := getReq.Cookie("PVEAuthCookie"); c == nil || c.Value != "PVE:tkt123" {
		t.Errorf("auth cookie not set correctly: %v", getReq.Cookies())
	}
	if getReq.Header.Get("CSRFPreventionToken") != "" {
		t.Error("CSRF header must not be set on read requests")
	}

	// Write request: CSRF header set.
	postReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/x", nil)
	if err := tp.Apply(postReq, true); err != nil {
		t.Fatal(err)
	}
	if postReq.Header.Get("CSRFPreventionToken") != "csrf456" {
		t.Errorf("CSRF header = %q, want csrf456", postReq.Header.Get("CSRFPreventionToken"))
	}

	// Ticket is cached: only one login despite two Apply calls.
	if logins != 1 {
		t.Errorf("logins = %d, want 1 (ticket should be cached)", logins)
	}
}

func TestNewTicketRequiresCreds(t *testing.T) {
	if _, err := NewTicket("https://h:8006", "", "", nil); err == nil {
		t.Error("expected error without creds")
	}
}

// NewTicket with a nil client must not fall back to a redirect-following
// client: a 307 would replay the password body to the redirect target.
func TestNewTicketNilClientRefusesRedirect(t *testing.T) {
	var leaked bool
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err == nil && r.PostForm.Get("password") != "" {
			leaked = true
		}
		w.Write([]byte(`{"data":{"ticket":"x","CSRFPreventionToken":"y"}}`))
	}))
	defer dst.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dst.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer src.Close()

	tp, err := NewTicket(src.URL, "root@pam", "pw", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tp.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing to follow redirect") {
		t.Fatalf("expected redirect refusal, got %v", err)
	}
	if leaked {
		t.Error("password was replayed to the redirect target")
	}
}
