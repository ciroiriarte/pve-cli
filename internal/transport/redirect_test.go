package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ciroiriarte/pve-cli/internal/auth"
)

// sink is a plaintext server standing in for a redirect target; it records
// whether any credential reached it.
func sink(t *testing.T) (*httptest.Server, *bool) {
	t.Helper()
	var leaked bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || len(r.Cookies()) > 0 {
			leaked = true
		}
		if err := r.ParseForm(); err == nil && r.PostForm.Get("password") != "" {
			leaked = true
		}
		w.Write([]byte(`{"data":{"ticket":"x","CSRFPreventionToken":"y"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &leaked
}

// redirector is an https server that redirects every request to target.
func redirector(t *testing.T, target string, code int) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+r.URL.Path, code)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// pinOf returns the sha256 pin of srv's leaf certificate.
func pinOf(srv *httptest.Server) string {
	sum := sha256.Sum256(srv.Certificate().Raw)
	return "sha256:" + colonize(hex.EncodeToString(sum[:]))
}

// Issue #38: an https→http redirect must not carry the API token.
func TestTokenClientRefusesRedirect(t *testing.T) {
	dst, leaked := sink(t)
	src := redirector(t, dst.URL, http.StatusFound)

	tok, _ := auth.NewToken("root@pam!ci", "s3cr3t")
	c, err := New(Options{BaseURL: src.URL, Auth: tok, TLS: TLSConfig{Fingerprint: pinOf(src)}, MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DoRaw(context.Background(), &Request{Method: http.MethodGet, Path: "/version"})
	if err == nil || !strings.Contains(err.Error(), "refusing to follow redirect to "+dst.URL) {
		t.Fatalf("expected redirect refusal naming %s, got %v", dst.URL, err)
	}
	if *leaked {
		t.Error("token was sent to the redirect target")
	}
}

// A refused redirect is deterministic, so an idempotent GET must not retry it.
func TestRefusedRedirectIsNotRetried(t *testing.T) {
	dst, _ := sink(t)
	var hits int
	src := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, dst.URL+r.URL.Path, http.StatusFound)
	}))
	defer src.Close()

	c, err := New(Options{BaseURL: src.URL, TLS: TLSConfig{Fingerprint: pinOf(src)}, MaxRetries: 3})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DoRaw(context.Background(), &Request{Method: http.MethodGet, Path: "/version"})
	if err == nil || !strings.Contains(err.Error(), ErrRedirectRefused.Error()) {
		t.Fatalf("expected redirect refusal, got %v", err)
	}
	if hits != 1 {
		t.Errorf("server hit %d times, want 1 (refused redirect must not be retried)", hits)
	}
}

// Issue #38: a 307/308 on ticket login must not replay the password body.
func TestTicketLoginRefusesRedirect(t *testing.T) {
	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		dst, leaked := sink(t)
		src := redirector(t, dst.URL, code)

		hc, err := NewHTTPClient(TLSConfig{Fingerprint: pinOf(src)}, 0)
		if err != nil {
			t.Fatal(err)
		}
		tp, err := auth.NewTicket(src.URL, "root@pam", "pw", hc)
		if err != nil {
			t.Fatal(err)
		}
		err = tp.Refresh(context.Background())
		if err == nil || !strings.Contains(err.Error(), "refusing to follow redirect") {
			t.Fatalf("HTTP %d: expected redirect refusal, got %v", code, err)
		}
		if *leaked {
			t.Errorf("HTTP %d: password was replayed to the redirect target", code)
		}
	}
}

// Issue #38: a configured pin wins over insecure — a wrong pin must fail even
// with Insecure set, and the right pin must succeed.
func TestPinWinsOverInsecure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":null}`))
	}))
	defer srv.Close()

	get := func(fp string) error {
		c, err := New(Options{BaseURL: srv.URL, TLS: TLSConfig{Insecure: true, Fingerprint: fp}, MaxRetries: 1})
		if err != nil {
			return err
		}
		_, err = c.DoRaw(context.Background(), &Request{Method: http.MethodGet, Path: "/version"})
		return err
	}
	if err := get("sha256:" + fp32()); err == nil || !strings.Contains(err.Error(), "does not match pinned") {
		t.Errorf("wrong pin with insecure: want pin mismatch, got %v", err)
	}
	if err := get(pinOf(srv)); err != nil {
		t.Errorf("right pin with insecure: %v", err)
	}
}

// Issue #38: insecure without a pin warns once on stderr; a pin suppresses it.
func TestInsecureWarnsOnceOnStderr(t *testing.T) {
	var buf bytes.Buffer
	old := stderr
	stderr = &buf
	insecureWarnOnce = sync.Once{}
	t.Cleanup(func() { stderr = old; insecureWarnOnce = sync.Once{} })

	if _, err := (TLSConfig{Insecure: true, Fingerprint: "sha256:" + fp32()}).build(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("pinned config must not warn, got %q", buf.String())
	}
	for i := 0; i < 2; i++ {
		if _, err := (TLSConfig{Insecure: true}).build(); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(buf.String(), "WARNING"); n != 1 {
		t.Errorf("want exactly one warning, got %d: %q", n, buf.String())
	}
}
