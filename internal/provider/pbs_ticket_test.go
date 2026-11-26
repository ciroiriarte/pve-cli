package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/ciroiriarte/pve-cli/internal/config"
	"github.com/ciroiriarte/pve-cli/internal/transport"
)

// PBS ticket auth returns the ticket in the JSON body; requests must carry it
// as the PBSAuthCookie cookie, plus the CSRFPreventionToken header on writes.
func TestPBSTicketAuthCookieAndCSRF(t *testing.T) {
	type seen struct{ cookie, csrf string }
	got := map[string]seen{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/access/ticket", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("username") != "root@pam" || r.PostForm.Get("password") != "pw" {
			http.Error(w, "bad creds", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":{"ticket":"PBS:root@pam:ABC::sig","CSRFPreventionToken":"csrf1","username":"root@pam"}}`))
	})
	mux.HandleFunc("/api2/json/admin/datastore/store1/gc", func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie("PBSAuthCookie")
		s := seen{csrf: r.Header.Get("CSRFPreventionToken")}
		if c != nil {
			s.cookie = c.Value
		}
		got[r.Method] = s
		w.Write([]byte(`{"data":null}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cl, err := NewClient(&config.Settings{Server: srv.URL, Provider: "pbs", AuthType: "ticket", User: "root@pam", Secret: "pw"}, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := cl.DoRaw(ctx, &transport.Request{Method: http.MethodGet, Path: "/admin/datastore/store1/gc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.DoRaw(ctx, &transport.Request{Method: http.MethodPost, Path: "/admin/datastore/store1/gc", Form: url.Values{}}); err != nil {
		t.Fatal(err)
	}
	if g := got["GET"]; g.cookie != "PBS:root@pam:ABC::sig" || g.csrf != "" {
		t.Errorf("GET: cookie %q csrf %q, want PBSAuthCookie and no CSRF", g.cookie, g.csrf)
	}
	if p := got["POST"]; p.cookie != "PBS:root@pam:ABC::sig" || p.csrf != "csrf1" {
		t.Errorf("POST: cookie %q csrf %q, want PBSAuthCookie + CSRF csrf1", p.cookie, p.csrf)
	}
}

func TestNewSelectsPBSProvider(t *testing.T) {
	SetPBSFactory(func(*transport.Client) Provider { return nil })
	defer SetPBSFactory(nil)
	if _, err := New(&config.Settings{Server: "https://pbs:8007", Provider: "pbs", AuthType: "token", TokenID: "a@pbs!t", Secret: "s"}, false); err != nil {
		t.Fatalf("New(pbs): %v", err)
	}
	if _, err := New(&config.Settings{Server: "https://x", Provider: "pxe", AuthType: "token", TokenID: "a@pbs!t", Secret: "s"}, false); err == nil {
		t.Error("expected an error for an unknown provider")
	}
}
