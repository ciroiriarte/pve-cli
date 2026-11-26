package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/zalando/go-keyring"
)

// Settings is the fully-resolved connection configuration the rest of the CLI
// consumes. It is produced by Resolve from file + env + flag inputs.
type Settings struct {
	Provider string
	Server   string
	AuthType string
	TokenID  string
	User     string
	Secret   string
	// SecretSource names where Secret came from (flag, flag ref, env var,
	// profile ref, inline); "" when no secret was found. For diagnostics only.
	SecretSource string
	Output       string
	Remote       string
	TLSCAFile    string
	TLSFinger    string
	TLSInsecure  bool
	RateQPS      float64
	RateBurst    int
	ProfileName  string
	ContextName  string
}

// Overrides carries explicit flag values (highest precedence). Empty string /
// nil means "not set", so the next precedence tier applies.
type Overrides struct {
	Profile     string
	Context     string
	Server      string
	Provider    string
	TokenID     string
	Secret      string
	SecretRef   string // env:NAME | keyring://service/key; below Secret, above env
	Output      string
	Insecure    *bool
	Fingerprint string
}

// ResolveProvider returns the effective provider (flag > selected profile >
// "pve") with the same context/profile selection as Resolve, but without
// dereferencing any secret — so it's cheap and side-effect free (no keyring
// access, no plaintext-secret warning) for gating commands up front.
func ResolveProvider(f *File, ov Overrides) string {
	if ov.Provider != "" {
		return ov.Provider
	}
	ctxName := firstNonEmpty(ov.Context, os.Getenv("PVE_CLI_CONTEXT"), f.CurrentContext)
	profName := firstNonEmpty(ov.Profile, os.Getenv("PVE_CLI_PROFILE"))
	if profName == "" && ctxName != "" {
		profName = f.Contexts[ctxName].Profile
	}
	return orDefault(f.Profiles[profName].Provider, "pve")
}

// Resolve computes effective Settings using precedence:
// flag > env > context-selected profile > profile defaults > built-in.
func Resolve(f *File, ov Overrides) (*Settings, error) {
	s := &Settings{Provider: "pve", Output: "table"}

	// 1. Pick context (flag > env > file.current_context).
	ctxName := firstNonEmpty(ov.Context, os.Getenv("PVE_CLI_CONTEXT"), f.CurrentContext)
	s.ContextName = ctxName

	// 2. Determine profile (flag > env > context's profile).
	profName := firstNonEmpty(ov.Profile, os.Getenv("PVE_CLI_PROFILE"))
	if profName == "" && ctxName != "" {
		if c, ok := f.Contexts[ctxName]; ok {
			profName = c.Profile
			s.Remote = c.Remote
		}
	}
	s.ProfileName = profName

	// 3. Layer in the profile from file, if any.
	if profName != "" {
		prof, ok := f.Profiles[profName]
		if !ok {
			return nil, errf("profile %q not found in config", profName)
		}
		s.Provider = orDefault(prof.Provider, s.Provider)
		s.Server = prof.Server
		s.AuthType = orDefault(prof.Auth.Type, "token")
		s.TokenID = prof.Auth.TokenID
		s.User = prof.Auth.User
		s.TLSCAFile = prof.TLS.CAFile
		s.TLSFinger = prof.TLS.Fingerprint
		if prof.TLS.Verify != nil {
			s.TLSInsecure = !*prof.TLS.Verify
		}
		if prof.Defaults.Output != "" {
			s.Output = prof.Defaults.Output
		}
		s.RateQPS = prof.RateLimit.QPS
		s.RateBurst = prof.RateLimit.Burst
	}

	// The secret is decided up front (flag > flag ref > env > profile) and the
	// profile's reference is dereferenced only when nothing higher is set, so a
	// locked/unavailable keyring can't block a valid env/flag override.
	// PVE_CLI_PASSWORD wins over PVE_CLI_TOKEN_SECRET (pre-existing order).
	switch {
	case ov.Secret != "":
		s.Secret, s.SecretSource = ov.Secret, "flag --token-secret"
	case ov.SecretRef != "":
		v, err := ResolveSecretRef(ov.SecretRef)
		if err != nil {
			return nil, err
		}
		if v == "" {
			return nil, errf("--token-secret-ref %s resolved to an empty secret", ov.SecretRef)
		}
		s.Secret, s.SecretSource = v, "flag --token-secret-ref "+ov.SecretRef
	case os.Getenv("PVE_CLI_PASSWORD") != "":
		s.Secret, s.SecretSource = os.Getenv("PVE_CLI_PASSWORD"), "env PVE_CLI_PASSWORD"
	case os.Getenv("PVE_CLI_TOKEN_SECRET") != "":
		s.Secret, s.SecretSource = os.Getenv("PVE_CLI_TOKEN_SECRET"), "env PVE_CLI_TOKEN_SECRET"
	case profName != "":
		a := f.Profiles[profName].Auth
		v, err := resolveSecret(a)
		if err != nil {
			return nil, err
		}
		s.Secret = v
		switch {
		case a.SecretRef != "":
			s.SecretSource = "profile " + a.SecretRef
		case a.Secret != "":
			s.SecretSource = "inline (plaintext in config)"
		}
	}

	// 4. Env vars override profile values.
	s.Server = firstNonEmpty(os.Getenv("PVE_CLI_SERVER"), s.Server)
	s.TokenID = firstNonEmpty(os.Getenv("PVE_CLI_TOKEN_ID"), s.TokenID)
	s.User = firstNonEmpty(os.Getenv("PVE_CLI_USER"), s.User)
	s.TLSFinger = firstNonEmpty(os.Getenv("PVE_CLI_TLS_FINGERPRINT"), s.TLSFinger)
	s.Output = firstNonEmpty(os.Getenv("PVE_CLI_OUTPUT"), s.Output)
	if v := os.Getenv("PVE_CLI_INSECURE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			s.TLSInsecure = b
		}
	}

	// 5. Explicit flags override everything.
	s.Provider = firstNonEmpty(ov.Provider, s.Provider)
	s.Server = firstNonEmpty(ov.Server, s.Server)
	s.TokenID = firstNonEmpty(ov.TokenID, s.TokenID)
	s.TLSFinger = firstNonEmpty(ov.Fingerprint, s.TLSFinger)
	s.Output = firstNonEmpty(ov.Output, s.Output)
	if ov.Insecure != nil {
		s.TLSInsecure = *ov.Insecure
	}

	if s.AuthType == "" {
		s.AuthType = "token"
	}
	s.Server = WithDefaultPort(s.Provider, s.Server)
	return s, nil
}

// PBSDefaultPort is the port Proxmox Backup Server's API listens on.
const PBSDefaultPort = "8007"

// WithDefaultPort fills in PBS's API port (8007) when a pbs server URL names no
// port, so `https://backup.example` reaches the API rather than :443. PVE and
// PDM URLs are returned unchanged (their historical behavior), as is anything
// that doesn't parse as an absolute URL with a host.
func WithDefaultPort(provider, server string) string {
	if provider != "pbs" || server == "" {
		return server
	}
	u, err := url.Parse(server)
	if err != nil || u.Host == "" || u.Port() != "" {
		return server
	}
	u.Host = net.JoinHostPort(u.Hostname(), PBSDefaultPort)
	return u.String()
}

// Validate checks that the resolved settings are usable for a connection.
func (s *Settings) Validate() error {
	if s.Server == "" {
		return errf("no server configured: set a profile, --server, or PVE_CLI_SERVER")
	}
	switch s.AuthType {
	case "token":
		if s.TokenID == "" || s.Secret == "" {
			return errf("token auth requires a token id and secret")
		}
	case "ticket":
		if s.User == "" || s.Secret == "" {
			return errf("ticket auth requires a user (user@realm) and password")
		}
	default:
		return errf("unknown auth type %q", s.AuthType)
	}
	return nil
}

// ResolveSecretRef dereferences a secret reference of the form
// keyring://service/key or env:NAME into its plaintext value. It is the shared
// mechanism behind the ticket-auth secret_ref and CLI flags such as
// `access realm --client-key-ref`, so both understand the same schemes.
func ResolveSecretRef(ref string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "keyring://"):
		rest := strings.TrimPrefix(ref, "keyring://")
		service, key, ok := strings.Cut(rest, "/")
		if !ok {
			return "", fmt.Errorf("invalid keyring ref %q: want keyring://service/key", ref)
		}
		v, err := keyring.Get(service, key)
		if err != nil {
			return "", fmt.Errorf("read secret from keyring (%s): %w", ref, err)
		}
		return v, nil
	case strings.HasPrefix(ref, "env:"):
		return os.Getenv(strings.TrimPrefix(ref, "env:")), nil
	default:
		return "", fmt.Errorf("unsupported secret ref scheme: %q (want keyring://service/key or env:NAME)", ref)
	}
}

// resolveSecret dereferences a secret_ref (keyring://service/key or env:NAME),
// or returns the inline secret. Inline plaintext secrets emit a warning.
func resolveSecret(a AuthConfig) (string, error) {
	if a.SecretRef != "" {
		return ResolveSecretRef(a.SecretRef)
	}
	if a.Secret != "" {
		fmt.Fprintln(os.Stderr, "[pc] warning: plaintext secret in config; prefer secret_ref: keyring://… or an env var")
	}
	return a.Secret, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
