package protocol

import (
	"strings"
	"testing"
)

func TestParseUPID(t *testing.T) {
	upid := "UPID:pve-01:0001ABCD:0F1E2D3C:65000000:qmstart:100:root@pam:"
	h, err := ParseUPID(upid)
	if err != nil {
		t.Fatalf("ParseUPID: %v", err)
	}
	if h.Node != "pve-01" {
		t.Errorf("node = %q, want pve-01", h.Node)
	}
	if h.UPID != upid {
		t.Errorf("upid round-trip mismatch")
	}
	if h.Display != "qmstart 100" {
		t.Errorf("display = %q, want %q", h.Display, "qmstart 100")
	}
}

func TestParseUPIDRejectsGarbage(t *testing.T) {
	if _, err := ParseUPID("not-a-upid"); err == nil {
		t.Fatal("expected error for non-UPID input")
	}
	if _, err := ParseUPID("UPID:too:few"); err == nil {
		t.Fatal("expected error for malformed UPID")
	}
}

func TestDecodeErrorTranslatesLock(t *testing.T) {
	e := DecodeError(500, []byte("VM 100 is locked (backup)"))
	if e.Kind != KindConflict {
		t.Errorf("kind = %v, want KindConflict", e.Kind)
	}
	want := "VM 100 is locked by an active 'backup' task"
	if e.Message != want {
		t.Errorf("message = %q, want %q", e.Message, want)
	}
	if e.RawBody != "VM 100 is locked (backup)" {
		t.Errorf("raw body not preserved: %q", e.RawBody)
	}
}

func TestDecodeErrorStatusMapping(t *testing.T) {
	cases := []struct {
		code int
		want Kind
	}{
		{401, KindAuth},
		{403, KindPermission},
		{404, KindNotFound},
		{400, KindValidation},
		{409, KindConflict},
		{500, KindServer},
	}
	for _, c := range cases {
		if got := DecodeError(c.code, []byte("oops")).Kind; got != c.want {
			t.Errorf("status %d: kind = %v, want %v", c.code, got, c.want)
		}
	}
}

func TestDecodeErrorExtractsEnvelopeMessage(t *testing.T) {
	// Proxmox 500s carry a top-level "message"; it must not render blank.
	body := []byte(`{"data":null,"message":"Configuration file 'nodes/bigiron/qemu-server/9999.conf' does not exist\n"}`)
	e := DecodeError(500, body)
	if e.Message == "" {
		t.Fatal("message should be extracted from envelope, got empty")
	}
	if !strings.Contains(e.Message, "does not exist") {
		t.Errorf("message = %q, want it to contain 'does not exist'", e.Message)
	}
	if strings.Contains(e.Message, "\n") {
		t.Errorf("message should be trimmed to a single line, got %q", e.Message)
	}
}

func TestDecodeData(t *testing.T) {
	var out []map[string]any
	if err := DecodeData([]byte(`{"data":[{"vmid":100},{"vmid":101}]}`), &out); err != nil {
		t.Fatalf("DecodeData: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
}

func TestTaskStatusOK(t *testing.T) {
	if !(TaskStatus{Status: "stopped", ExitStatus: "OK"}).OK() {
		t.Error("expected OK")
	}
	if (TaskStatus{Status: "stopped", ExitStatus: "boom"}).OK() {
		t.Error("expected not OK")
	}
	if (TaskStatus{Status: "running"}).Done() {
		t.Error("running task should not be Done")
	}
}
