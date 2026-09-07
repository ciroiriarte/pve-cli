package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Authentication-realm write commands (create/update/delete) on PVE's
// /access/domains. These are PVE-only: on PDM, realms live under
// `pc server realm` (a different control-plane endpoint), so the writes here
// fail fast pointing there.
//
// The OpenID client secret is handled carefully: a plaintext --client-key on the
// command line leaks into shell history and the process table, so the preferred
// input is --client-key-ref (env:NAME | keyring://service/key), with `-` on
// --client-key reading stdin and an interactive TTY prompt as the last resort.

func newAccessRealmCreateCmd(a *app) *cobra.Command {
	var typ, issuerURL, clientID, usernameClaim, comment string
	var clientKey, clientKeyRef string
	var autocreate, deflt bool
	var set []string
	cmd := &cobra.Command{
		Use:     "create <realm>",
		Aliases: []string{"add"},
		Short:   "Create an authentication realm (PVE)",
		Long: "Creates an authentication realm on PVE (POST /access/domains). --type is\n" +
			"required; OpenID/OIDC fields are promoted as first-class flags and any other\n" +
			"field can be passed via --set key=value.\n\n" +
			"The OpenID client secret should NOT be passed as a plaintext --client-key\n" +
			"(it leaks to shell history and the process table). Prefer:\n" +
			"  --client-key-ref env:OIDC_CLIENT_KEY   (or keyring://service/key)\n" +
			"  --client-key -                          (read the secret from stdin)\n" +
			"  omit both on a TTY                       (you'll be prompted, no echo)",
		Example: "  pc access realm create keycloak --type openid \\\n" +
			"    --issuer-url https://auth.example.com/realms/infra \\\n" +
			"    --client-id pve-cluster --client-key-ref env:KEYCLOAK_SECRET \\\n" +
			"    --username-claim preferred_username --autocreate",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVE(p, "access realm create", "for PDM use `pc server realm create`"); err != nil {
				return err
			}
			if typ == "" {
				return fmt.Errorf("--type is required (e.g. openid, ldap, ad)")
			}
			realm := args[0]
			base := map[string]string{
				"type": typ, "issuer-url": issuerURL, "client-id": clientID,
				"username-claim": usernameClaim, "comment": comment,
			}
			if cmd.Flags().Changed("autocreate") {
				base["autocreate"] = boolParam(autocreate)
			}
			if cmd.Flags().Changed("default") {
				base["default"] = boolParam(deflt)
			}
			// Resolve the client secret only for openid, and only if the user
			// supplied one somehow; other realm types don't take a client key.
			if typ == "openid" {
				key, err := resolveClientKey(cmd, clientKey, clientKeyRef)
				if err != nil {
					return err
				}
				if key != "" {
					base["client-key"] = key
				}
			}
			params, err := mergeSet(base, set)
			if err != nil {
				return err
			}
			params.Set("realm", realm)
			return rawMutate(cmd.Context(), a, p, "POST", "/access/domains", params, "create realm "+realm, true, 0)
		},
	}
	registerRealmFlags(cmd, &issuerURL, &clientID, &comment, &clientKey, &clientKeyRef, &autocreate, &deflt, &set)
	cmd.Flags().StringVar(&typ, "type", "", "realm type (openid|ldap|ad|…)")
	cmd.Flags().StringVar(&usernameClaim, "username-claim", "", "OpenID username claim (e.g. preferred_username, email); create-only")
	_ = cmd.RegisterFlagCompletionFunc("type", cobra.FixedCompletions([]string{"openid", "ldap", "ad"}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

func newAccessRealmUpdateCmd(a *app) *cobra.Command {
	var issuerURL, clientID, comment, digest string
	var clientKey, clientKeyRef string
	var autocreate, deflt bool
	var set []string
	cmd := &cobra.Command{
		Use:   "update <realm>",
		Short: "Update an authentication realm (PVE)",
		Long: "Updates a realm (PUT /access/domains/<realm>). Only the flags you pass are\n" +
			"changed; omitting the client-key flags leaves the stored secret untouched.\n" +
			"To rotate it, pass --client-key-ref env:…|keyring://… or `--client-key -`\n" +
			"(stdin) — a plaintext --client-key works but warns. Clear an optional field\n" +
			"with `--set delete=<field>` (e.g. --set delete=comment).",
		Example: "  pc access realm update keycloak --client-key-ref env:NEW_SECRET",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVE(p, "access realm update", "for PDM use `pc server realm`"); err != nil {
				return err
			}
			realm := args[0]
			// Note: PVE's realm PUT does not accept `username-claim` (POST-only),
			// so it is not offered on update — see registerRealmFlags.
			base := map[string]string{
				"issuer-url": issuerURL, "client-id": clientID,
				"comment": comment, "digest": digest,
			}
			if cmd.Flags().Changed("autocreate") {
				base["autocreate"] = boolParam(autocreate)
			}
			if cmd.Flags().Changed("default") {
				base["default"] = boolParam(deflt)
			}
			// Only resolve/send a client key if the user actually asked to change it.
			if cmd.Flags().Changed("client-key") || cmd.Flags().Changed("client-key-ref") {
				key, err := resolveClientKey(cmd, clientKey, clientKeyRef)
				if err != nil {
					return err
				}
				base["client-key"] = key
			}
			params, err := mergeSet(base, set)
			if err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "PUT", "/access/domains/"+realm, params, "update realm "+realm, true, 0)
		},
	}
	registerRealmFlags(cmd, &issuerURL, &clientID, &comment, &clientKey, &clientKeyRef, &autocreate, &deflt, &set)
	cmd.Flags().StringVar(&digest, "digest", "", "config digest for optimistic locking (optional)")
	return cmd
}

func newAccessRealmDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "delete <realm>", Aliases: []string{"rm"},
		Short: "Delete an authentication realm (PVE)", Args: cobra.ExactArgs(1),
		Example: "  pc access realm delete keycloak",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVE(p, "access realm delete", "for PDM use `pc server realm delete`"); err != nil {
				return err
			}
			realm := args[0]
			if err := confirm(a, fmt.Sprintf("delete authentication realm %q?", realm)); err != nil {
				return err
			}
			return rawMutate(cmd.Context(), a, p, "DELETE", "/access/domains/"+realm, nil, "delete realm "+realm, true, 0)
		},
	}
}

// registerRealmFlags wires the realm field flags common to create and update.
// --type and --username-claim are registered by create only: PVE's realm PUT
// rejects both (type is fixed at creation, username-claim is POST-only).
func registerRealmFlags(cmd *cobra.Command, issuerURL, clientID, comment, clientKey, clientKeyRef *string, autocreate, deflt *bool, set *[]string) {
	cmd.Flags().StringVar(issuerURL, "issuer-url", "", "OpenID issuer URL")
	cmd.Flags().StringVar(clientID, "client-id", "", "OpenID client id")
	cmd.Flags().StringVar(clientKey, "client-key", "", "OpenID client secret (use `-` for stdin; prefer --client-key-ref)")
	cmd.Flags().StringVar(clientKeyRef, "client-key-ref", "", "resolve the client secret from env:NAME or keyring://service/key")
	cmd.Flags().StringVar(comment, "comment", "", "realm comment/description")
	cmd.Flags().BoolVar(autocreate, "autocreate", false, "auto-create users on first login")
	cmd.Flags().BoolVar(deflt, "default", false, "make this the default realm")
	cmd.Flags().StringArrayVar(set, "set", nil, "any other field key=value (escape hatch, repeatable)")
	cmd.MarkFlagsMutuallyExclusive("client-key", "client-key-ref")
}

// resolveClientKey turns the --client-key / --client-key-ref inputs into the
// OIDC client secret via the shared off-argv resolver. Empty is allowed here (a
// non-openid realm has no client key; the API rejects a missing openid secret
// with a clear error).
func resolveClientKey(_ *cobra.Command, clientKey, clientKeyRef string) (string, error) {
	return resolveSecretInput(clientKey, clientKeyRef, "--client-key", "OpenID client key (leave empty to skip): ")
}
