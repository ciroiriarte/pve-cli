package config

import (
	"strings"
	"testing"
)

func TestWithDefaultPortPBS(t *testing.T) {
	cases := []struct{ provider, in, want string }{
		{"pbs", "https://pbs.example", "https://pbs.example:8007"},
		{"pbs", "https://pbs.example/", "https://pbs.example:8007/"},
		{"pbs", "https://pbs.example:443", "https://pbs.example:443"},
		{"pbs", "https://[fd00::1]", "https://[fd00::1]:8007"},
		{"pbs", "", ""},
		{"pbs", "pbs.example", "pbs.example"}, // not an absolute URL: untouched
		{"pve", "https://pve.example", "https://pve.example"},
		{"pdm", "https://pdm.example", "https://pdm.example"},
	}
	for _, c := range cases {
		if got := WithDefaultPort(c.provider, c.in); got != c.want {
			t.Errorf("WithDefaultPort(%q, %q) = %q, want %q", c.provider, c.in, got, c.want)
		}
	}
}

func TestResolveAppliesPBSDefaultPort(t *testing.T) {
	f := &File{
		CurrentContext: "bk",
		Contexts:       map[string]Context{"bk": {Profile: "backup"}},
		Profiles: map[string]Profile{"backup": {
			Provider: "pbs", Server: "https://pbs1",
			Auth: AuthConfig{Type: "token", TokenID: "a@pbs!t", Secret: "s"},
		}},
	}
	s, err := Resolve(f, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Provider != "pbs" || s.Server != "https://pbs1:8007" {
		t.Errorf("provider %q server %q", s.Provider, s.Server)
	}
	for _, c := range []struct {
		f    *File
		ov   Overrides
		want string
	}{
		{f, Overrides{}, "pbs"},
		{f, Overrides{Provider: "pve"}, "pve"},
		{f, Overrides{Provider: "PBS"}, "pbs"}, // case-normalized
		{&File{}, Overrides{}, "pve"},
	} {
		got, err := ResolveProvider(c.f, c.ov)
		if err != nil || got != c.want {
			t.Errorf("ResolveProvider(%+v) = %q, %v; want %q", c.ov, got, err, c.want)
		}
	}
}

// Provider names are case-normalized identically in Resolve and
// ResolveProvider, so `provider: PBS` selects pbs everywhere.
func TestProviderCaseNormalized(t *testing.T) {
	f := &File{
		CurrentContext: "bk",
		Contexts:       map[string]Context{"bk": {Profile: "backup"}},
		Profiles:       map[string]Profile{"backup": {Provider: " PBS ", Server: "https://pbs1:8007"}},
	}
	s, err := Resolve(f, Overrides{})
	if err != nil || s.Provider != "pbs" {
		t.Fatalf("Resolve provider = %q, %v; want pbs", s.Provider, err)
	}
	if p, err := ResolveProvider(f, Overrides{}); err != nil || p != "pbs" {
		t.Errorf("ResolveProvider = %q, %v; want pbs", p, err)
	}
}

// A missing profile is the same error from both entry points (shared
// selection), not a silent fallback to pve.
func TestResolveProviderMissingProfile(t *testing.T) {
	f := &File{Profiles: map[string]Profile{"a": {Provider: "pbs"}}}
	_, rerr := Resolve(f, Overrides{Profile: "typo"})
	_, perr := ResolveProvider(f, Overrides{Profile: "typo"})
	if rerr == nil || perr == nil || rerr.Error() != perr.Error() || !strings.Contains(perr.Error(), `profile "typo" not found`) {
		t.Errorf("Resolve err %v, ResolveProvider err %v; want the same profile-not-found error", rerr, perr)
	}
}
