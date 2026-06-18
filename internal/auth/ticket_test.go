package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
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
