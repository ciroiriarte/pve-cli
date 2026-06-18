// Package schema is the intermediate representation (IR) of the Proxmox API
// schema. It parses the upstream apiSchema tree into a flat, navigable model
// the CLI uses to drive the generated `pc raw` command and golden/diff tests.
//
// The IR is the firewall between the upstream schema's shape and the CLI: the
// rest of the code depends on these types, never on the raw apidoc.js JSON.
package schema

import "sort"

// API is a parsed Proxmox API schema: both a flat endpoint list (sorted by
// path then method) and the original navigable tree.
type API struct {
	Meta      Meta        `json:"meta"`
	Endpoints []*Endpoint `json:"endpoints"`
	Tree      []*Node     `json:"-"`
}

// Meta records provenance of a snapshot.
type Meta struct {
	Source  string `json:"source"`
	Fetched string `json:"fetched"`
	Version string `json:"version"`
}

// Endpoint is one (path, method) operation.
type Endpoint struct {
	Path        string   `json:"path"`
	Method      string   `json:"method"` // GET/POST/PUT/DELETE
	Description string   `json:"description,omitempty"`
	Parameters  []*Param `json:"parameters,omitempty"`
	AllowToken  bool     `json:"allowtoken"`
}

// Param is a request parameter.
type Param struct {
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty"`
	Description string   `json:"description,omitempty"`
	Optional    bool     `json:"optional"`
	Default     string   `json:"default,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// Node is a tree node mirroring the upstream structure, used for `pc raw`
// path traversal and discovery.
type Node struct {
	Path     string  `json:"path"`
	Text     string  `json:"text"` // segment label; "{param}" for variables
	Leaf     bool    `json:"leaf"`
	Children []*Node `json:"children,omitempty"`
	Methods  map[string]*Endpoint
}

// IsParam reports whether this node's segment is a path variable like {node}.
func (n *Node) IsParam() bool {
	return len(n.Text) >= 2 && n.Text[0] == '{' && n.Text[len(n.Text)-1] == '}'
}

// Child returns the child whose segment matches seg. A literal match wins; if
// none, a single {param} child (if any) matches any value.
func (n *Node) Child(seg string) *Node {
	var param *Node
	for _, c := range n.Children {
		if c.Text == seg {
			return c
		}
		if c.IsParam() {
			param = c
		}
	}
	return param
}

// SortEndpoints orders endpoints by path then method for stable output.
func SortEndpoints(eps []*Endpoint) {
	sort.Slice(eps, func(i, j int) bool {
		if eps[i].Path != eps[j].Path {
			return eps[i].Path < eps[j].Path
		}
		return eps[i].Method < eps[j].Method
	})
}

// RequiredParams returns the endpoint's required parameter names, sorted. A
// change to this set is the signal both Signature and Diff treat as breaking.
func (e *Endpoint) RequiredParams() []string {
	var names []string
	for _, p := range e.Parameters {
		if !p.Optional {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names
}

// Find returns the endpoint for an exact path+method, or nil.
func (a *API) Find(path, method string) *Endpoint {
	for _, e := range a.Endpoints {
		if e.Path == path && e.Method == method {
			return e
		}
	}
	return nil
}
