package config

import (
	"path/filepath"
	"testing"
)

func TestSetValueCoercesTypesSoConfigStillLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	// String, number, and bool values into typed fields.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(SetValue(path, "profiles.x.server", "https://pve1:8006"))
	must(SetValue(path, "profiles.x.provider", "pve"))
	must(SetValue(path, "profiles.x.rate_limit.qps", "20"))
	must(SetValue(path, "profiles.x.tls.verify", "false"))

	// The config must still load via the typed struct (regression: previously
	// "20"/"false" were written as strings and broke typed loading).
	f, err := Load(path)
	if err != nil {
		t.Fatalf("config no longer loads after typed set: %v", err)
	}
	p := f.Profiles["x"]
	if p.Server != "https://pve1:8006" {
		t.Errorf("server = %q", p.Server)
	}
	if p.RateLimit.QPS != 20 {
		t.Errorf("rate_limit.qps = %v, want 20", p.RateLimit.QPS)
	}
	if p.TLS.Verify == nil || *p.TLS.Verify != false {
		t.Errorf("tls.verify = %v, want false", p.TLS.Verify)
	}
}

func TestCoerceScalar(t *testing.T) {
	cases := map[string]any{
		"true": true, "false": false, "20": int64(20), "2.5": 2.5,
		"https://h:8006": "https://h:8006", "user@pam!t": "user@pam!t",
	}
	for in, want := range cases {
		if got := coerceScalar(in); got != want {
			t.Errorf("coerceScalar(%q) = %v (%T), want %v (%T)", in, got, got, want, want)
		}
	}
}
