package cli

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #38: --insecure=false must override an env/profile insecure: true, so
// verification is back on and the self-signed test cert is rejected.
func TestInsecureFalseFlagOverridesEnv(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":{"version":"8.2.2","release":"8.2","repoid":"abc"}}`))
	}))
	defer srv.Close()
	t.Setenv("PVE_CLI_CONFIG", filepath.Join(t.TempDir(), "none.yaml"))
	t.Setenv("PVE_CLI_INSECURE", "true")

	// Env insecure alone: the unverified cert is accepted.
	if _, err := runCLI(t, withCreds(srv, "raw", "version")...); err != nil {
		t.Fatalf("env insecure=true should accept the self-signed cert: %v", err)
	}
	// Explicit --insecure=false wins: verification fails.
	_, err := runCLI(t, withCreds(srv, "--insecure=false", "raw", "version")...)
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("--insecure=false should re-enable verification, got %v", err)
	}
}
