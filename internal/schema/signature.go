package schema

import (
	"sort"
	"strings"
)

// Signature returns a stable, line-per-endpoint serialization of the API:
//
//	METHOD /path [req1,req2]
//
// where the bracketed list is the sorted required-parameter names. It is the
// basis for golden snapshot tests that guard against unreviewed schema drift.
func (a *API) Signature() string {
	lines := make([]string, 0, len(a.Endpoints))
	for _, e := range a.Endpoints {
		lines = append(lines, e.Method+" "+e.Path+" ["+strings.Join(e.RequiredParams(), ",")+"]")
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}
