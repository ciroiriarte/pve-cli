package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// #26: `access realm create --type openid` posts the promoted OIDC fields, and
// --client-key-ref resolves the secret from the environment (kept off argv).
func TestAccessRealmCreateOpenIDFromEnvRef(t *testing.T) {
	var got url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/access/domains", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		w.Write([]byte(`{"data":null}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("OIDC_TEST_SECRET", "s3cr3t-value")
	_, err := runCLI(t, withCreds(srv, "access", "realm", "create", "keycloak",
		"--type", "openid", "--issuer-url", "https://idp.example.com/realms/main",
		"--client-id", "pve-cluster", "--client-key-ref", "env:OIDC_TEST_SECRET",
		"--username-claim", "preferred_username", "--autocreate")...)
	if err != nil {
		t.Fatalf("access realm create: %v", err)
	}
	want := map[string]string{
		"realm": "keycloak", "type": "openid",
		"issuer-url": "https://idp.example.com/realms/main",
		"client-id":  "pve-cluster", "username-claim": "preferred_username",
		"autocreate": "1", "client-key": "s3cr3t-value",
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("param %q = %q, want %q (all: %v)", k, got.Get(k), v, got)
		}
	}
	// An unset boolean stays out so the API keeps its default.
	if _, ok := got["default"]; ok {
		t.Errorf("unset --default must be omitted, got %q", got.Get("default"))
	}
}

// `add` is an alias for `create`.
func TestAccessRealmAddAlias(t *testing.T) {
	var got url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/access/domains", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		w.Write([]byte(`{"data":null}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, err := runCLI(t, withCreds(srv, "access", "realm", "add", "ldap1", "--type", "ldap")...); err != nil {
		t.Fatalf("access realm add: %v", err)
	}
	if got.Get("realm") != "ldap1" || got.Get("type") != "ldap" {
		t.Errorf("unexpected params via add alias: %v", got)
	}
}

// --client-key and --client-key-ref are mutually exclusive.
func TestAccessRealmClientKeyMutualExclusion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	_, err := runCLI(t, withCreds(srv, "access", "realm", "create", "k", "--type", "openid",
		"--client-key", "x", "--client-key-ref", "env:Y")...)
	if err == nil || !strings.Contains(err.Error(), "client-key") {
		t.Fatalf("expected a mutual-exclusion error, got %v", err)
	}
}

// realm delete is confirm-gated and hits the escaped realm path.
func TestAccessRealmDelete(t *testing.T) {
	var method string
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/access/domains/keycloak", func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		w.Write([]byte(`{"data":null}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, err := runCLI(t, withCreds(srv, "access", "realm", "delete", "keycloak", "--yes")...); err != nil {
		t.Fatalf("access realm delete: %v", err)
	}
	if method != "DELETE" {
		t.Errorf("expected DELETE, got %s", method)
	}
}

// Realm writes are PVE-only; on PDM they fail fast pointing at `pc server realm`.
func TestAccessRealmCreateRefusedOnPDM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access/domains") && r.Method == "POST" {
			t.Errorf("no create request expected on PDM refusal")
		}
	}))
	defer srv.Close()

	_, err := runCLI(t, withPDMCreds(srv, "access", "realm", "create", "k", "--type", "openid")...)
	if err == nil || !strings.Contains(err.Error(), "server realm") {
		t.Fatalf("expected a PVE-only refusal pointing at `pc server realm`, got %v", err)
	}
}
