package cli

import "github.com/spf13/cobra"

// newPBSCmd inspects Proxmox Backup Server remotes managed by PDM (read-only).
func newPBSCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "pbs", Short: "Inspect Proxmox Backup Server remotes (read-only)"}
	cmd.AddCommand(
		simpleGet(a, "remotes", "List PBS remotes", 0, func([]string) string { return "/pbs/remotes" }, "id", "type"),
		simpleGet(a, "show <remote>", "Show a PBS remote", 1, func(a []string) string { return "/pbs/remotes/" + a[0] }),
		simpleGet(a, "status <remote>", "PBS remote status", 1, func(a []string) string { return "/pbs/remotes/" + a[0] + "/status" }),
		newPBSDatastoreCmd(a),
	)
	return cmd
}

func newPBSDatastoreCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "datastore", Aliases: []string{"ds"}, Short: "Inspect PBS datastores on a remote"}
	cmd.AddCommand(
		simpleGet(a, "list <remote>", "List datastores", 1,
			func(a []string) string { return "/pbs/remotes/" + a[0] + "/datastore" }, "store", "comment"),
		simpleGet(a, "show <remote> <datastore>", "Show a datastore", 2,
			func(a []string) string { return "/pbs/remotes/" + a[0] + "/datastore/" + a[1] }),
		simpleGet(a, "namespaces <remote> <datastore>", "List datastore namespaces", 2,
			func(a []string) string { return "/pbs/remotes/" + a[0] + "/datastore/" + a[1] + "/namespaces" }, "ns"),
		simpleGet(a, "snapshots <remote> <datastore>", "List datastore snapshots", 2,
			func(a []string) string { return "/pbs/remotes/" + a[0] + "/datastore/" + a[1] + "/snapshots" },
			"backup-type", "backup-id", "backup-time", "size"),
	)
	return cmd
}
