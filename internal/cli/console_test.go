package cli

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
)

// fakeConn scripts ReadMessage outputs and records WriteMessage payloads. After
// the scripted reads are exhausted it either returns EOF or blocks (so a test
// can isolate the input or the output direction without a race).
type fakeConn struct {
	mu       sync.Mutex
	reads    [][]byte
	ri       int
	writes   []string
	blockEOF bool
	blockCh  chan struct{}
}

func (f *fakeConn) ReadMessage() (int, []byte, error) {
	f.mu.Lock()
	if f.ri < len(f.reads) {
		m := f.reads[f.ri]
		f.ri++
		f.mu.Unlock()
		return 1, m, nil
	}
	block := f.blockEOF
	f.mu.Unlock()
	if block {
		<-f.blockCh // block until the test ends
	}
	return 0, nil, io.EOF
}
func (f *fakeConn) WriteMessage(_ int, data []byte) error {
	f.mu.Lock()
	f.writes = append(f.writes, string(data))
	f.mu.Unlock()
	return nil
}
func (f *fakeConn) Close() error { return nil }

// TestRunConsoleInputFraming: the reader blocks, so runConsole returns only when
// the quit byte is read from input — deterministically exercising the framing.
func TestRunConsoleInputFraming(t *testing.T) {
	conn := &fakeConn{reads: [][]byte{[]byte("OK")}, blockEOF: true, blockCh: make(chan struct{})}
	defer close(conn.blockCh)
	in := bytes.NewReader([]byte{'h', 'i', consoleQuit})

	if err := runConsole(conn, "root@pam", "PVE:tkt", in, io.Discard, 80, 24); err != nil {
		t.Fatalf("runConsole: %v", err)
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.writes) == 0 || conn.writes[0] != "root@pam:PVE:tkt\n" {
		t.Fatalf("first write must be the auth frame; got %v", conn.writes)
	}
	j := strings.Join(conn.writes, "|")
	if !strings.Contains(j, "2:80:24:") {
		t.Errorf("expected resize frame; writes=%v", conn.writes)
	}
	if !strings.Contains(j, "0:2:hi") {
		t.Errorf("expected input framed as 0:2:hi; writes=%v", conn.writes)
	}
}

// TestRunConsoleOutputBridge: input blocks, so runConsole returns only on the
// reader's EOF after the server output is written — deterministic output check.
func TestRunConsoleOutputBridge(t *testing.T) {
	conn := &fakeConn{reads: [][]byte{[]byte("OK"), []byte("hello-from-server")}}
	pr, pw := io.Pipe()
	defer pw.Close()
	var out bytes.Buffer

	if err := runConsole(conn, "u", "t", pr, &out, 0, 0); err != nil {
		t.Fatalf("runConsole: %v", err)
	}
	if !strings.Contains(out.String(), "hello-from-server") {
		t.Errorf("server output not bridged; got %q", out.String())
	}
}

func TestRunConsoleRejectsBadAuth(t *testing.T) {
	conn := &fakeConn{reads: [][]byte{[]byte("authentication failure")}}
	err := runConsole(conn, "u", "t", bytes.NewReader(nil), io.Discard, 0, 0)
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected auth rejection, got %v", err)
	}
}
