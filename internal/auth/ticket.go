package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// TicketProvider authenticates with a username/password by obtaining a Proxmox
// ticket (cookie) and CSRF token. The ticket is cached and refreshed before it
// expires (Proxmox tickets are valid ~2h).
type TicketProvider struct {
	baseURL  string // e.g. https://host:8006
	user     string // user@realm
	password string
	client   *http.Client

	mu     sync.Mutex
	ticket string
	csrf   string
	expiry time.Time
}

// NewTicket builds a TicketProvider. client must be configured with the same
// TLS policy as the main transport.
func NewTicket(baseURL, user, password string, client *http.Client) (*TicketProvider, error) {
	if user == "" || password == "" {
		return nil, fmt.Errorf("ticket auth requires a user and password")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &TicketProvider{
		baseURL:  strings.TrimRight(baseURL, "/"),
		user:     user,
		password: password,
		client:   client,
	}, nil
}

// Apply attaches the auth cookie, plus the CSRF token on write requests.
func (t *TicketProvider) Apply(req *http.Request, write bool) error {
	if err := t.ensure(req.Context()); err != nil {
		return err
	}
	t.mu.Lock()
	ticket, csrf := t.ticket, t.csrf
	t.mu.Unlock()
	req.AddCookie(&http.Cookie{Name: "PVEAuthCookie", Value: ticket})
	if write {
		req.Header.Set("CSRFPreventionToken", csrf)
	}
	return nil
}

// Refresh forces a new login.
func (t *TicketProvider) Refresh(ctx context.Context) error {
	t.mu.Lock()
	t.expiry = time.Time{}
	t.mu.Unlock()
	return t.ensure(ctx)
}

// Kind returns the mechanism name.
func (t *TicketProvider) Kind() string { return "ticket" }

// ensure logs in if there is no valid cached ticket.
func (t *TicketProvider) ensure(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ticket != "" && time.Now().Before(t.expiry) {
		return nil
	}
	form := url.Values{"username": {t.user}, "password": {t.password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.baseURL+"/api2/json/access/ticket", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("ticket login: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ticket login failed: HTTP %d", resp.StatusCode)
	}
	var env struct {
		Data struct {
			Ticket string `json:"ticket"`
			CSRF   string `json:"CSRFPreventionToken"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("decode ticket response: %w", err)
	}
	if env.Data.Ticket == "" {
		return fmt.Errorf("ticket login: empty ticket in response")
	}
	t.ticket = env.Data.Ticket
	t.csrf = env.Data.CSRF
	// Tickets last ~2h; refresh a little early.
	t.expiry = time.Now().Add(110 * time.Minute)
	return nil
}
