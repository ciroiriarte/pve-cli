package config

import "testing"

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
	if got := ResolveProvider(f, Overrides{}); got != "pbs" {
		t.Errorf("ResolveProvider = %q, want pbs", got)
	}
	if got := ResolveProvider(f, Overrides{Provider: "pve"}); got != "pve" {
		t.Errorf("ResolveProvider with flag = %q, want pve", got)
	}
	if got := ResolveProvider(&File{}, Overrides{}); got != "pve" {
		t.Errorf("ResolveProvider default = %q, want pve", got)
	}
}
