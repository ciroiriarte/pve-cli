package cli

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// setToValues parses repeated "key=value" flags into url.Values.
func setToValues(set []string) (url.Values, error) {
	v := url.Values{}
	for _, kv := range set {
		k, val, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --set %q: expected key=value", kv)
		}
		v.Set(k, val)
	}
	return v, nil
}

// pdmProvider returns the active provider, requiring it to be PDM (these
// command groups wrap PDM-native control-plane endpoints).
func (a *app) pdmProvider() (provider.Provider, error) {
	p, err := a.Provider()
	if err != nil {
		return nil, err
	}
	if !p.Capabilities().Remotes {
		return nil, fmt.Errorf("this command requires the PDM provider (current provider: %s); set provider: pdm in your profile", p.Name())
	}
	return p, nil
}

// kvTable renders a JSON object as a sorted key/value table.
func kvTable(m map[string]any) output.Tabular {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	t := output.Tabular{Columns: []string{"key", "value"}, Raw: m}
	for _, k := range keys {
		t.Rows = append(t.Rows, []string{k, fmt.Sprintf("%v", m[k])})
	}
	return t
}

// pdmGet GETs a path on the PDM provider and renders an array (mapsTable) or a
// single object (kvTable). cols orders the leading columns for array output.
func (a *app) pdmGet(cmd *cobra.Command, path string, cols ...string) error {
	p, err := a.pdmProvider()
	if err != nil {
		return err
	}
	return a.renderGet(cmd, p, path, cols...)
}

// renderGet GETs a path on the given provider and renders an array (mapsTable),
// a single object (kvTable), or a scalar — provider-agnostic.
func (a *app) renderGet(cmd *cobra.Command, p provider.Provider, path string, cols ...string) error {
	body, err := p.Raw(cmd.Context(), "GET", path, nil)
	if err != nil {
		return err
	}
	var arr []map[string]any
	if err := protocol.DecodeData(body, &arr); err == nil {
		return a.render(mapsTable(arr, cols...))
	}
	var obj map[string]any
	if err := protocol.DecodeData(body, &obj); err == nil {
		return a.render(kvTable(obj))
	}
	// Scalar (e.g. next free vmid) or array-of-scalars: unwrap the envelope's
	// data and render its string form.
	var scalar any
	if err := protocol.DecodeData(body, &scalar); err == nil {
		return a.render(output.Tabular{Columns: []string{"value"}, Rows: [][]string{{fmt.Sprintf("%v", scalar)}}, Raw: scalar})
	}
	return a.render(output.Tabular{Columns: []string{"value"}, Rows: [][]string{{string(body)}}})
}

// simpleGet builds a cobra command that GETs a fixed or arg-derived path on the
// PDM provider (gated). pathFn receives the positional args and returns the path.
func simpleGet(a *app, use, short string, nargs int, pathFn func(args []string) string, cols ...string) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(nargs),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.pdmGet(cmd, pathFn(args), cols...)
		},
	}
}

// anyGet is like simpleGet but provider-agnostic (works on whichever backend is
// active), for endpoints that exist on both PVE and PDM (e.g. /access/*).
func anyGet(a *app, use, short string, nargs int, pathFn func(args []string) string, cols ...string) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(nargs),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			return a.renderGet(cmd, p, pathFn(args), cols...)
		},
	}
}

// newCephCmd inspects Ceph on PDM-managed clusters (all read-only).
func newCephCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "ceph", Short: "Inspect (PDM) and manage (PVE, --node) Ceph"}
	cmd.AddCommand(
		simpleGet(a, "clusters", "List Ceph clusters", 0, func([]string) string { return "/ceph/clusters" }, "cluster", "name"),
		simpleGet(a, "show <cluster>", "Show a Ceph cluster", 1, func(a []string) string { return "/ceph/clusters/" + a[0] }),
		simpleGet(a, "status <cluster>", "Ceph cluster status", 1, func(a []string) string { return "/ceph/clusters/" + a[0] + "/status" }),
		simpleGet(a, "summary <cluster>", "Ceph cluster summary", 1, func(a []string) string { return "/ceph/clusters/" + a[0] + "/summary" }),
		simpleGet(a, "pools <cluster>", "List Ceph pools", 1, func(a []string) string { return "/ceph/clusters/" + a[0] + "/pools" }, "pool_name", "size", "min_size"),
		simpleGet(a, "osd-tree <cluster>", "Ceph OSD tree", 1, func(a []string) string { return "/ceph/clusters/" + a[0] + "/osd-tree" }),
		simpleGet(a, "mon <cluster>", "List Ceph monitors", 1, func(a []string) string { return "/ceph/clusters/" + a[0] + "/mon" }, "name", "host", "state"),
		simpleGet(a, "mgr <cluster>", "List Ceph managers", 1, func(a []string) string { return "/ceph/clusters/" + a[0] + "/mgr" }, "name", "state"),
		simpleGet(a, "mds <cluster>", "List Ceph metadata servers", 1, func(a []string) string { return "/ceph/clusters/" + a[0] + "/mds" }, "name", "state"),
		simpleGet(a, "fs <cluster>", "List CephFS filesystems", 1, func(a []string) string { return "/ceph/clusters/" + a[0] + "/fs" }, "name"),
		simpleGet(a, "flags <cluster>", "Show Ceph flags", 1, func(a []string) string { return "/ceph/clusters/" + a[0] + "/flags" }),
	)
	// PVE node-scoped Ceph management (osd/pool/service/config/health, --node).
	cmd.AddCommand(newCephMgmtCmds(a)...)
	return cmd
}

// newResourcesCmd surfaces PDM's aggregate resource views.
func newResourcesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "resources", Aliases: []string{"res"}, Short: "PDM aggregate resource views (read-only)"}
	cmd.AddCommand(
		simpleGet(a, "status", "Cluster-wide resource totals", 0, func([]string) string { return "/resources/status" }),
		simpleGet(a, "top-entities", "Top resource consumers", 0, func([]string) string { return "/resources/top-entities" }),
		simpleGet(a, "subscription", "Aggregate subscription status", 0, func([]string) string { return "/resources/subscription" }),
		simpleGet(a, "location-info", "Resource location info", 0, func([]string) string { return "/resources/location-info" }),
	)
	return cmd
}
