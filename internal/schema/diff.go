package schema

import (
	"fmt"
	"sort"
	"strings"
)

// Difference is the result of comparing two API snapshots.
type Difference struct {
	Added   []string // "METHOD /path" present only in new
	Removed []string // "METHOD /path" present only in old (breaking)
	Changed []string // parameter set changed (potentially breaking)
}

// HasBreaking reports whether the diff contains backward-incompatible changes.
func (d Difference) HasBreaking() bool {
	return len(d.Removed) > 0 || len(d.Changed) > 0
}

// String renders the diff as a human-readable report.
func (d Difference) String() string {
	var b strings.Builder
	report := func(title string, items []string) {
		fmt.Fprintf(&b, "%s (%d):\n", title, len(items))
		for _, it := range items {
			fmt.Fprintf(&b, "  %s\n", it)
		}
	}
	report("Added", d.Added)
	report("Removed", d.Removed)
	report("Changed", d.Changed)
	if !d.HasBreaking() {
		b.WriteString("no breaking changes\n")
	}
	return b.String()
}

// Diff compares two APIs and reports added/removed/changed endpoints.
func Diff(oldAPI, newAPI *API) Difference {
	oldEps := index(oldAPI)
	newEps := index(newAPI)

	var d Difference
	for key, ne := range newEps {
		oe, ok := oldEps[key]
		if !ok {
			d.Added = append(d.Added, key)
			continue
		}
		if !sameParams(oe, ne) {
			d.Changed = append(d.Changed, key)
		}
	}
	for key := range oldEps {
		if _, ok := newEps[key]; !ok {
			d.Removed = append(d.Removed, key)
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	sort.Strings(d.Changed)
	return d
}

func index(a *API) map[string]*Endpoint {
	m := make(map[string]*Endpoint, len(a.Endpoints))
	for _, e := range a.Endpoints {
		m[e.Method+" "+e.Path] = e
	}
	return m
}

// sameParams compares the required-parameter sets of two endpoints. A change in
// required parameters is the signal we treat as potentially breaking.
func sameParams(a, b *Endpoint) bool {
	return strings.Join(a.RequiredParams(), ",") == strings.Join(b.RequiredParams(), ",")
}
