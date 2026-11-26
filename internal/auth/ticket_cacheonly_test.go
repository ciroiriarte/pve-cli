package auth

import (
	"strings"
	"testing"
	"time"
)

// CacheOnly (shell completion) never logs in with the password or a TFA code:
// a fresh cached ticket is used as-is, an aged one may be renewed, and without
// a usable one the request fails without any POST.
func TestTicketCacheOnly(t *testing.T) {
	cases := []struct {
		name      string
		cached    bool
		age       time.Duration
		wantPosts int
		wantErr   bool
	}{
		{"no cache", false, 0, 0, true},
		{"fresh reused", true, 10 * time.Minute, 0, false},
		{"aged renewed", true, 70 * time.Minute, 1, false},
		{"expired", true, 3 * time.Hour, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &tfaServer{}
			tp, _ := newTFA(t, s)
			tp.CacheOnly = true
			tp.OTP = tfaCode // must never be sent
			cache := &memCache{}
			if tc.cached {
				cache = &memCache{ok: true, c: CachedTicket{
					User: tfaUser, Ticket: tfaTicket, CSRF: "csrf-cached",
					Cookies: []CachedCookie{{Name: "PVEAuthCookie", Value: tfaTicket}},
					Issued:  time.Now().Add(-tc.age),
				}}
			}
			tp.Cache = cache
			_, err := applyGet(t, tp)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "no usable cached ticket") {
				t.Errorf("error = %v", err)
			}
			if len(s.posts) != tc.wantPosts {
				t.Fatalf("posts = %d, want %d: %v", len(s.posts), tc.wantPosts, s.posts)
			}
			for _, p := range s.posts {
				if p["password"] == tfaPassword || p["tfa-challenge"] != "" {
					t.Errorf("password/TFA login attempted: %v", p)
				}
			}
		})
	}
}
