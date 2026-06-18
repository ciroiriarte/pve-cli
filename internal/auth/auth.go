// Package auth provides pluggable authentication for the Proxmox API.
// M1 ships token auth; ticket auth is added in a later phase.
package auth

import (
	"context"
	"fmt"
	"net/http"
)

// Provider injects credentials into outgoing requests and can refresh them.
type Provider interface {
	// Apply adds auth to the request. write indicates a mutating method, which
	// matters for ticket-based CSRF handling (a no-op for tokens).
	Apply(req *http.Request, write bool) error
	// Refresh renews short-lived credentials. No-op for API tokens.
	Refresh(ctx context.Context) error
	// Kind names the auth mechanism for diagnostics.
	Kind() string
}

// TokenProvider authenticates with a Proxmox API token. Tokens are
// non-interactive, automation-safe, and need no CSRF token.
type TokenProvider struct {
	// TokenID is "user@realm!tokenname".
	TokenID string
	// Secret is the token's UUID secret.
	Secret string
}

// NewToken builds a TokenProvider, validating the inputs.
func NewToken(tokenID, secret string) (*TokenProvider, error) {
	if tokenID == "" {
		return nil, fmt.Errorf("token id is empty")
	}
	if secret == "" {
		return nil, fmt.Errorf("token secret is empty")
	}
	return &TokenProvider{TokenID: tokenID, Secret: secret}, nil
}

// Apply sets the PVEAPIToken Authorization header.
func (t *TokenProvider) Apply(req *http.Request, _ bool) error {
	req.Header.Set("Authorization", fmt.Sprintf("PVEAPIToken=%s=%s", t.TokenID, t.Secret))
	return nil
}

// Refresh is a no-op for tokens.
func (t *TokenProvider) Refresh(context.Context) error { return nil }

// Kind returns the mechanism name.
func (t *TokenProvider) Kind() string { return "token" }
