package cli

import (
	"fmt"
	"net/url"
	"sort"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/domain"
	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// newRemoteCmd manages PDM remotes. It is always registered but refuses to run
// against a provider that does not support remotes (i.e. direct PVE).
func newRemoteCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Manage PDM remotes (clusters); requires the pdm provider",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use: "list", Short: "List clusters managed by PDM", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				p, err := a.providerWithRemotes()
				if err != nil {
					return err
				}
				remotes, err := p.ListRemotes(cmd.Context())
				if err != nil {
					return err
				}
				sort.Slice(remotes, func(i, j int) bool { return remotes[i].ID < remotes[j].ID })
				return a.render(remotesTable(remotes))
			},
		},
		&cobra.Command{
			Use: "show <id>", Short: "Show a remote", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				p, err := a.providerWithRemotes()
				if err != nil {
					return err
				}
				remotes, err := p.ListRemotes(cmd.Context())
				if err != nil {
					return err
				}
				for _, r := range remotes {
					if r.ID == args[0] {
						return a.render(remotesTable([]domain.Remote{r}))
					}
				}
				return fmt.Errorf("remote %q not found", args[0])
			},
		},
		newRemoteAddCmd(a),
		newRemoteUpdateCmd(a),
		newRemoteRemoveCmd(a),
		newRemoteClusterStatusCmd(a),
		newRemoteUpdatesCmd(a),
		// per-remote PVE reads (proxied)
		simpleGet(a, "resources <id>", "List a remote's cluster resources", 1,
			func(a []string) string { return "/pve/remotes/" + a[0] + "/resources" }, "type", "id", "node", "status"),
		simpleGet(a, "options <id>", "Show a remote's datacenter options", 1,
			func(a []string) string { return "/pve/remotes/" + a[0] + "/options" }),
		simpleGet(a, "next-id <id>", "Get the next free VMID on a remote", 1,
			func(a []string) string { return "/pve/remotes/" + a[0] + "/cluster-nextid" }),
		simpleGet(a, "updates-list <id>", "List pending node updates on a remote", 1,
			func(a []string) string { return "/pve/remotes/" + a[0] + "/updates" }, "node", "status"),
		simpleGet(a, "firewall <id>", "Show a remote's datacenter firewall rules", 1,
			func(a []string) string { return "/pve/remotes/" + a[0] + "/firewall/rules" }, "pos", "type", "action", "proto", "dport"),
		simpleGet(a, "node-storage <id> <node>", "List storages on a remote node", 2,
			func(a []string) string { return "/pve/remotes/" + a[0] + "/nodes/" + a[1] + "/storage" }, "storage", "type", "content", "enabled"),
		simpleGet(a, "node-status <id> <node>", "Show a remote node's status", 2,
			func(a []string) string { return "/pve/remotes/" + a[0] + "/nodes/" + a[1] + "/status" }),
		simpleGet(a, "node-network <id> <node>", "List a remote node's network interfaces", 2,
			func(a []string) string { return "/pve/remotes/" + a[0] + "/nodes/" + a[1] + "/network" }, "iface", "type", "method", "address"),
	)
	return cmd
}

func newRemoteAddCmd(a *app) *cobra.Command {
	var typ, authID, token, createToken, webURL string
	var nodes []string
	cmd := &cobra.Command{
		Use:   "add <id>",
		Short: "Register a new cluster (remote) with PDM",
		Example: "  pc remote add dc-west --type pve --auth-id 'root@pam!pdm' --token SECRET \\\n" +
			"    --node 'pve1.example:8006,fingerprint=AB:CD:..'",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.providerWithRemotes()
			if err != nil {
				return err
			}
			if typ == "" || authID == "" || token == "" || len(nodes) == 0 {
				return fmt.Errorf("--type, --auth-id, --token and at least one --node are required")
			}
			params := url.Values{"id": {args[0]}, "type": {typ}, "authid": {authID}, "token": {token}}
			params["nodes"] = nodes
			if createToken != "" {
				params.Set("create-token", createToken)
			}
			if webURL != "" {
				params.Set("web-url", webURL)
			}
			return rawMutate(cmd.Context(), a, p, "POST", "/remotes/remote", params,
				fmt.Sprintf("add remote %s", args[0]), true, 0)
		},
	}
	cmd.Flags().StringVar(&typ, "type", "pve", "remote type: pve|pbs")
	cmd.Flags().StringVar(&authID, "auth-id", "", "API token id or user on the remote (e.g. root@pam!pdm)")
	cmd.Flags().StringVar(&token, "token", "", "API token secret / password")
	cmd.Flags().StringArrayVar(&nodes, "node", nil, "remote node 'host:port,fingerprint=..' (repeatable)")
	cmd.Flags().StringVar(&createToken, "create-token", "", "create-token name (optional)")
	cmd.Flags().StringVar(&webURL, "web-url", "", "web UI URL (optional)")
	return cmd
}

func newRemoteUpdateCmd(a *app) *cobra.Command {
	var authID, token, webURL string
	var nodes, del []string
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a registered remote",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.providerWithRemotes()
			if err != nil {
				return err
			}
			params := url.Values{}
			if authID != "" {
				params.Set("authid", authID)
			}
			if token != "" {
				params.Set("token", token)
			}
			if webURL != "" {
				params.Set("web-url", webURL)
			}
			if len(nodes) > 0 {
				params["nodes"] = nodes
			}
			if len(del) > 0 {
				params["delete"] = del
			}
			if len(params) == 0 {
				return fmt.Errorf("nothing to update: pass --auth-id/--token/--node/--web-url/--delete")
			}
			return rawMutate(cmd.Context(), a, p, "PUT", "/remotes/remote/"+args[0], params,
				fmt.Sprintf("update remote %s", args[0]), true, 0)
		},
	}
	cmd.Flags().StringVar(&authID, "auth-id", "", "API token id or user")
	cmd.Flags().StringVar(&token, "token", "", "API token secret / password")
	cmd.Flags().StringArrayVar(&nodes, "node", nil, "replace node list (repeatable)")
	cmd.Flags().StringArrayVar(&del, "delete", nil, "config key to unset (repeatable)")
	cmd.Flags().StringVar(&webURL, "web-url", "", "web UI URL")
	return cmd
}

func newRemoteRemoveCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "remove <id>", Aliases: []string{"rm", "delete"},
		Short: "Remove a registered remote from PDM", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.providerWithRemotes()
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("remove remote %q from PDM?", args[0])); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", "/remotes/remote/"+args[0], nil,
				fmt.Sprintf("remove remote %s", args[0]), true, 0)
		},
	}
}

func newRemoteClusterStatusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "cluster-status <id>", Short: "Show a remote cluster's node/quorum status", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.providerWithRemotes()
			if err != nil {
				return err
			}
			body, err := p.Raw(cmd.Context(), "GET", "/pve/remotes/"+args[0]+"/cluster-status", nil)
			if err != nil {
				return err
			}
			var rows []map[string]any
			if err := protocol.DecodeData(body, &rows); err != nil {
				return err
			}
			return a.render(mapsTable(rows, "name", "type", "online", "quorate", "nodes", "ip", "level"))
		},
	}
}

func newRemoteUpdatesCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "updates", Short: "Summarize pending package updates across remotes", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.providerWithRemotes()
			if err != nil {
				return err
			}
			body, err := p.Raw(cmd.Context(), "GET", "/remotes/updates/summary", nil)
			if err != nil {
				return err
			}
			// The summary is nested: remote -> {nodes: {node -> {...}}}. Flatten
			// to one row per node so the table is useful.
			var summary struct {
				Remotes map[string]any `json:"remotes"`
			}
			if err := protocol.DecodeData(body, &summary); err != nil {
				return err
			}
			rows := []map[string]any{}
			for remoteID, rv := range summary.Remotes {
				rm, _ := rv.(map[string]any)
				nodes, _ := rm["nodes"].(map[string]any)
				if len(nodes) == 0 {
					rows = append(rows, map[string]any{"remote": remoteID, "status": rm["status"]})
					continue
				}
				for node, nv := range nodes {
					nm, _ := nv.(map[string]any)
					ver := ""
					if vs, ok := nm["versions"].([]any); ok {
						for _, v := range vs {
							if vm, ok := v.(map[string]any); ok && vm["package"] == "pve-manager" {
								ver = fmt.Sprintf("%v", vm["version"])
							}
						}
					}
					rows = append(rows, map[string]any{
						"remote": remoteID, "node": node,
						"updates": nm["number-of-updates"], "repo-status": nm["repository-status"],
						"version": ver, "status": nm["status"],
					})
				}
			}
			sort.Slice(rows, func(i, j int) bool {
				return fmt.Sprintf("%v%v", rows[i]["remote"], rows[i]["node"]) < fmt.Sprintf("%v%v", rows[j]["remote"], rows[j]["node"])
			})
			return a.render(mapsTable(rows, "remote", "node", "updates", "repo-status", "version", "status"))
		},
	}
}

// providerWithRemotes builds the provider and verifies it supports remotes.
func (a *app) providerWithRemotes() (provider.Provider, error) {
	p, err := a.Provider()
	if err != nil {
		return nil, err
	}
	if !p.Capabilities().Remotes {
		return nil, fmt.Errorf("remotes are only available with the PDM provider (current provider: %s); set provider: pdm in your profile", p.Name())
	}
	return p, nil
}

func remotesTable(remotes []domain.Remote) output.Tabular {
	t := output.Tabular{Columns: []string{"id", "type", "nodes", "web-url"}, Raw: remotes}
	for _, r := range remotes {
		t.Rows = append(t.Rows, []string{r.ID, r.Type, r.Nodes, r.Web})
	}
	return t
}
