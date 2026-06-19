package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newServerCmd manages PDM server-level configuration: auth realms, ACME,
// certificate, notes, and saved views.
func newServerCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "server", Short: "Manage PDM server configuration (realms, ACME, certificate, views)"}
	cmd.AddCommand(newServerRealmCmd(a), newServerACMECmd(a), newServerViewCmd(a), newServerNodeCmd(a),
		simpleGet(a, "certificate", "Show the PDM server certificate info", 0, func([]string) string { return "/config/certificate" }),
		newServerNotesCmd(a),
	)
	return cmd
}

// realmType validates the realm backend type segment.
func realmTypeArg(t string) (string, error) {
	switch t {
	case "ad", "ldap", "openid":
		return t, nil
	default:
		return "", fmt.Errorf("realm type must be one of ad|ldap|openid (got %q)", t)
	}
}

func newServerRealmCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "realm", Short: "Configure authentication realms (ad|ldap|openid)"}
	list := &cobra.Command{
		Use: "list <type>", Short: "List realms of a type", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := realmTypeArg(args[0])
			if err != nil {
				return err
			}
			return a.pdmGet(cmd, "/config/access/"+t, "realm", "comment")
		},
	}
	show := &cobra.Command{
		Use: "show <type> <realm>", Short: "Show a realm's config", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := realmTypeArg(args[0])
			if err != nil {
				return err
			}
			return a.pdmGet(cmd, "/config/access/"+t+"/"+args[1])
		},
	}
	var set []string
	create := &cobra.Command{
		Use: "create <type> <realm>", Short: "Create a realm (use --set for fields)", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := realmTypeArg(args[0])
			if err != nil {
				return err
			}
			p, err := a.pdmProvider()
			if err != nil {
				return err
			}
			params, err := setToValues(set)
			if err != nil {
				return err
			}
			params.Set("realm", args[1])
			return rawMutate(cmd.Context(), a, p, "POST", "/config/access/"+t, params, "create realm "+args[1], true, 0)
		},
	}
	create.Flags().StringArrayVar(&set, "set", nil, "field key=value (repeatable)")
	del := &cobra.Command{
		Use: "delete <type> <realm>", Aliases: []string{"rm"}, Short: "Delete a realm", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := realmTypeArg(args[0])
			if err != nil {
				return err
			}
			p, err := a.pdmProvider()
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("delete %s realm %q?", t, args[1])); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", "/config/access/"+t+"/"+args[1], nil, "delete realm "+args[1], true, 0)
		},
	}
	cmd.AddCommand(list, show, create, del)
	return cmd
}

func newServerACMECmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "acme", Short: "Inspect ACME (Let's Encrypt) configuration"}
	cmd.AddCommand(
		simpleGet(a, "accounts", "List ACME accounts", 0, func([]string) string { return "/config/acme/account" }, "name"),
		simpleGet(a, "plugins", "List ACME plugins", 0, func([]string) string { return "/config/acme/plugins" }, "plugin", "type", "api"),
		simpleGet(a, "directories", "List known ACME directories", 0, func([]string) string { return "/config/acme/directories" }, "name", "url"),
		simpleGet(a, "tos", "Show the ACME terms of service URL", 0, func([]string) string { return "/config/acme/tos" }),
	)
	return cmd
}

func newServerViewCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "view", Short: "Manage saved resource views"}
	cmd.AddCommand(
		simpleGet(a, "list", "List views", 0, func([]string) string { return "/config/views" }, "id", "name"),
		simpleGet(a, "show <id>", "Show a view", 1, func(a []string) string { return "/config/views/" + a[0] }),
	)
	var set []string
	create := &cobra.Command{
		Use: "create <id>", Short: "Create a view (use --set for fields)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.pdmProvider()
			if err != nil {
				return err
			}
			params, err := setToValues(set)
			if err != nil {
				return err
			}
			params.Set("id", args[0])
			return rawMutate(cmd.Context(), a, p, "POST", "/config/views", params, "create view "+args[0], true, 0)
		},
	}
	create.Flags().StringArrayVar(&set, "set", nil, "field key=value (repeatable)")
	del := &cobra.Command{
		Use: "delete <id>", Aliases: []string{"rm"}, Short: "Delete a view", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.pdmProvider()
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("delete view %q?", args[0])); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", "/config/views/"+args[0], nil, "delete view "+args[0], true, 0)
		},
	}
	cmd.AddCommand(create, del)
	return cmd
}

func newServerNotesCmd(a *app) *cobra.Command {
	var text string
	cmd := &cobra.Command{
		Use: "notes", Short: "Show or set the PDM datacenter notes", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.pdmProvider()
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("set") {
				return a.pdmGet(cmd, "/config/notes")
			}
			return rawMutate(cmd.Context(), a, p, "PUT", "/config/notes", map[string][]string{"text": {text}}, "update notes", true, 0)
		},
	}
	cmd.Flags().StringVar(&text, "set", "", "set the notes text")
	return cmd
}
