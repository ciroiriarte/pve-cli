package transport

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeFingerprint(t *testing.T) {
	const hex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := []string{
		hex64,
		"sha256:" + hex64,
		"01:23:45:67:89:ab:cd:ef:01:23:45:67:89:ab:cd:ef:01:23:45:67:89:ab:cd:ef:01:23:45:67:89:ab:cd:ef",
	}
	for _, in := range cases {
		got, err := normalizeFingerprint(in)
		if err != nil {
			t.Fatalf("normalizeFingerprint(%q): %v", in, err)
		}
		if got != hex64 {
			t.Errorf("normalizeFingerprint(%q) = %q, want %q", in, got, hex64)
		}
	}
}

func TestNormalizeFingerprintRejectsBad(t *testing.T) {
	for _, in := range []string{"", "deadbeef", "sha256:zz"} {
		if _, err := normalizeFingerprint(in); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

func TestProbeServerCert(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	fp, trusted, err := ProbeServerCert(srv.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("ProbeServerCert: %v", err)
	}
	if trusted {
		t.Error("httptest self-signed cert must not be reported as system-trusted")
	}

	sum := sha256.Sum256(srv.Certificate().Raw)
	want := "sha256:" + strings.ToUpper(colonize(hex.EncodeToString(sum[:])))
	if fp != want {
		t.Errorf("fingerprint = %q, want %q", fp, want)
	}
	// The returned fingerprint must be in a form the pinning path accepts and
	// that actually matches this server's leaf cert.
	norm, err := normalizeFingerprint(fp)
	if err != nil {
		t.Fatalf("normalizeFingerprint(probe result): %v", err)
	}
	if verr := pinVerifier(norm)([][]byte{srv.Certificate().Raw}, nil); verr != nil {
		t.Errorf("pinVerifier rejected the probed fingerprint: %v", verr)
	}
}

func TestProbeServerCertUnreachable(t *testing.T) {
	// 127.0.0.1:1 is reserved/closed; the dial must fail fast, not hang.
	if _, _, err := ProbeServerCert("https://127.0.0.1:1", 2*time.Second); err == nil {
		t.Error("expected an error dialing a closed port")
	}
}

func TestDialTargetDefaultsPort(t *testing.T) {
	hp, host, err := dialTarget("https://pve.example")
	if err != nil {
		t.Fatalf("dialTarget: %v", err)
	}
	if hp != "pve.example:443" || host != "pve.example" {
		t.Errorf("dialTarget = (%q,%q), want (pve.example:443, pve.example)", hp, host)
	}
	// bare host:port (no scheme) should also work and keep the explicit port.
	hp, _, err = dialTarget("10.2.0.210:8006")
	if err != nil {
		t.Fatalf("dialTarget bare: %v", err)
	}
	if hp != "10.2.0.210:8006" {
		t.Errorf("dialTarget bare host:port = %q, want 10.2.0.210:8006", hp)
	}
}

func TestRetryableStatus(t *testing.T) {
	for _, c := range []int{429, 502, 503, 504} {
		if !retryableStatus(c) {
			t.Errorf("%d should be retryable", c)
		}
	}
	for _, c := range []int{200, 400, 401, 404, 409} {
		if retryableStatus(c) {
			t.Errorf("%d should not be retryable", c)
		}
	}
}

func TestIsIdempotent(t *testing.T) {
	if !isIdempotent("GET") || isIdempotent("POST") || isIdempotent("DELETE") {
		t.Error("idempotency classification wrong")
	}
}
