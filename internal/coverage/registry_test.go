package coverage

import "testing"

func TestClassifyKnownEndpoints(t *testing.T) {
	curated := []struct{ m, p string }{
		{"POST", "/nodes/{node}/qemu/{vmid}/clone"},
		{"DELETE", "/nodes/{node}/lxc/{vmid}"},
		{"POST", "/nodes/{node}/qemu/{vmid}/status/start"},
		{"PUT", "/nodes/{node}/qemu/{vmid}/config"},
		{"GET", "/version"},
		{"GET", "/remotes/remote"},
	}
	for _, c := range curated {
		if _, ok := Classify(c.m, c.p); !ok {
			t.Errorf("expected curated: %s %s", c.m, c.p)
		}
	}

	rawOnly := []struct{ m, p string }{
		{"GET", "/access/users"},
		{"POST", "/nodes/{node}/qemu/{vmid}/vncwebsocket"},
		{"GET", "/cluster/ha/resources"},
		{"POST", "/nodes/{node}/qemu/{vmid}/resize"},
	}
	for _, c := range rawOnly {
		if cmd, ok := Classify(c.m, c.p); ok {
			t.Errorf("expected raw-only, but %s %s is mapped to %q", c.m, c.p, cmd)
		}
	}
}
