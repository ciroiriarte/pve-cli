package pdm

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ciroiriarte/pve-cli/internal/auth"
	"github.com/ciroiriarte/pve-cli/internal/provider"
	"github.com/ciroiriarte/pve-cli/internal/transport"
)

// Lists keep warning about remotes whose inventory failed (#37), to the
// configured warn writer (os.Stderr unless SetWarnWriter was called).
func TestListWarnsFailedRemoteToWriter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[
			{"remote":"dc-down","error":"connection refused","resources":[]},
			{"remote":"dc-west","resources":[{"type":"pve-qemu","vmid":100,"name":"w","node":"n3"},{"type":"pve-node","node":"n3","status":"online"}]}
		]}`))
	}))
	defer srv.Close()
	tok, _ := auth.NewToken("u@pam!c", "secret")
	cl, err := transport.New(transport.Options{BaseURL: srv.URL, Auth: tok})
	if err != nil {
		t.Fatal(err)
	}
	p := &PDM{cl: cl}
	var buf bytes.Buffer
	p.SetWarnWriter(&buf)

	gs, err := p.ListGuests(context.Background(), provider.GuestFilter{})
	if err != nil || len(gs) != 1 {
		t.Fatalf("ListGuests = %v, %v", gs, err)
	}
	if !strings.Contains(buf.String(), "inventory for remote dc-down unavailable: connection refused") {
		t.Errorf("ListGuests warning = %q", buf.String())
	}
	buf.Reset()
	if _, err := p.ListNodes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "dc-down") {
		t.Errorf("ListNodes warning = %q", buf.String())
	}
}
