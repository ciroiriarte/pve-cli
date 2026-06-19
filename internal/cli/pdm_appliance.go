package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newAutoInstallCmd inspects PDM's automated-installation assets.
func newAutoInstallCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "auto-install", Aliases: []string{"autoinstall"}, Short: "Inspect PDM auto-install assets"}
	cmd.AddCommand(
		simpleGet(a, "installations", "List installations", 0, func([]string) string { return "/auto-install/installations" }, "uuid", "status"),
		simpleGet(a, "prepared", "List prepared install configs", 0, func([]string) string { return "/auto-install/prepared" }, "id"),
		simpleGet(a, "prepared-show <id>", "Show a prepared config", 1, func(a []string) string { return "/auto-install/prepared/" + a[0] }),
		simpleGet(a, "tokens", "List auto-install tokens", 0, func([]string) string { return "/auto-install/tokens" }, "id", "comment"),
		newDeleteCmd(a, "installation-delete <uuid>", "Delete an installation record", "/auto-install/installations/", "installation"),
		newDeleteCmd(a, "prepared-delete <id>", "Delete a prepared config", "/auto-install/prepared/", "prepared config"),
		newDeleteCmd(a, "token-delete <id>", "Delete an auto-install token", "/auto-install/tokens/", "token"),
	)
	return cmd
}

// newDeleteCmd builds a confirm-gated DELETE command at base+<arg>.
func newDeleteCmd(a *app, use, short, base, label string) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.pdmProvider()
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("delete %s %q?", label, args[0])); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", base+args[0], nil, "delete "+label+" "+args[0], true, 0)
		},
	}
}

// newServerNodeCmd surfaces read-only views of the PDM appliance host itself.
func newServerNodeCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "node", Short: "Inspect the PDM appliance host (read-only)"}
	cmd.AddCommand(
		simpleGet(a, "list", "List PDM appliance host nodes", 0, func([]string) string { return "/nodes" }, "node", "status"),
		simpleGet(a, "status <node>", "Host status", 1, func(a []string) string { return "/nodes/" + a[0] + "/status" }),
		simpleGet(a, "time <node>", "Host time/zone", 1, func(a []string) string { return "/nodes/" + a[0] + "/time" }),
		simpleGet(a, "dns <node>", "Host DNS config", 1, func(a []string) string { return "/nodes/" + a[0] + "/dns" }),
		simpleGet(a, "network <node>", "Host network interfaces", 1, func(a []string) string { return "/nodes/" + a[0] + "/network" }, "iface", "type", "method", "address"),
		simpleGet(a, "apt-versions <node>", "Installed package versions", 1, func(a []string) string { return "/nodes/" + a[0] + "/apt/versions" }, "Package", "OldVersion", "Version"),
		simpleGet(a, "apt-updates <node>", "Available package updates", 1, func(a []string) string { return "/nodes/" + a[0] + "/apt/update" }, "Package", "OldVersion", "Version"),
		simpleGet(a, "subscription <node>", "Host subscription status", 1, func(a []string) string { return "/nodes/" + a[0] + "/subscription" }),
		simpleGet(a, "certificates <node>", "Host certificate info", 1, func(a []string) string { return "/nodes/" + a[0] + "/certificates/info" }, "filename", "fingerprint", "notafter"),
	)
	return cmd
}
