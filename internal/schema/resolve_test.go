package schema

import "testing"

// tinyAPI builds a small hand-made tree for deterministic resolver tests.
func tinyAPI() *API {
	version := &Node{Path: "/version", Text: "version", Leaf: true,
		Methods: map[string]*Endpoint{"GET": {Path: "/version", Method: "GET"}}}
	statusCurrent := &Node{Path: "/nodes/{node}/qemu/{vmid}/status/current", Text: "current", Leaf: true,
		Methods: map[string]*Endpoint{"GET": {Path: "/nodes/{node}/qemu/{vmid}/status/current", Method: "GET"}}}
	status := &Node{Path: "/nodes/{node}/qemu/{vmid}/status", Text: "status", Children: []*Node{statusCurrent}}
	vmid := &Node{Path: "/nodes/{node}/qemu/{vmid}", Text: "{vmid}", Children: []*Node{status}}
	qemu := &Node{Path: "/nodes/{node}/qemu", Text: "qemu", Children: []*Node{vmid},
		Methods: map[string]*Endpoint{"GET": {Path: "/nodes/{node}/qemu", Method: "GET"}}}
	node := &Node{Path: "/nodes/{node}", Text: "{node}", Children: []*Node{qemu}}
	nodes := &Node{Path: "/nodes", Text: "nodes", Children: []*Node{node}}
	return &API{Tree: []*Node{nodes, version}}
}

func TestResolveLeaf(t *testing.T) {
	api := tinyAPI()
	n, path, err := api.Resolve([]string{"version"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/version" {
		t.Errorf("path = %q, want /version", path)
	}
	if n.Methods["GET"] == nil {
		t.Error("version should have a GET method")
	}
}

func TestResolveParamSubstitution(t *testing.T) {
	api := tinyAPI()
	n, path, err := api.Resolve([]string{"nodes", "pve-01", "qemu", "100", "status", "current"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/nodes/pve-01/qemu/100/status/current" {
		t.Errorf("path = %q", path)
	}
	if n.Text != "current" {
		t.Errorf("resolved node = %q, want current", n.Text)
	}
}

func TestResolveUnknownSegment(t *testing.T) {
	api := tinyAPI()
	_, _, err := api.Resolve([]string{"nodes", "pve-01", "bogus"})
	re, ok := err.(*ResolveError)
	if !ok {
		t.Fatalf("err = %v, want *ResolveError", err)
	}
	if re.Bad != "bogus" {
		t.Errorf("bad segment = %q", re.Bad)
	}
}

func TestResolveEmptyListsRoots(t *testing.T) {
	api := tinyAPI()
	n, _, err := api.Resolve(nil)
	if err != nil || n != nil {
		t.Fatalf("empty resolve = (%v,%v), want (nil,nil)", n, err)
	}
	roots := api.Roots()
	if len(roots) != 2 || roots[0] != "nodes" || roots[1] != "version" {
		t.Errorf("roots = %v", roots)
	}
}
