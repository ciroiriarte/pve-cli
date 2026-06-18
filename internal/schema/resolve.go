package schema

import (
	"fmt"
	"sort"
	"strings"
)

// ResolveError reports an unknown path segment during traversal, listing the
// valid alternatives at that point for discoverability.
type ResolveError struct {
	Bad   string
	Valid []string
}

func (e *ResolveError) Error() string {
	if len(e.Valid) == 0 {
		return fmt.Sprintf("unknown path segment %q", e.Bad)
	}
	return fmt.Sprintf("unknown path segment %q; valid here: %s", e.Bad, strings.Join(e.Valid, ", "))
}

// Roots returns the sorted top-level segment names.
func (a *API) Roots() []string {
	out := make([]string, 0, len(a.Tree))
	for _, r := range a.Tree {
		out = append(out, r.Text)
	}
	sort.Strings(out)
	return out
}

// Resolve walks segs through the schema tree. It returns the matched node and
// the concrete API path (with literal values substituted for {params}). With
// no segments it returns (nil, "", nil) so the caller can list Roots().
func (a *API) Resolve(segs []string) (*Node, string, error) {
	if len(segs) == 0 {
		return nil, "", nil
	}
	var cur *Node
	for _, r := range a.Tree {
		if r.Text == segs[0] {
			cur = r
			break
		}
	}
	if cur == nil {
		return nil, "", &ResolveError{Bad: segs[0], Valid: a.Roots()}
	}
	for _, seg := range segs[1:] {
		next := cur.Child(seg)
		if next == nil {
			return nil, "", &ResolveError{Bad: seg, Valid: ChildLabels(cur)}
		}
		cur = next
	}
	// The user types real values for {param} segments and real names for
	// literals, so the concrete path is just the joined segments.
	return cur, "/" + strings.Join(segs, "/"), nil
}

// ChildLabels returns the sorted child segment labels for discovery, rendering
// a {param} child as <param>.
func ChildLabels(n *Node) []string {
	out := make([]string, 0, len(n.Children))
	for _, c := range n.Children {
		if c.IsParam() {
			out = append(out, "<"+strings.Trim(c.Text, "{}")+">")
		} else {
			out = append(out, c.Text)
		}
	}
	sort.Strings(out)
	return out
}

// MethodNames returns the sorted HTTP methods available on a node.
func (n *Node) MethodNames() []string {
	out := make([]string, 0, len(n.Methods))
	for m := range n.Methods {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
