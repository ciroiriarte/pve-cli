package schema

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// sortParams orders parameters with required ones first, then alphabetically.
func sortParams(ps []*Param) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Optional != ps[j].Optional {
			return !ps[i].Optional // required (Optional=false) first
		}
		return ps[i].Name < ps[j].Name
	})
}

// rawNode mirrors the upstream apidoc.js tree node shape.
type rawNode struct {
	Path     string                  `json:"path"`
	Text     string                  `json:"text"`
	Leaf     flexInt                 `json:"leaf"`
	Children []*rawNode              `json:"children"`
	Info     map[string]*rawEndpoint `json:"info"`
}

type rawEndpoint struct {
	Method      string         `json:"method"`
	Description string         `json:"description"`
	AllowToken  flexInt        `json:"allowtoken"`
	Parameters  *rawParameters `json:"parameters"`
}

type rawParameters struct {
	Properties map[string]*rawParam `json:"properties"`
	// AllOf composes the parameter object from several property sets (PBS
	// uses it heavily, e.g. prune = backup group + keep options); the members
	// are merged into one flat parameter list.
	AllOf []*rawParameters `json:"allOf"`
}

// flatten merges Properties with those of every (nested) allOf member.
func (rp *rawParameters) flatten(into map[string]*rawParam) {
	if rp == nil {
		return
	}
	for name, p := range rp.Properties {
		into[name] = p
	}
	for _, sub := range rp.AllOf {
		sub.flatten(into)
	}
}

type rawParam struct {
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Optional    flexInt         `json:"optional"`
	Default     json.RawMessage `json:"default"`
	Enum        flexStrings     `json:"enum"`
}

// Parse converts the upstream apiSchema tree (a JSON array of nodes) into an
// API IR with a flat, sorted endpoint list and the navigable tree.
func Parse(treeJSON []byte) (*API, error) {
	var roots []*rawNode
	if err := json.Unmarshal(treeJSON, &roots); err != nil {
		return nil, fmt.Errorf("parse api schema tree: %w", err)
	}
	api := &API{}
	for _, r := range roots {
		// PBS documents its HTTP/2 backup/restore protocols as extra roots
		// (/backup/_upgrade_, /reader/_upgrade_). They are only reachable after
		// a protocol upgrade, never via the REST API, so they're left out.
		if strings.Contains(r.Path, "/_upgrade_") {
			continue
		}
		node := convert(r, api)
		api.Tree = append(api.Tree, node)
	}
	// PDM wraps everything under a single "/" root; flatten it so top-level
	// segments (access, pve, remotes, resources, …) traverse like PVE's roots.
	if len(api.Tree) == 1 && api.Tree[0].Path == "/" {
		api.Tree = api.Tree[0].Children
	}
	SortEndpoints(api.Endpoints)
	return api, nil
}

// convert recursively builds an IR Node, appending endpoints to api.Endpoints.
func convert(r *rawNode, api *API) *Node {
	n := &Node{
		Path: r.Path,
		Text: r.Text,
		Leaf: r.Leaf == 1,
	}
	if len(r.Info) > 0 {
		n.Methods = make(map[string]*Endpoint, len(r.Info))
		for method, re := range r.Info {
			ep := &Endpoint{
				Path:        r.Path,
				Method:      method,
				Description: re.Description,
				AllowToken:  re.AllowToken == 1,
				Parameters:  convertParams(re.Parameters),
			}
			n.Methods[method] = ep
			api.Endpoints = append(api.Endpoints, ep)
		}
	}
	for _, c := range r.Children {
		n.Children = append(n.Children, convert(c, api))
	}
	return n
}

func convertParams(rp *rawParameters) []*Param {
	props := map[string]*rawParam{}
	rp.flatten(props)
	if len(props) == 0 {
		return nil
	}
	out := make([]*Param, 0, len(props))
	for name, p := range props {
		out = append(out, &Param{
			Name:        name,
			Type:        p.Type,
			Description: p.Description,
			Optional:    p.Optional == 1,
			Default:     decodeDefault(p.Default),
			Enum:        []string(p.Enum),
		})
	}
	// Stable order: required first, then alphabetical.
	sortParams(out)
	return out
}

// decodeDefault renders a JSON default value as a string (defaults can be
// strings, numbers, or booleans in the upstream schema).
func decodeDefault(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}
