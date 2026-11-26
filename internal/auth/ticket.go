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

	"github.com/ciroiriarte/pve-cli/internal/protocol"
)

const (
	// ticketLifetime is how long a ticket is used before a new one is
	// obtained. Proxmox tickets are valid for 2h; refresh a little early.
	ticketLifetime = 110 * time.Minute
	// ticketRenewAfter is the age after which a cached ticket is renewed
	// (POST /access/ticket with the ticket as the password) instead of reused
	// as-is. Renewal keeps the TFA-confirmed state, so it never re-prompts.
	ticketRenewAfter = 60 * time.Minute
)

// TicketProvider authenticates with a username/password by obtaining a Proxmox
// ticket (cookie) and CSRF token. The ticket is cached and refreshed before it
// expires (Proxmox tickets are valid ~2h).
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

	mu      sync.Mutex
	cookies []*http.Cookie // auth cookie(s) from Set-Cookie (PVE or PDM)
	csrf    string
	expiry  time.Time
	tfa     bool // the last login answered a TFA challenge
}

// NewTicket builds a TicketProvider. client must be configured with the same
// TLS policy as the main transport.
func NewTicket(baseURL, user, password string, client *http.Client) (*TicketProvider, error) {
	if user == "" || password == "" {
		return nil, fmt.Errorf("ticket auth requires a user and password")
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
	t.expiry = time.Time{}
	t.mu.Unlock()
	return t.login(ctx, false)
}

// Kind returns the mechanism name.
func (t *TicketProvider) Kind() string { return "ticket" }

// ticketResult is one decoded /access/ticket response.
type ticketResult struct {
	ticket    string // body ticket, or the auth cookie value when only a cookie is set
	csrf      string
	cookies   []*http.Cookie
	challenge bool // a TFA challenge, not a usable ticket
}

// ensure logs in if there is no valid cached ticket.
func (t *TicketProvider) ensure(ctx context.Context) error {
	return t.login(ctx, true)
}

// login obtains a ticket unless a valid in-memory one exists. With useCache,
// a persisted ticket is tried before the password.
func (t *TicketProvider) login(ctx context.Context, useCache bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.cookies) > 0 && time.Now().Before(t.expiry) {
		return nil
	}

	// A persisted TFA-confirmed ticket: reuse it while fresh, renew it once
	// older (renewal never triggers TFA). Any failure falls through to a
	// password login.
	if useCache && t.Cache != nil {
		if c, ok := t.Cache.Load(); ok && c.User == t.user && c.Ticket != "" {
			age := time.Since(c.Issued)
			switch {
			case age >= 0 && age < ticketRenewAfter:
				t.adopt(c.cookieList(), c.CSRF, c.Issued)
				return nil
			case age >= 0 && age < ticketLifetime:
				res, err := t.post(ctx, url.Values{"username": {t.user}, "password": {c.Ticket}})
				if err == nil && !res.challenge {
					t.adopt(res.cookies, res.csrf, time.Now())
					t.save(res)
					return nil
				}
			}
		}
	}

	res, err := t.post(ctx, url.Values{"username": {t.user}, "password": {t.password}})
	if err != nil {
		return err
	}
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
		t.save(res)
		t.tfa = true
	}
	t.adopt(res.cookies, res.csrf, time.Now())
	return nil
}

// TFA reports whether the last password login answered a TFA challenge.
func (t *TicketProvider) TFA() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tfa
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

func (t *TicketProvider) adopt(cookies []*http.Cookie, csrf string, issued time.Time) {
	t.cookies = cookies
	t.csrf = csrf
	t.expiry = issued.Add(ticketLifetime)
}

func (t *TicketProvider) save(res ticketResult) {
	if t.Cache == nil {
		return
	}
	c := CachedTicket{User: t.user, Ticket: res.ticket, CSRF: res.csrf, Issued: time.Now()}
	for _, ck := range res.cookies {
		c.Cookies = append(c.Cookies, CachedCookie{Name: ck.Name, Value: ck.Value})
	}
	t.Cache.Save(c)
}

// post issues one POST /access/ticket. Errors never include the form values
// (password, ticket, or TFA code) or the response body.
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
		ae := protocol.DecodeError(resp.StatusCode, body)
		ae.RawBody = ""
		if ae.Kind == protocol.KindAuth {
			ae.Message = fmt.Sprintf("ticket login failed for %s: authentication failure (check the user and password)", t.user)
		} else {
			ae.Message = "ticket login failed: " + ae.Message
		}
		return res, ae
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
	// server set; fall back to constructing PVEAuthCookie from the body ticket
	// for servers that only return it in the JSON.
	res.cookies = resp.Cookies()
	if res.ticket == "" {
		for _, c := range res.cookies {
			if strings.Contains(strings.ToLower(c.Name), "authcookie") {
				if v, err := url.QueryUnescape(c.Value); err == nil {
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
		res.cookies = []*http.Cookie{{Name: "PVEAuthCookie", Value: res.ticket}}
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
