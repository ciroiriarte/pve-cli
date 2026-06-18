package cli

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// rawMutate issues a mutating API call via provider.Raw and, when the response
// is a UPID, drives the shared task-wait UX. Non-task mutations (e.g. config
// updates that return null) simply succeed.
func rawMutate(ctx context.Context, a *app, p provider.Provider, method, path string, params url.Values, label string, wait bool, timeout int) error {
	body, err := p.Raw(ctx, method, path, params)
	if err != nil {
		return err
	}
	var upid string
	if err := protocol.DecodeData(body, &upid); err != nil || !protocol.IsUPID(upid) {
		return nil // synchronous mutation, nothing to wait on
	}
	h, err := protocol.ParseUPID(upid)
	if err != nil {
		return nil
	}
	h.Display = label
	return finishTask(ctx, a, p, h, wait, timeout, label)
}

// kindEndpoint maps a guest kind to its API path segment.
func kindEndpoint(spec guestSpec) string { return string(spec.kind) } // "qemu" | "lxc"

// waitFlags registers the standard --wait/--no-wait/--wait-timeout trio shared
// by all task-producing mutations. Effective wait = *wait && !*noWait.
func waitFlags(cmd *cobra.Command, wait, noWait *bool, timeout *int) {
	cmd.Flags().BoolVar(wait, "wait", true, "wait for the task to finish (default)")
	cmd.Flags().BoolVar(noWait, "no-wait", false, "return immediately with the task id")
	cmd.Flags().IntVar(timeout, "wait-timeout", 0, "seconds to wait (0 = no limit)")
	cmd.MarkFlagsMutuallyExclusive("wait", "no-wait")
}

func newGuestCloneCmd(a *app, spec guestSpec) *cobra.Command {
	var node, name, targetNode, storage string
	var full, wait, noWait bool
	var timeout int
	cmd := &cobra.Command{
		Use:     "clone <vmid> <newid>",
		Short:   fmt.Sprintf("Clone a %s to a new id", spec.label),
		Example: fmt.Sprintf("  pc %s clone 100 200 --name copy --full", spec.noun),
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			g, err := resolveGuest(cmd.Context(), p, spec, args[0], node)
			if err != nil {
				return err
			}
			newid, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid newid %q: must be a number", args[1])
			}
			params := url.Values{"newid": {args[1]}}
			if name != "" {
				params.Set("name", name)
			}
			if targetNode != "" {
				params.Set("target", targetNode)
			}
			if full {
				params.Set("full", "1")
			}
			if storage != "" {
				params.Set("storage", storage)
			}
			path := fmt.Sprintf("/nodes/%s/%s/%d/clone", g.Node, kindEndpoint(spec), g.VMID)
			return rawMutate(cmd.Context(), a, p, "POST", path, params,
				fmt.Sprintf("clone %s %d -> %d", spec.label, g.VMID, newid), wait && !noWait, timeout)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "node hosting the source guest")
	cmd.Flags().StringVar(&name, "name", "", "name for the clone")
	cmd.Flags().StringVar(&targetNode, "target-node", "", "destination node")
	cmd.Flags().BoolVar(&full, "full", false, "full clone (copy disks) instead of linked")
	cmd.Flags().StringVar(&storage, "storage", "", "target storage/pool (full clone)")
	waitFlags(cmd, &wait, &noWait, &timeout)
	return cmd
}

func newGuestDeleteCmd(a *app, spec guestSpec) *cobra.Command {
	var node string
	var purge, wait, noWait bool
	var timeout int
	cmd := &cobra.Command{
		Use:     "delete <vmid>",
		Aliases: []string{"destroy", "rm"},
		Short:   fmt.Sprintf("Delete a %s", spec.label),
		Example: fmt.Sprintf("  pc %s delete 200 --yes --purge", spec.noun),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			g, err := resolveGuest(cmd.Context(), p, spec, args[0], node)
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("permanently delete %s %d (%s) on %s?", spec.label, g.VMID, g.Name, g.Node)); err != nil {
				return err
			}
			params := url.Values{}
			if purge {
				params.Set("purge", "1")
			}
			path := fmt.Sprintf("/nodes/%s/%s/%d", g.Node, kindEndpoint(spec), g.VMID)
			return rawMutate(cmd.Context(), a, p, "DELETE", path, params,
				fmt.Sprintf("delete %s %d", spec.label, g.VMID), wait && !noWait, timeout)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "node hosting the guest")
	cmd.Flags().BoolVar(&purge, "purge", false, "also remove from backup jobs / HA")
	waitFlags(cmd, &wait, &noWait, &timeout)
	return cmd
}

func newGuestMigrateCmd(a *app, spec guestSpec) *cobra.Command {
	var node, targetNode string
	var online, wait, noWait bool
	var timeout int
	cmd := &cobra.Command{
		Use:     "migrate <vmid>",
		Short:   fmt.Sprintf("Migrate a %s to another node", spec.label),
		Example: fmt.Sprintf("  pc %s migrate 100 --target-node pve-02 --online", spec.noun),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if targetNode == "" {
				return fmt.Errorf("--target-node is required")
			}
			p, err := a.Provider()
			if err != nil {
				return err
			}
			g, err := resolveGuest(cmd.Context(), p, spec, args[0], node)
			if err != nil {
				return err
			}
			params := url.Values{"target": {targetNode}}
			if online {
				params.Set("online", "1")
			}
			path := fmt.Sprintf("/nodes/%s/%s/%d/migrate", g.Node, kindEndpoint(spec), g.VMID)
			return rawMutate(cmd.Context(), a, p, "POST", path, params,
				fmt.Sprintf("migrate %s %d -> %s", spec.label, g.VMID, targetNode), wait && !noWait, timeout)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "current node hosting the guest")
	cmd.Flags().StringVar(&targetNode, "target-node", "", "destination node (required)")
	cmd.Flags().BoolVar(&online, "online", false, "live migration")
	waitFlags(cmd, &wait, &noWait, &timeout)
	return cmd
}

func newGuestConfigCmd(a *app, spec guestSpec) *cobra.Command {
	var node string
	var set []string
	cmd := &cobra.Command{
		Use:     "config <vmid>",
		Short:   fmt.Sprintf("Show or update %s config", spec.label),
		Example: fmt.Sprintf("  pc %s config 100\n  pc %s config 100 --set cores=4 --set memory=4096", spec.noun, spec.noun),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			g, err := resolveGuest(cmd.Context(), p, spec, args[0], node)
			if err != nil {
				return err
			}
			if len(set) == 0 {
				cfg, err := p.GuestConfig(cmd.Context(), g)
				if err != nil {
					return err
				}
				return a.render(guestConfigTable(g, cfg))
			}
			params := url.Values{}
			for _, kv := range set {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("invalid --set %q: expected key=value", kv)
				}
				params.Set(k, v)
			}
			path := fmt.Sprintf("/nodes/%s/%s/%d/config", g.Node, kindEndpoint(spec), g.VMID)
			if err := rawMutate(cmd.Context(), a, p, "PUT", path, params,
				fmt.Sprintf("update %s %d config", spec.label, g.VMID), true, 0); err != nil {
				return err
			}
			fmt.Fprintf(stderrWriter(), "updated %s %d config\n", spec.label, g.VMID)
			return nil
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "node hosting the guest")
	cmd.Flags().StringArrayVar(&set, "set", nil, "set a config key (key=value, repeatable)")
	return cmd
}

func newGuestCreateCmd(a *app, spec guestSpec) *cobra.Command {
	var node, name string
	var memory, cores int
	var set []string
	var wait, noWait bool
	var timeout int
	cmd := &cobra.Command{
		Use:   "create <vmid>",
		Short: fmt.Sprintf("Create a %s", spec.label),
		Example: fmt.Sprintf("  pc %s create 200 --node pve-01 --name web --memory 2048 --cores 2 \\\n"+
			"    --set net0=virtio,bridge=vmbr0", spec.noun),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if node == "" {
				return fmt.Errorf("--node is required for create (creation is node-scoped)")
			}
			if _, err := strconv.Atoi(args[0]); err != nil {
				return fmt.Errorf("invalid vmid %q: must be a number", args[0])
			}
			p, err := a.Provider()
			if err != nil {
				return err
			}
			params := url.Values{"vmid": {args[0]}}
			if name != "" {
				params.Set("name", name)
			}
			if memory > 0 {
				params.Set("memory", strconv.Itoa(memory))
			}
			if cores > 0 {
				params.Set("cores", strconv.Itoa(cores))
			}
			for _, kv := range set {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("invalid --set %q: expected key=value", kv)
				}
				params.Set(k, v)
			}
			path := fmt.Sprintf("/nodes/%s/%s", node, kindEndpoint(spec))
			return rawMutate(cmd.Context(), a, p, "POST", path, params,
				fmt.Sprintf("create %s %s", spec.label, args[0]), wait && !noWait, timeout)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "target node (required)")
	cmd.Flags().StringVar(&name, "name", "", "guest name/hostname")
	cmd.Flags().IntVar(&memory, "memory", 0, "memory in MiB")
	cmd.Flags().IntVar(&cores, "cores", 0, "CPU cores")
	cmd.Flags().StringArrayVar(&set, "set", nil, "any additional key=value param (repeatable)")
	waitFlags(cmd, &wait, &noWait, &timeout)
	return cmd
}

func newGuestSnapshotCmd(a *app, spec guestSpec) *cobra.Command {
	cmd := &cobra.Command{Use: "snapshot", Aliases: []string{"snap"}, Short: fmt.Sprintf("Manage %s snapshots", spec.label)}
	var node string

	create := &cobra.Command{
		Use: "create <vmid> <name>", Short: "Create a snapshot", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			g, err := resolveGuest(cmd.Context(), p, spec, args[0], node)
			if err != nil {
				return err
			}
			path := fmt.Sprintf("/nodes/%s/%s/%d/snapshot", g.Node, kindEndpoint(spec), g.VMID)
			return rawMutate(cmd.Context(), a, p, "POST", path, url.Values{"snapname": {args[1]}},
				fmt.Sprintf("snapshot %s %d", spec.label, g.VMID), true, 0)
		},
	}
	list := &cobra.Command{
		Use: "list <vmid>", Short: "List snapshots", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			g, err := resolveGuest(cmd.Context(), p, spec, args[0], node)
			if err != nil {
				return err
			}
			path := fmt.Sprintf("/nodes/%s/%s/%d/snapshot", g.Node, kindEndpoint(spec), g.VMID)
			body, err := p.Raw(cmd.Context(), "GET", path, nil)
			if err != nil {
				return err
			}
			var snaps []map[string]any
			if err := protocol.DecodeData(body, &snaps); err != nil {
				return err
			}
			t := output.Tabular{Columns: []string{"name", "description"}, Raw: snaps}
			names := make([]string, 0, len(snaps))
			for _, s := range snaps {
				names = append(names, fmt.Sprintf("%v", s["name"]))
			}
			sort.Strings(names)
			byName := map[string]map[string]any{}
			for _, s := range snaps {
				byName[fmt.Sprintf("%v", s["name"])] = s
			}
			for _, n := range names {
				t.Rows = append(t.Rows, []string{n, fmt.Sprintf("%v", byName[n]["description"])})
			}
			return a.render(t)
		},
	}
	del := &cobra.Command{
		Use: "delete <vmid> <name>", Aliases: []string{"rm"}, Short: "Delete a snapshot", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			g, err := resolveGuest(cmd.Context(), p, spec, args[0], node)
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("delete snapshot %q of %s %d?", args[1], spec.label, g.VMID)); err != nil {
				return err
			}
			path := fmt.Sprintf("/nodes/%s/%s/%d/snapshot/%s", g.Node, kindEndpoint(spec), g.VMID, args[1])
			return rawMutate(cmd.Context(), a, p, "DELETE", path, nil,
				fmt.Sprintf("delete snapshot %s of %s %d", args[1], spec.label, g.VMID), true, 0)
		},
	}
	cmd.PersistentFlags().StringVar(&node, "node", "", "node hosting the guest")
	cmd.AddCommand(create, list, del)
	return cmd
}
