package auth

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

// TicketCache persists a TFA-confirmed ticket between runs, so a two-factor
// user is not asked for a code on every command. Implementations are
// best-effort: a failed Save just means the next run logs in again.
type TicketCache interface {
	Load() (CachedTicket, bool)
	Save(CachedTicket)
}

// CachedTicket is a persisted ticket. Ticket is the raw ticket string (used as
// the password to renew it); Cookies are what the server set on login.
type CachedTicket struct {
	User    string         `json:"user"`
	Ticket  string         `json:"ticket"`
	CSRF    string         `json:"csrf"`
	Cookies []CachedCookie `json:"cookies,omitempty"`
	Issued  time.Time      `json:"issued"`
}

// CachedCookie is one persisted auth cookie.
type CachedCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (c CachedTicket) cookieList() []*http.Cookie {
	out := make([]*http.Cookie, 0, len(c.Cookies))
	for _, ck := range c.Cookies {
		out = append(out, &http.Cookie{Name: ck.Name, Value: ck.Value})
	}
	if len(out) == 0 && c.Ticket != "" {
		out = append(out, &http.Cookie{Name: "PVEAuthCookie", Value: c.Ticket})
	}
	return out
}

// KeyringTicketCache stores the ticket in the OS keyring, keyed by user and
// server so a ticket is never replayed to another server or for another user.
// With no keyring available nothing is cached (the ticket is a bearer
// credential, so it is never written to the config file).
type KeyringTicketCache struct {
	Service string
	Key     string
}

// NewKeyringTicketCache returns the cache entry for user@server under service.
func NewKeyringTicketCache(service, server, user string) *KeyringTicketCache {
	return &KeyringTicketCache{Service: service, Key: "ticket:" + user + "@" + strings.TrimRight(server, "/")}
}

// Load reads the cached ticket, if any.
func (k *KeyringTicketCache) Load() (CachedTicket, bool) {
	var c CachedTicket
	v, err := keyring.Get(k.Service, k.Key)
	if err != nil || v == "" {
		return c, false
	}
	if json.Unmarshal([]byte(v), &c) != nil {
		return c, false
	}
	return c, true
}

// Save writes the ticket; errors are ignored (best-effort cache).
func (k *KeyringTicketCache) Save(c CachedTicket) {
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = keyring.Set(k.Service, k.Key, string(b))
}
