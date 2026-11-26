package auth

import (
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeyringTicketCacheKeyIncludesProvider(t *testing.T) {
	if got := NewKeyringTicketCache("svc", "pbs", "https://h:8007/", "u@pbs").Key; got != "ticket:pbs:u@pbs@https://h:8007" {
		t.Errorf("pbs key = %q", got)
	}
	if got := NewKeyringTicketCache("svc", "", "https://h:8006", "u@pam").Key; got != "ticket:pve:u@pam@https://h:8006" {
		t.Errorf("default provider key = %q", got)
	}
}

// A ticket cached for one backend is never read for another on the same URL.
func TestKeyringTicketCacheIsolatesProviders(t *testing.T) {
	keyring.MockInit()
	NewKeyringTicketCache("svc", "pve", "https://h", "u@pam").Save(CachedTicket{User: "u@pam", Ticket: "T"})
	if _, ok := NewKeyringTicketCache("svc", "pbs", "https://h", "u@pam").Load(); ok {
		t.Error("pbs must not read a pve ticket")
	}
	if c, ok := NewKeyringTicketCache("svc", "", "https://h", "u@pam").Load(); !ok || c.Ticket != "T" {
		t.Errorf("empty provider should mean pve: %+v %v", c, ok)
	}
}

func TestCachedTicketCookieListUsesProviderName(t *testing.T) {
	c := CachedTicket{Ticket: "T"}
	if got := c.cookieList(CookieNameFor("pbs")); got[0].Name != "PBSAuthCookie" {
		t.Errorf("pbs cookie = %q", got[0].Name)
	}
	if got := c.cookieList(CookieNameFor("pve")); got[0].Name != "PVEAuthCookie" {
		t.Errorf("pve cookie = %q", got[0].Name)
	}
}
