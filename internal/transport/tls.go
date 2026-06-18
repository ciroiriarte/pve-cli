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
	"os"
	"strings"
)

// TLSConfig describes how the client validates the server certificate.
type TLSConfig struct {
	// CAFile is an optional PEM bundle to trust instead of the system pool.
	CAFile string
	// Fingerprint pins the server cert's SHA-256 fingerprint. Accepts the
	// colon-separated hex form, with or without a "sha256:" prefix.
	Fingerprint string
	// Insecure disables all verification. Opt-in footgun.
	Insecure bool
}

// build constructs a *tls.Config from the TLSConfig.
func (c TLSConfig) build() (*tls.Config, error) {
	out := &tls.Config{MinVersion: tls.VersionTLS12}

	if c.Insecure {
		out.InsecureSkipVerify = true
		return out, nil
	}

	if c.Fingerprint != "" {
		want, err := normalizeFingerprint(c.Fingerprint)
		if err != nil {
			return nil, err
		}
		// Pinning: do our own verification against the pinned fingerprint.
		out.InsecureSkipVerify = true
		out.VerifyPeerCertificate = pinVerifier(want)
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
