package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The hyphenated→hierarchical rename (#5) must keep the old flat names working
// as hidden aliases. Each case asserts the new form and the legacy form hit the
// exact same PDM API path.
func TestHierarchicalRenameKeepsAliases(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		newForm  []string
		oldForm  []string
	}{
		{
			name:     "remote node status",
			endpoint: "/api2/json/pve/remotes/dc-east/nodes/pve-01/status",
			newForm:  []string{"remote", "node", "status", "dc-east", "pve-01"},
			oldForm:  []string{"remote", "node-status", "dc-east", "pve-01"},
		},
		{
			name:     "remote cluster status",
			endpoint: "/api2/json/pve/remotes/dc-east/cluster-status",
			newForm:  []string{"remote", "cluster", "status", "dc-east"},
			oldForm:  []string{"remote", "cluster-status", "dc-east"},
		},
		{
			name:     "remote updates list",
			endpoint: "/api2/json/pve/remotes/dc-east/updates",
			newForm:  []string{"remote", "updates", "list", "dc-east"},
			oldForm:  []string{"remote", "updates-list", "dc-east"},
		},
		{
			name:     "auto-install prepared show",
			endpoint: "/api2/json/auto-install/prepared/abc",
			newForm:  []string{"auto-install", "prepared", "show", "abc"},
			oldForm:  []string{"auto-install", "prepared-show", "abc"},
		},
		{
			name:     "resources location info",
			endpoint: "/api2/json/resources/location-info",
			newForm:  []string{"resources", "location", "info"},
			oldForm:  []string{"resources", "location-info"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hits := 0
			mux := http.NewServeMux()
			mux.HandleFunc(c.endpoint, func(w http.ResponseWriter, _ *http.Request) {
				hits++
				w.Write([]byte(`{"data":[]}`))
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()

			if _, err := runCLI(t, withPDMCreds(srv, c.newForm...)...); err != nil {
				t.Fatalf("new form %v: %v", c.newForm, err)
			}
			if _, err := runCLI(t, withPDMCreds(srv, c.oldForm...)...); err != nil {
				t.Fatalf("legacy alias %v: %v", c.oldForm, err)
			}
			if hits != 2 {
				t.Fatalf("expected new + legacy forms to hit %s twice, got %d", c.endpoint, hits)
			}
		})
	}
}
