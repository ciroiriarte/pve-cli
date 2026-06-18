package pve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ciroiriarte/pve-cli/internal/auth"
	"github.com/ciroiriarte/pve-cli/internal/domain"
	"github.com/ciroiriarte/pve-cli/internal/provider"
	"github.com/ciroiriarte/pve-cli/internal/transport"
)

// fakeProxmox returns an httptest server emulating the subset of the PVE API
// the M1 commands use, asserting the token auth header along the way.
func fakeProxmox(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "PVEAPIToken=") {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[
			{"type":"node","node":"pve-01","status":"online","maxcpu":8,"maxmem":16000000000},
			{"type":"qemu","vmid":100,"name":"web","node":"pve-01","status":"running","maxmem":2147483648},
			{"type":"lxc","vmid":200,"name":"ct1","node":"pve-01","status":"stopped","maxmem":536870912}
		]}`))
	})

	mux.HandleFunc("/api2/json/nodes/pve-01/qemu/100/status/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.Write([]byte(`{"data":"UPID:pve-01:0001ABCD:0F1E2D3C:65000000:qmstart:100:root@pam:"}`))
	})

	mux.HandleFunc("/api2/json/nodes/pve-01/tasks/", func(w http.ResponseWriter, r *http.Request) {
		// .../tasks/<upid>/status
		w.Write([]byte(`{"data":{"upid":"UPID:pve-01:...","node":"pve-01","type":"qmstart","status":"stopped","exitstatus":"OK"}}`))
	})

	return httptest.NewServer(mux)
}

func newTestProvider(t *testing.T, baseURL string) *PVE {
	t.Helper()
	tok, _ := auth.NewToken("u@pam!c", "secret")
	cl, err := transport.New(transport.Options{BaseURL: baseURL, Auth: tok})
	if err != nil {
		t.Fatal(err)
	}
	return &PVE{cl: cl}
}

func TestListNodes(t *testing.T) {
	srv := fakeProxmox(t)
	defer srv.Close()
	p := newTestProvider(t, srv.URL)

	nodes, err := p.ListNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Name != "pve-01" {
		t.Fatalf("nodes = %+v", nodes)
	}
}

func TestListGuestsFiltersByKind(t *testing.T) {
	srv := fakeProxmox(t)
	defer srv.Close()
	p := newTestProvider(t, srv.URL)

	vms, err := p.ListGuests(context.Background(), provider.GuestFilter{Kind: domain.KindVM})
	if err != nil {
		t.Fatal(err)
	}
	if len(vms) != 1 || vms[0].VMID != 100 || vms[0].Kind != domain.KindVM {
		t.Fatalf("vms = %+v", vms)
	}
}

func TestResolveGuestAndPower(t *testing.T) {
	srv := fakeProxmox(t)
	defer srv.Close()
	p := newTestProvider(t, srv.URL)

	g, err := p.ResolveGuest(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if g.Node != "pve-01" || g.Kind != domain.KindVM {
		t.Fatalf("resolved = %+v", g)
	}

	h, err := p.GuestPower(context.Background(), g, "start")
	if err != nil {
		t.Fatal(err)
	}
	if h.Node != "pve-01" || !strings.HasPrefix(h.UPID, "UPID:") {
		t.Fatalf("handle = %+v", h)
	}

	st, err := p.TaskStatus(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if !st.OK() {
		t.Fatalf("task not OK: %+v", st)
	}
}

func TestResolveGuestNotFound(t *testing.T) {
	srv := fakeProxmox(t)
	defer srv.Close()
	p := newTestProvider(t, srv.URL)

	if _, err := p.ResolveGuest(context.Background(), 999); err == nil {
		t.Fatal("expected not-found error")
	}
}
