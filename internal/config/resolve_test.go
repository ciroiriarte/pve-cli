package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func baseFile() *File {
	return &File{
		CurrentContext: "home",
		Contexts: map[string]Context{
			"home": {Profile: "homelab"},
		},
		Profiles: map[string]Profile{
			"homelab": {
				Provider: "pve",
				Server:   "https://pve1:8006",
				Auth:     AuthConfig{Type: "token", TokenID: "u@pam!c", Secret: "filesecret"},
				Defaults: ProfileDefault{Output: "json"},
			},
		},
	}
}

func TestResolveFromProfile(t *testing.T) {
	s, err := Resolve(baseFile(), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Server != "https://pve1:8006" {
		t.Errorf("server = %q", s.Server)
	}
	if s.Output != "json" {
		t.Errorf("output = %q, want json (profile default)", s.Output)
	}
	if s.Secret != "filesecret" {
		t.Errorf("secret = %q", s.Secret)
	}
}

func TestEnvOverridesProfile(t *testing.T) {
	t.Setenv("PVE_CLI_SERVER", "https://env-host:8006")
	t.Setenv("PVE_CLI_TOKEN_SECRET", "envsecret")
	s, err := Resolve(baseFile(), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Server != "https://env-host:8006" {
		t.Errorf("env did not override server: %q", s.Server)
	}
	if s.Secret != "envsecret" {
		t.Errorf("env did not override secret: %q", s.Secret)
	}
}

func TestFlagOverridesEnv(t *testing.T) {
	t.Setenv("PVE_CLI_SERVER", "https://env-host:8006")
	s, err := Resolve(baseFile(), Overrides{Server: "https://flag-host:8006"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Server != "https://flag-host:8006" {
		t.Errorf("flag did not win: %q", s.Server)
	}
}

func TestUnknownProfileErrors(t *testing.T) {
	if _, err := Resolve(baseFile(), Overrides{Profile: "nope"}); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func TestValidateRequiresServerAndCreds(t *testing.T) {
	if err := (&Settings{AuthType: "token", TokenID: "x", Secret: "y"}).Validate(); err == nil {
		t.Error("expected error without server")
	}
	if err := (&Settings{Server: "https://h:8006", AuthType: "token"}).Validate(); err == nil {
		t.Error("expected error without token creds")
	}
	if err := (&Settings{Server: "https://h:8006", AuthType: "token", TokenID: "x", Secret: "y"}).Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// keyringRefFile is baseFile with the secret behind a keyring reference.
func keyringRefFile() *File {
	f := baseFile()
	p := f.Profiles["homelab"]
	p.Auth.Secret = ""
	p.Auth.SecretRef = "keyring://pve-cli/homelab"
	f.Profiles["homelab"] = p
	return f
}

// #34: a locked/unavailable keyring must not block a valid env or flag secret —
// the profile reference is only dereferenced when nothing higher is set.
func TestUnavailableKeyringDoesNotBlockOverrides(t *testing.T) {
	keyring.MockInitWithError(errors.New("keyring locked"))
	t.Cleanup(keyring.MockInit)

	t.Run("env", func(t *testing.T) {
		t.Setenv("PVE_CLI_TOKEN_SECRET", "envsecret")
		s, err := Resolve(keyringRefFile(), Overrides{})
		if err != nil {
			t.Fatalf("env secret should bypass the keyring: %v", err)
		}
		if s.Secret != "envsecret" {
			t.Errorf("secret = %q, want envsecret", s.Secret)
		}
	})
	t.Run("flag", func(t *testing.T) {
		s, err := Resolve(keyringRefFile(), Overrides{Secret: "flagsecret"})
		if err != nil {
			t.Fatalf("flag secret should bypass the keyring: %v", err)
		}
		if s.Secret != "flagsecret" {
			t.Errorf("secret = %q, want flagsecret", s.Secret)
		}
	})
	t.Run("flag ref", func(t *testing.T) {
		t.Setenv("PC_TEST_SECRET", "refsecret")
		t.Setenv("PVE_CLI_TOKEN_SECRET", "envsecret")
		s, err := Resolve(keyringRefFile(), Overrides{SecretRef: "env:PC_TEST_SECRET"})
		if err != nil {
			t.Fatalf("flag ref should bypass the keyring: %v", err)
		}
		if s.Secret != "refsecret" {
			t.Errorf("secret = %q, want refsecret (flag ref beats env)", s.Secret)
		}
	})
	t.Run("nothing else", func(t *testing.T) {
		_, err := Resolve(keyringRefFile(), Overrides{})
		if err == nil || !strings.Contains(err.Error(), "keyring") {
			t.Fatalf("expected a clear keyring error, got %v", err)
		}
	})
}

func TestSecretRefOverrideEmptyErrors(t *testing.T) {
	t.Setenv("PC_TEST_EMPTY", "")
	if _, err := Resolve(baseFile(), Overrides{SecretRef: "env:PC_TEST_EMPTY"}); err == nil {
		t.Fatal("expected error for a ref that resolves to an empty secret")
	}
}
