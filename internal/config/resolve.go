package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/zalando/go-keyring"
)

// Settings is the fully-resolved connection configuration the rest of the CLI
// consumes. It is produced by Resolve from file + env + flag inputs.
type Settings struct {
	Provider    string
	Server      string
	AuthType    string
	TokenID     string
	User        string
	Secret      string
	Output      string
	Remote      string
	TLSCAFile   string
	TLSFinger   string
	TLSInsecure bool
	RateQPS     float64
	RateBurst   int
	ProfileName string
	ContextName string
}

// Overrides carries explicit flag values (highest precedence). Empty string /
// nil means "not set", so the next precedence tier applies.
type Overrides struct {
	Profile     string
	Context     string
	Server      string
	TokenID     string
	Secret      string
	Output      string
	Insecure    *bool
	Fingerprint string
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
		if sec, err := resolveSecret(prof.Auth); err != nil {
			return nil, err
		} else {
			s.Secret = sec
		}
	}

	// 4. Env vars override profile values.
	s.Server = firstNonEmpty(os.Getenv("PVE_CLI_SERVER"), s.Server)
	s.TokenID = firstNonEmpty(os.Getenv("PVE_CLI_TOKEN_ID"), s.TokenID)
	s.User = firstNonEmpty(os.Getenv("PVE_CLI_USER"), s.User)
	if v := os.Getenv("PVE_CLI_TOKEN_SECRET"); v != "" {
		s.Secret = v
	}
	if v := os.Getenv("PVE_CLI_PASSWORD"); v != "" {
		s.Secret = v
	}
	s.TLSFinger = firstNonEmpty(os.Getenv("PVE_CLI_TLS_FINGERPRINT"), s.TLSFinger)
	s.Output = firstNonEmpty(os.Getenv("PVE_CLI_OUTPUT"), s.Output)
	if v := os.Getenv("PVE_CLI_INSECURE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			s.TLSInsecure = b
		}
	}

	// 5. Explicit flags override everything.
	s.Server = firstNonEmpty(ov.Server, s.Server)
	s.TokenID = firstNonEmpty(ov.TokenID, s.TokenID)
	if ov.Secret != "" {
		s.Secret = ov.Secret
	}
	s.TLSFinger = firstNonEmpty(ov.Fingerprint, s.TLSFinger)
	s.Output = firstNonEmpty(ov.Output, s.Output)
	if ov.Insecure != nil {
		s.TLSInsecure = *ov.Insecure
	}

	if s.AuthType == "" {
		s.AuthType = "token"
	}
	return s, nil
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

// resolveSecret dereferences a secret_ref (keyring://service/key or env:NAME),
// or returns the inline secret. Inline plaintext secrets emit a warning.
func resolveSecret(a AuthConfig) (string, error) {
	if a.SecretRef != "" {
		switch {
		case strings.HasPrefix(a.SecretRef, "keyring://"):
			rest := strings.TrimPrefix(a.SecretRef, "keyring://")
			service, key, ok := strings.Cut(rest, "/")
			if !ok {
				return "", fmt.Errorf("invalid keyring ref %q: want keyring://service/key", a.SecretRef)
			}
			v, err := keyring.Get(service, key)
			if err != nil {
				return "", fmt.Errorf("read secret from keyring (%s): %w", a.SecretRef, err)
			}
			return v, nil
		case strings.HasPrefix(a.SecretRef, "env:"):
			return os.Getenv(strings.TrimPrefix(a.SecretRef, "env:")), nil
		default:
			return "", fmt.Errorf("unsupported secret_ref scheme: %q", a.SecretRef)
		}
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
