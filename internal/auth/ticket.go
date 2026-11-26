package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ciroiriarte/pve-cli/internal/protocol"
)

const (
	// ticketLifetime is how long a ticket is used at most. Proxmox tickets
	// are valid for 2h; stop a little early.
	ticketLifetime = 110 * time.Minute
	// ticketRenewAfter is the age after which a ticket (in memory or
	// persisted) is renewed — POST /access/ticket with the ticket as the
	// password — instead of reused as-is. Renewal keeps the TFA-confirmed
	// state, so it never re-prompts.
	ticketRenewAfter = 60 * time.Minute
)

// Invalidator is implemented by auth providers that can tell a 401 was caused
// by a stale persisted credential. Invalidate drops it and reports whether a
// retry with fresh credentials makes sense.
type Invalidator interface {
	Invalidate() bool
}

// Verifier is implemented by auth providers that want to know a request
// authenticated successfully (a 2xx), e.g. to stop treating a persisted
// credential as unverified.
type Verifier interface {
	Verified()
}

// EnsureSecureURL rejects sending credentials over plaintext http to a
// non-loopback host — tokens, ticket passwords and TFA codes would travel in
// the clear. Loopback http stays allowed for local SSH tunnels and tests.
func EnsureSecureURL(u *url.URL) error {
	if u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("transport: refusing to send credentials over %q to %q — use https (loopback http is allowed for tunnels/tests)", u.Scheme, u.Hostname())
	}
	return nil
}

// isLoopbackHost reports whether host is a loopback address or "localhost",
// where plaintext http carries no network-exposure risk (tunnels, tests).
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// TicketProvider authenticates with a username/password by obtaining a Proxmox
// ticket (cookie) and CSRF token. The ticket is kept in memory, renewed once
// it is an hour old, and replaced before it expires (Proxmox tickets are valid
// ~2h).
//
// For a user with two-factor authentication, the first /access/ticket call
// returns a TFA challenge (a "PVE:!tfa!…" / "PDM:!tfa!…" ticket; PVE also sets
// NeedTFA). The provider answers it with a second /access/ticket call carrying
// tfa-challenge=<challenge> and password=totp:<code>. Because the password
// alone can't produce a usable ticket for such a user, a TFA-confirmed ticket
// is persisted in Cache and renewed from there on later runs.
type TicketProvider struct {
	baseURL  string // e.g. https://host:8006
	user     string // user@realm
	password string
	client   *http.Client
	// CookieName is the auth cookie built from the body ticket when the server
	// sets no cookie itself: "PVEAuthCookie" (default) or "PBSAuthCookie".
	// Set it with CookieNameFor so every ticket login path agrees.
	CookieName string

	// OTP is the second-factor response used when the server answers with a
	// TFA challenge: a bare TOTP code, or a typed response such as
	// "recovery:<key>" (sent as-is).
	OTP string
	// PromptOTP, when set and OTP is empty, is called to obtain a code
	// interactively. Nil means non-interactive: a challenge then fails with
	// an error naming --otp / PVE_CLI_OTP.
	PromptOTP func() (string, error)
	// Cache, when set, persists TFA-confirmed tickets across processes.
	Cache TicketCache

	now func() time.Time // clock; nil means time.Now (tests override it)

	mu        sync.Mutex
	cookies   []*http.Cookie // auth cookie(s) from Set-Cookie (PVE or PDM)
	csrf      string
	ticket    string    // raw ticket string, the password for renewal
	issued    time.Time // when the current ticket was obtained
	fromCache bool      // current ticket was adopted from Cache, unverified
	skipCache bool      // a cached ticket was rejected: never load from Cache again
	tfaChain  bool      // current ticket descends from a TFA login: persist renewals
	tfa       bool      // the last password login answered a TFA challenge
}

// CookieNameFor returns the auth cookie a backend expects for a body-only
// ticket: "PBSAuthCookie" for pbs, "" (the PVEAuthCookie default) otherwise.
func CookieNameFor(provider string) string {
	if provider == "pbs" {
		return "PBSAuthCookie"
	}
	return ""
}

// NewProviderTicket is the single construction point for a backend's ticket
// provider: it applies the backend's cookie name and the keyring ticket cache
// (keyed by provider, user and server), so `pc auth login` and the runtime
// client always cache and replay the same cookie.
func NewProviderTicket(provider, service, baseURL, user, password string, client *http.Client) (*TicketProvider, error) {
	t, err := NewTicket(baseURL, user, password, client)
	if err != nil {
		return nil, err
	}
	t.CookieName = CookieNameFor(provider)
	t.Cache = NewKeyringTicketCache(service, provider, baseURL, user)
	return t, nil
}

// NewTicket builds a TicketProvider. client must be configured with the same
// TLS policy as the main transport. baseURL must be https (or loopback http):
// the password and any TFA code are never sent in cleartext.
func NewTicket(baseURL, user, password string, client *http.Client) (*TicketProvider, error) {
	if user == "" || password == "" {
		return nil, fmt.Errorf("ticket auth requires a user and password")
	}
	base := strings.TrimRight(baseURL, "/")
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("ticket auth: server must be an absolute https URL, got %q", baseURL)
	}
	if err := EnsureSecureURL(u); err != nil {
		return nil, err
	}
	if client == nil {
		// Not http.DefaultClient: it follows redirects, and a 307/308 would
		// replay the password body to the redirect target.
		client = &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return fmt.Errorf("ticket login: refusing to follow redirect to %s", req.URL.Redacted())
			},
		}
	}
	return &TicketProvider{
		baseURL:  base,
		user:     user,
		password: password,
		client:   client,
	}, nil
}

// Apply attaches the auth cookie, plus the CSRF token on write requests.
func (t *TicketProvider) Apply(req *http.Request, write bool) error {
	if err := t.login(req.Context(), true); err != nil {
		return err
	}
	t.mu.Lock()
	cookies, csrf := t.cookies, t.csrf
	t.mu.Unlock()
	for _, c := range cookies {
		req.AddCookie(c)
	}
	if write {
		req.Header.Set("CSRFPreventionToken", csrf)
	}
	return nil
}

// Refresh forces a new password login, bypassing both the in-memory and the
// persisted ticket (`pc auth login` uses it to verify credentials and complete
// TFA).
func (t *TicketProvider) Refresh(ctx context.Context) error {
	t.mu.Lock()
	t.clear()
	t.mu.Unlock()
	return t.login(ctx, false)
}

// Invalidate handles a 401: if the ticket in use was adopted from the
// persisted cache and no request has succeeded with it yet, it is stale (e.g.
// the server's auth key changed). Drop it from memory, stop consulting the
// cache for this provider (so a failed keyring delete can't hand the same
// ticket back), delete the cache entry only if it still holds that ticket
// (another process may have stored a fresh one), and report true so the
// caller retries once.
func (t *TicketProvider) Invalidate() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.fromCache {
		return false
	}
	used := t.ticket
	t.clear()
	t.skipCache = true
	if t.Cache != nil {
		if c, ok := t.Cache.Load(); ok && c.Ticket == used {
			t.Cache.Delete()
		}
	}
	return true
}

// Verified records that a request succeeded with the current ticket, so a
// later 401 is a real authorization failure, not a stale cached ticket.
func (t *TicketProvider) Verified() {
	t.mu.Lock()
	t.fromCache = false
	t.mu.Unlock()
}

// Kind returns the mechanism name.
func (t *TicketProvider) Kind() string { return "ticket" }

// TFA reports whether the last password login answered a TFA challenge.
func (t *TicketProvider) TFA() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tfa
}

// ticketResult is one decoded /access/ticket response.
type ticketResult struct {
	ticket    string // body ticket, or the auth cookie value when only a cookie is set
	csrf      string
	cookies   []*http.Cookie
	challenge bool // a TFA challenge, not a usable ticket
}

func (t *TicketProvider) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// login makes sure a usable ticket is held: the in-memory one while fresh
// (renewed once it is ticketRenewAfter old), else — with useCache — the
// persisted one, else a password login (answering a TFA challenge).
func (t *TicketProvider) login(ctx context.Context, useCache bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock()

	if len(t.cookies) > 0 {
		age := now.Sub(t.issued)
		if age >= 0 && age < ticketRenewAfter {
			return nil
		}
		if age >= 0 && age < ticketLifetime {
			err := t.renew(ctx, t.ticket)
			if err == nil || !rejected(err) {
				// Renewed, or a transient failure (5xx, network): the
				// current ticket is still valid, keep using it and try
				// renewing again on the next request.
				return nil
			}
		}
		t.clear()
	}

	// A persisted TFA-confirmed ticket: reuse it while fresh, renew it once
	// older. Any failure falls through to a password login.
	if useCache && !t.skipCache && t.Cache != nil {
		if c, ok := t.Cache.Load(); ok && c.User == t.user && c.Ticket != "" {
			age := now.Sub(c.Issued)
			adopt := func() {
				t.set(c.cookieList(t.CookieName), c.CSRF, c.Ticket, c.Issued)
				t.fromCache, t.tfaChain = true, true
			}
			switch {
			case age >= 0 && age < ticketRenewAfter:
				adopt()
				return nil
			case age >= 0 && age < ticketLifetime:
				t.tfaChain = true
				err := t.renew(ctx, c.Ticket)
				if err == nil {
					return nil
				}
				if !rejected(err) {
					adopt() // transient failure: the cached ticket is still valid
					return nil
				}
				t.tfaChain = false
			}
		}
	}

	res, err := t.post(ctx, url.Values{"username": {t.user}, "password": {t.password}})
	if err != nil {
		return err
	}
	t.tfa = false
	if res.challenge {
		code, err := t.otpCode()
		if err != nil {
			return err
		}
		res, err = t.post(ctx, url.Values{
			"username":      {t.user},
			"tfa-challenge": {res.ticket},
			"password":      {tfaResponse(code)},
		})
		if err != nil {
			if ae, ok := err.(*protocol.APIError); ok && ae.Kind == protocol.KindAuth {
				ae.Message = fmt.Sprintf("two-factor authentication failed for %s: the code was rejected (wrong or expired TOTP code?)", t.user)
			}
			return err
		}
		if res.challenge {
			return authError(fmt.Sprintf("two-factor authentication for %s was not completed by the server", t.user))
		}
		t.tfa = true
	}
	t.set(res.cookies, res.csrf, res.ticket, t.clock())
	t.tfaChain = t.tfa
	switch {
	case t.tfa:
		t.save()
	case t.Cache != nil:
		// A plain password login supersedes any persisted TFA ticket for
		// this user (e.g. TFA was removed): don't let a stale one win later.
		if _, ok := t.Cache.Load(); ok {
			t.Cache.Delete()
		}
	}
	return nil
}

// rejected reports whether a renewal failure means the ticket itself is no
// good (an auth/permission rejection, or a TFA challenge instead of a
// ticket), as opposed to a transient server or network problem.
func rejected(err error) bool {
	if errors.Is(err, errRenewChallenge) {
		return true
	}
	var ae *protocol.APIError
	if errors.As(err, &ae) {
		return ae.Kind == protocol.KindAuth || ae.Kind == protocol.KindPermission
	}
	return false
}

var errRenewChallenge = errors.New("renewal answered with a TFA challenge")

// renew trades a still-valid ticket for a fresh one (no TFA involved) and,
// for a TFA-derived ticket, persists the result. Caller holds t.mu.
func (t *TicketProvider) renew(ctx context.Context, ticket string) error {
	if ticket == "" {
		return errRenewChallenge // nothing to renew with: treat as rejected
	}
	res, err := t.post(ctx, url.Values{"username": {t.user}, "password": {ticket}})
	if err != nil {
		return err
	}
	if res.challenge {
		return errRenewChallenge
	}
	t.set(res.cookies, res.csrf, res.ticket, t.clock())
	if t.tfaChain {
		t.save()
	}
	return nil
}

// otpCode returns the second-factor response: the preset OTP, else an
// interactive prompt, else an auth error telling the user how to supply one.
func (t *TicketProvider) otpCode() (string, error) {
	code := strings.TrimSpace(t.OTP)
	if code == "" && t.PromptOTP != nil {
		c, err := t.PromptOTP()
		if err != nil {
			return "", fmt.Errorf("read two-factor code: %w", err)
		}
		code = strings.TrimSpace(c)
	}
	if code == "" {
		return "", authError(fmt.Sprintf("user %s requires two-factor authentication (TOTP) and no valid ticket is cached; "+
			"run `pc auth login --user %s --otp <code>`, or pass the code via PVE_CLI_OTP", t.user, t.user))
	}
	return code, nil
}

// tfaResponse formats the challenge response. A bare code is a TOTP code;
// a typed response (recovery:/totp:/yubico:/…) is passed through unchanged.
func tfaResponse(code string) string {
	if strings.Contains(code, ":") {
		return code
	}
	return "totp:" + code
}

// set installs a ticket as the current one. Caller holds t.mu.
func (t *TicketProvider) set(cookies []*http.Cookie, csrf, ticket string, issued time.Time) {
	t.cookies, t.csrf, t.ticket, t.issued = cookies, csrf, ticket, issued
	t.fromCache = false
}

// clear drops the current ticket. Caller holds t.mu.
func (t *TicketProvider) clear() {
	t.cookies, t.csrf, t.ticket, t.issued = nil, "", "", time.Time{}
	t.fromCache, t.tfaChain = false, false
}

// save persists the current ticket. Caller holds t.mu.
func (t *TicketProvider) save() {
	if t.Cache == nil || t.ticket == "" {
		return
	}
	c := CachedTicket{User: t.user, Ticket: t.ticket, CSRF: t.csrf, Issued: t.issued}
	for _, ck := range t.cookies {
		c.Cookies = append(c.Cookies, CachedCookie{Name: ck.Name, Value: ck.Value})
	}
	t.Cache.Save(c)
}

// post issues one POST /access/ticket. Errors carry nothing the server sent
// back (message, per-field errors, body) and never the form values (password,
// ticket, or TFA code): a server reflecting a submitted value can't leak it.
func (t *TicketProvider) post(ctx context.Context, form url.Values) (ticketResult, error) {
	var res ticketResult
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.baseURL+"/api2/json/access/ticket", strings.NewReader(form.Encode()))
	if err != nil {
		return res, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.client.Do(req)
	if err != nil {
		return res, fmt.Errorf("ticket login: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		kind := protocol.DecodeError(resp.StatusCode, nil).Kind
		msg := fmt.Sprintf("ticket login failed for %s: HTTP %d %s", t.user, resp.StatusCode, http.StatusText(resp.StatusCode))
		if kind == protocol.KindAuth {
			msg = fmt.Sprintf("ticket login failed for %s: authentication failure (check the user and password)", t.user)
		}
		return res, &protocol.APIError{Kind: kind, StatusCode: resp.StatusCode, Message: msg}
	}
	var env struct {
		Data struct {
			Ticket  string `json:"ticket"`
			CSRF    string `json:"CSRFPreventionToken"`
			NeedTFA any    `json:"NeedTFA"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return res, fmt.Errorf("decode ticket response: %w", err)
	}
	res.ticket = env.Data.Ticket
	res.csrf = env.Data.CSRF

	// The ticket is delivered as a Set-Cookie (PVE: PVEAuthCookie; PDM:
	// __Host-PDMAuthCookie, HttpOnly with no body ticket). Capture whatever the
	// server set; fall back to constructing CookieName (PVEAuthCookie or
	// PBSAuthCookie) from the body ticket
	// for servers that only return it in the JSON.
	res.cookies = resp.Cookies()
	if res.ticket == "" {
		for _, c := range res.cookies {
			if strings.Contains(strings.ToLower(c.Name), "authcookie") {
				// PathUnescape, not QueryUnescape: it decodes %XX (PVE
				// percent-encodes its cookie) but keeps '+', which is a
				// literal character in base64 ticket signatures.
				if v, err := url.PathUnescape(c.Value); err == nil {
					res.ticket = v
				} else {
					res.ticket = c.Value
				}
				break
			}
		}
	}

	// PVE flags a challenge with NeedTFA=1; PDM/proxmox-auth-api only returns
	// the partial "<PREFIX>:!tfa!…" ticket in the body.
	if truthy(env.Data.NeedTFA) || strings.Contains(res.ticket, ":!tfa!") {
		res.challenge = true
		if res.ticket == "" {
			return res, fmt.Errorf("ticket login: TFA challenge without a challenge ticket")
		}
		return res, nil
	}

	if len(res.cookies) == 0 && res.ticket != "" {
		name := t.CookieName
		if name == "" {
			name = "PVEAuthCookie"
		}
		res.cookies = []*http.Cookie{{Name: name, Value: res.ticket}}
	}
	if len(res.cookies) == 0 {
		return res, fmt.Errorf("ticket login: no auth cookie or ticket in response")
	}
	return res, nil
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != "" && x != "0"
	}
	return false
}

func authError(msg string) error {
	return &protocol.APIError{Kind: protocol.KindAuth, Message: msg}
}
