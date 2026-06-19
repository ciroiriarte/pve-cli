package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newAccessCmd manages PDM access control: users, tokens, roles, ACLs, realms.
func newAccessCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "access", Short: "Manage PDM access control (users, tokens, roles, ACLs, realms)"}
	cmd.AddCommand(newAccessUserCmd(a), newAccessTokenCmd(a), newAccessACLCmd(a),
		simpleGet(a, "roles", "List roles and their privileges", 0, func([]string) string { return "/access/roles" }, "roleid", "privs", "special"),
		simpleGet(a, "permissions", "Show the caller's effective permissions", 0, func([]string) string { return "/access/permissions" }),
		newAccessRealmCmd(a),
		simpleGet(a, "tfa [userid]", "List TFA entries", 0, func([]string) string { return "/access/tfa" }, "userid", "type", "description"),
	)
	return cmd
}

func newAccessUserCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "user", Short: "Manage PDM users"}
	cmd.AddCommand(
		simpleGet(a, "list", "List users", 0, func([]string) string { return "/access/users" }, "userid", "enable", "comment"),
		simpleGet(a, "show <userid>", "Show a user", 1, func(a []string) string { return "/access/users/" + a[0] }),
	)
	var set []string
	create := &cobra.Command{
		Use: "create <userid>", Short: "Create a user (e.g. alice@pve)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.pdmProvider()
			if err != nil {
				return err
			}
			params, err := setToValues(set)
			if err != nil {
				return err
			}
			params.Set("userid", args[0])
			return rawMutate(cmd.Context(), a, p, "POST", "/access/users", params, "create user "+args[0], true, 0)
		},
	}
	create.Flags().StringArrayVar(&set, "set", nil, "field key=value (e.g. --set comment=ops, repeatable)")
	del := &cobra.Command{
		Use: "delete <userid>", Aliases: []string{"rm"}, Short: "Delete a user", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.pdmProvider()
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("delete user %q?", args[0])); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", "/access/users/"+args[0], nil, "delete user "+args[0], true, 0)
		},
	}
	cmd.AddCommand(create, del)
	return cmd
}

func newAccessTokenCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "token", Short: "Manage API tokens for a user"}
	cmd.AddCommand(
		simpleGet(a, "list <userid>", "List a user's tokens", 1, func(a []string) string { return "/access/users/" + a[0] + "/token" }, "tokenid", "comment", "expire"),
		simpleGet(a, "show <userid> <token>", "Show a token", 2, func(a []string) string { return "/access/users/" + a[0] + "/token/" + a[1] }),
	)
	create := &cobra.Command{
		Use: "create <userid> <token>", Short: "Create an API token (prints the secret once)", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.pdmProvider()
			if err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "POST", "/access/users/"+args[0]+"/token/"+args[1], nil,
				fmt.Sprintf("create token %s for %s", args[1], args[0]), true, 0)
		},
	}
	del := &cobra.Command{
		Use: "delete <userid> <token>", Aliases: []string{"rm"}, Short: "Delete an API token", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.pdmProvider()
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("delete token %q of %q?", args[1], args[0])); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", "/access/users/"+args[0]+"/token/"+args[1], nil,
				fmt.Sprintf("delete token %s of %s", args[1], args[0]), true, 0)
		},
	}
	cmd.AddCommand(create, del)
	return cmd
}

func newAccessACLCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "acl", Short: "View and set access-control list entries"}
	cmd.AddCommand(simpleGet(a, "list", "List ACL entries", 0, func([]string) string { return "/access/acl" }, "path", "ugid", "roleid", "type", "propagate"))
	var path, roles, ugids string
	var asGroups, propagate, delete_ bool
	set := &cobra.Command{
		Use: "set", Short: "Set (or delete) an ACL entry", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.pdmProvider()
			if err != nil {
				return err
			}
			if path == "" || roles == "" || ugids == "" {
				return fmt.Errorf("--path, --roles and --ids are required")
			}
			params := map[string][]string{"path": {path}, "roles": {roles}}
			if asGroups {
				params["groups"] = []string{ugids}
			} else {
				params["users"] = []string{ugids}
			}
			if propagate {
				params["propagate"] = []string{"1"}
			}
			if delete_ {
				params["delete"] = []string{"1"}
			}
			return rawMutate(cmd.Context(), a, p, "PUT", "/access/acl", params, "update acl "+path, true, 0)
		},
	}
	set.Flags().StringVar(&path, "path", "", "ACL path (e.g. /resource/dc-west)")
	set.Flags().StringVar(&roles, "roles", "", "comma-separated role ids")
	set.Flags().StringVar(&ugids, "ids", "", "comma-separated user (or group) ids")
	set.Flags().BoolVar(&asGroups, "as-groups", false, "treat --ids as group ids")
	set.Flags().BoolVar(&propagate, "propagate", true, "propagate to child paths")
	set.Flags().BoolVar(&delete_, "delete", false, "remove the entry instead of adding it")
	cmd.AddCommand(set)
	return cmd
}

func newAccessRealmCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "realm", Aliases: []string{"domain"}, Short: "Authentication realms"}
	cmd.AddCommand(
		simpleGet(a, "list", "List authentication realms", 0, func([]string) string { return "/access/domains" }, "realm", "type", "comment"),
	)
	sync := &cobra.Command{
		Use: "sync <realm>", Short: "Sync users/groups from a realm (LDAP/AD)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.pdmProvider()
			if err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "POST", "/access/domains/"+args[0]+"/sync", nil, "sync realm "+args[0], true, 0)
		},
	}
	cmd.AddCommand(sync)
	return cmd
}
