package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/ciroiriarte/pve-cli/internal/auth"
	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// ticketCompletionServer counts /access/ticket POSTs (and any carrying a TFA
// response) and serves guests only to requests with the cached ticket cookie.
func ticketCompletionServer(t *testing.T, ticketPosts, tfaPosts *int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/access/ticket", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		atomic.AddInt32(ticketPosts, 1)
		if r.PostForm.Get("tfa-challenge") != "" || len(r.PostForm.Get("password")) == 6 {
			atomic.AddInt32(tfaPosts, 1)
		}
		w.Write([]byte(`{"data":{"username":"ops@pve","ticket":"PVE:!tfa!x::sig","CSRFPreventionToken":"c","NeedTFA":1}}`))
	})
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("PVEAuthCookie"); err != nil || c.Value != "PVE:ops@pve:cached" {
			http.Error(w, "no ticket", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":[{"type":"qemu","vmid":100,"name":"web-01","node":"pve-01"}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func ticketProfile(t *testing.T, server string) {
	t.Helper()
	writeConfig(t, "current_context: c\ncontexts:\n  c: {profile: p}\nprofiles:\n  p:\n    server: "+server+
		"\n    auth: {type: ticket, user: \"ops@pve\", secret: pw}\n")
}

// A <TAB> never sends a TFA code (PVE_CLI_OTP is ignored) and never does a
// password login: with no cached ticket, ticket auth yields no suggestions
// and no /access/ticket request at all.
func TestCompleteTicketNoCachedTicketNoLogin(t *testing.T) {
	isolateCache(t)
	var posts, tfa int32
	srv := ticketCompletionServer(t, &posts, &tfa)
	ticketProfile(t, srv.URL)
	t.Setenv("PVE_CLI_OTP", "123456")
	if got := complete(t, "vm", "start", ""); len(got) != 0 {
		t.Errorf("want no suggestions, got %q", got)
	}
	if p, f := atomic.LoadInt32(&posts), atomic.LoadInt32(&tfa); p != 0 || f != 0 {
		t.Errorf("/access/ticket posts = %d (with TFA code: %d), want none", p, f)
	}
}

// With a fresh cached (TFA-confirmed) ticket in the keyring, completion uses
// it without logging in.
func TestCompleteTicketUsesCachedTicket(t *testing.T) {
	isolateCache(t)
	var posts, tfa int32
	srv := ticketCompletionServer(t, &posts, &tfa)
	ticketProfile(t, srv.URL)
	t.Setenv("PVE_CLI_OTP", "123456")
	c := auth.NewKeyringTicketCache(provider.KeyringService, "pve", srv.URL, "ops@pve")
	b, _ := json.Marshal(auth.CachedTicket{User: "ops@pve", Ticket: "PVE:ops@pve:cached", CSRF: "c",
		Cookies: []auth.CachedCookie{{Name: "PVEAuthCookie", Value: "PVE:ops@pve:cached"}}, Issued: time.Now()})
	if err := keyring.Set(c.Service, c.Key, string(b)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Delete)
	if got, want := complete(t, "vm", "start", ""), []string{"100\tweb-01", "web-01\tvmid 100"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if p := atomic.LoadInt32(&posts); p != 0 {
		t.Errorf("/access/ticket posts = %d, want 0 (cached ticket reused)", p)
	}
}
