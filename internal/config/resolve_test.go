package config

import "testing"

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
