package transport

import "testing"

// New must refuse plaintext http to a non-loopback host (credentials would go
// over the wire in the clear) while allowing loopback http (tunnels/tests) and
// https everywhere.
func TestNewRejectsPlaintextHTTPToRemote(t *testing.T) {
	cases := []struct {
		url     string
		wantErr bool
	}{
		{"http://pve.example:8006", true},   // plaintext to a real host — refused
		{"http://10.2.0.210:8006", true},    // plaintext to a routable IP — refused
		{"https://pve.example:8006", false}, // https — fine
		{"http://127.0.0.1:8006", false},    // loopback http — allowed (tunnel/test)
		{"http://localhost:8006", false},    // loopback name — allowed
		{"http://[::1]:8006", false},        // IPv6 loopback — allowed
	}
	for _, c := range cases {
		_, err := New(Options{BaseURL: c.url})
		if c.wantErr && err == nil {
			t.Errorf("%s: expected an error (plaintext credential leak), got nil", c.url)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: expected no error, got %v", c.url, err)
		}
	}
}
