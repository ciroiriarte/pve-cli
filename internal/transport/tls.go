// Package transport is the base HTTP client for the Proxmox API: TLS handling
// (system CA / custom CA / SHA-256 fingerprint pinning / insecure), retries,
// auth injection, and request/response plumbing. It knows nothing about the
// resource model.
package transport

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// NewHTTPClient builds a stdlib *http.Client with the given TLS policy and
// timeout. Exported so other layers (e.g. ticket-auth login) can issue requests
// with the same TLS handling without depending on the request pipeline.
func NewHTTPClient(tlsConf TLSConfig, timeout time.Duration) (*http.Client, error) {
	cfg, err := tlsConf.build()
	if err != nil {
		return nil, err
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     &http.Transport{TLSClientConfig: cfg},
		CheckRedirect: refuseRedirect,
	}, nil
}

// refuseRedirect is the CheckRedirect policy for every credentialed client.
// The PVE/PDM APIs never redirect; following one could downgrade https→http
// (Go forwards Authorization/cookies on same-host redirects regardless of
// scheme) or replay the ticket-login password body on a 307/308.
func refuseRedirect(req *http.Request, _ []*http.Request) error {
	return fmt.Errorf("transport: refusing to follow redirect to %s", req.URL.Redacted())
}

// insecureWarnOnce limits the insecure-TLS warning to one line per process.
var insecureWarnOnce sync.Once

// TLSConfig describes how the client validates the server certificate.
type TLSConfig struct {
	// CAFile is an optional PEM bundle to trust instead of the system pool.
	CAFile string
	// Fingerprint pins the server cert's SHA-256 fingerprint. Accepts the
	// colon-separated hex form, with or without a "sha256:" prefix.
	Fingerprint string
	// Insecure disables all verification. Opt-in footgun, warned on stderr.
	// Ignored when Fingerprint is set: a pin always wins.
	Insecure bool
}

// build constructs a *tls.Config from the TLSConfig.
func (c TLSConfig) build() (*tls.Config, error) {
	out := &tls.Config{MinVersion: tls.VersionTLS12}

	// A pin always wins over Insecure, so a configured fingerprint is never
	// silently ignored.
	if c.Fingerprint != "" {
		want, err := normalizeFingerprint(c.Fingerprint)
		if err != nil {
			return nil, err
		}
		// Pinning: do our own verification against the pinned fingerprint.
		// Disable session resumption so VerifyPeerCertificate runs on every
		// handshake — Go skips that callback on resumed sessions, which would
		// otherwise let a resumed connection bypass the pin check.
		out.InsecureSkipVerify = true
		out.SessionTicketsDisabled = true
		out.VerifyPeerCertificate = pinVerifier(want)
		return out, nil
	}

	if c.Insecure {
		// build only backs credentialed clients, so credentials will cross an
		// unverified connection: say so, once.
		insecureWarnOnce.Do(func() {
			fmt.Fprintln(stderr, "[pc] WARNING: TLS verification is disabled (insecure); credentials are sent to an unverified server. Pin the cert with --tls-fingerprint instead.")
		})
		out.InsecureSkipVerify = true
		return out, nil
	}

	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read ca file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %s", c.CAFile)
		}
		out.RootCAs = pool
	}
	return out, nil
}

// pinVerifier returns a callback that accepts the connection only if the leaf
// certificate's SHA-256 fingerprint matches want.
func pinVerifier(want string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return fmt.Errorf("tls: no certificate presented by server")
		}
		sum := sha256.Sum256(rawCerts[0])
		got := hex.EncodeToString(sum[:])
		if !strings.EqualFold(got, want) {
			return fmt.Errorf("tls: server fingerprint %s does not match pinned %s", colonize(got), colonize(want))
		}
		return nil
	}
}

// ProbeServerCert connects to server without verifying the certificate and
// returns the leaf certificate's SHA-256 fingerprint (formatted
// "sha256:AA:BB:.." uppercase) plus whether that certificate chains to a
// system-trusted root for the server's host. It powers trust-on-first-use
// fingerprint pinning in `pc auth login` for self-signed clusters. The
// InsecureSkipVerify here is deliberate: we hash and display the cert, we do
// not trust it — the caller decides whether to pin.
func ProbeServerCert(server string, timeout time.Duration) (fingerprint string, trusted bool, err error) {
	hostPort, hostname, err := dialTarget(server)
	if err != nil {
		return "", false, err
	}
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", hostPort, &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // intentional: cert is hashed+shown, not trusted
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		return "", false, err
	}
	defer conn.Close()

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", false, fmt.Errorf("server presented no certificate")
	}
	leaf := certs[0]
	sum := sha256.Sum256(leaf.Raw)
	fingerprint = "sha256:" + strings.ToUpper(colonize(hex.EncodeToString(sum[:])))

	// Best-effort trust check against the system roots, using any intermediates
	// the server sent. A self-signed Proxmox cert fails this (trusted=false),
	// which is exactly the case we want to offer pinning for.
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	if _, verr := leaf.Verify(x509.VerifyOptions{DNSName: hostname, Intermediates: inter}); verr == nil {
		trusted = true
	}
	return fingerprint, trusted, nil
}

// dialTarget turns an https URL or host[:port] into a "host:port" dial target
// (defaulting to port 443) and the bare hostname used for verification.
func dialTarget(server string) (hostPort, hostname string, err error) {
	s := strings.TrimSpace(server)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", "", fmt.Errorf("parse server url %q: %w", server, err)
	}
	if u.Hostname() == "" {
		return "", "", fmt.Errorf("no host in server url %q", server)
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port), u.Hostname(), nil
}

// normalizeFingerprint strips an optional "sha256:" prefix and all colons, and
// validates that the result is 64 hex chars (32 bytes).
func normalizeFingerprint(fp string) (string, error) {
	s := strings.TrimSpace(fp)
	s = strings.TrimPrefix(strings.ToLower(s), "sha256:")
	s = strings.ReplaceAll(s, ":", "")
	if len(s) != 64 {
		return "", fmt.Errorf("invalid sha256 fingerprint %q: expected 64 hex chars, got %d", fp, len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", fmt.Errorf("invalid sha256 fingerprint %q: %w", fp, err)
	}
	return s, nil
}

// colonize formats a hex string into colon-separated byte pairs for display.
func colonize(hexStr string) string {
	var b strings.Builder
	for i := 0; i < len(hexStr); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		end := i + 2
		if end > len(hexStr) {
			end = len(hexStr)
		}
		b.WriteString(hexStr[i:end])
	}
	return b.String()
}
