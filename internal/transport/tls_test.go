package transport

import "testing"

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
