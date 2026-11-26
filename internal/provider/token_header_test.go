package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ciroiriarte/pve-cli/internal/config"
	"github.com/ciroiriarte/pve-cli/internal/transport"
)

// Issue #30: token auth must use the provider's header format — PVE wants
// "PVEAPIToken=<id>=<secret>", PDM wants "PDMAPIToken=<id>:<secret>".
func TestTokenAuthorizationHeaderPerProvider(t *testing.T) {
	cases := []struct {
		provider, want string
	}{
		{"", "PVEAPIToken=root@pam!ci=s3cr3t"},
		{"pve", "PVEAPIToken=root@pam!ci=s3cr3t"},
		{"pdm", "PDMAPIToken=root@pam!ci:s3cr3t"},
		{"pbs", "PBSAPIToken=root@pam!ci:s3cr3t"}, // PBS docs: PBSAPIToken=TOKENID:TOKENSECRET
	}
	for _, tc := range cases {
		t.Run("provider="+tc.provider, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get("Authorization")
				w.Write([]byte(`{"data":null}`))
			}))
			defer srv.Close()

			cl, err := NewClient(&config.Settings{
				Server:   srv.URL,
				Provider: tc.provider,
				AuthType: "token",
				TokenID:  "root@pam!ci",
				Secret:   "s3cr3t",
			}, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cl.DoRaw(context.Background(), &transport.Request{Method: http.MethodGet, Path: "/version"}); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("Authorization = %q, want %q", got, tc.want)
			}
		})
	}
}
