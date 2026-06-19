package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newSubscriptionCmd manages PDM subscription keys and status.
func newSubscriptionCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "subscription", Aliases: []string{"sub"}, Short: "Manage PDM subscriptions and keys"}
	cmd.AddCommand(
		simpleGet(a, "node-status", "Per-node subscription status", 0, func([]string) string { return "/subscriptions/node-status" }, "node", "status", "key", "level"),
		newSubKeyCmd(a),
	)
	return cmd
}

func newSubKeyCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "key", Short: "Manage subscription keys"}
	cmd.AddCommand(
		simpleGet(a, "list", "List subscription keys", 0, func([]string) string { return "/subscriptions/keys" }, "key", "status", "node"),
		simpleGet(a, "show <key>", "Show a subscription key", 1, func(a []string) string { return "/subscriptions/keys/" + a[0] }),
	)
	var set []string
	add := &cobra.Command{
		Use: "add <key>", Short: "Add a subscription key", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.pdmProvider()
			if err != nil {
				return err
			}
			params, err := setToValues(set)
			if err != nil {
				return err
			}
			params.Set("key", args[0])
			return rawMutate(cmd.Context(), a, p, "POST", "/subscriptions/keys", params, "add key "+args[0], true, 0)
		},
	}
	add.Flags().StringArrayVar(&set, "set", nil, "field key=value (repeatable)")
	rm := &cobra.Command{
		Use: "remove <key>", Aliases: []string{"rm", "delete"}, Short: "Remove a subscription key", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.pdmProvider()
			if err != nil {
				return err
			}
			if err := confirm(a, fmt.Sprintf("remove subscription key %q?", args[0])); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", "/subscriptions/keys/"+args[0], nil, "remove key "+args[0], true, 0)
		},
	}
	cmd.AddCommand(add, rm)
	return cmd
}
