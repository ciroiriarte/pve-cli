package pdm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ciroiriarte/pve-cli/internal/auth"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
	"github.com/ciroiriarte/pve-cli/internal/transport"
)

// #37: GuestsForResolve fails closed on incomplete inventory.
func TestGuestsForResolve(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[
			{"remote":"DC-East","error":"connection refused","resources":[]},
			{"remote":"dc-south","error":"timeout","resources":[]},
			{"remote":"dc-west","resources":[{"type":"pve-qemu","vmid":100,"name":"w","node":"n3"}]}
		]}`))
	}))
	defer srv.Close()
	tok, _ := auth.NewToken("u@pam!c", "secret")
	cl, err := transport.New(transport.Options{BaseURL: srv.URL, Auth: tok})
	if err != nil {
		t.Fatal(err)
	}
	p := &PDM{cl: cl}
	ctx := context.Background()

	// Unscoped: refused, naming every failed remote, as a conflict.
	_, err = p.GuestsForResolve(ctx, provider.GuestFilter{}, "")
	var apiErr *protocol.APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != protocol.KindConflict {
		t.Fatalf("unscoped: want KindConflict, got %v", err)
	}
	for _, want := range []string{"DC-East (connection refused)", "dc-south (timeout)", "--remote"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("unscoped message missing %q: %v", want, err)
		}
	}

	// Scoped to a failed remote, matched case-insensitively.
	_, err = p.GuestsForResolve(ctx, provider.GuestFilter{}, "dc-east")
	if !errors.As(err, &apiErr) || apiErr.Kind != protocol.KindServer || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("scoped to failed remote: want KindServer naming the error, got %v", err)
	}

	// Scoped to a healthy remote: the other failures don't matter.
	gs, err := p.GuestsForResolve(ctx, provider.GuestFilter{}, "dc-west")
	if err != nil || len(gs) != 1 || gs[0].VMID != 100 {
		t.Fatalf("scoped to healthy remote: gs=%v err=%v", gs, err)
	}
}
