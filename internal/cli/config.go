package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/ciroiriarte/pve-cli/internal/config"
	"github.com/ciroiriarte/pve-cli/internal/transport"
)

const keyringService = "pve-cli"

func newConfigCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Manage pve-cli configuration"}
	cmd.AddCommand(
		newConfigViewCmd(),
		newConfigPathCmd(),
		newConfigGetCmd(),
		newConfigSetCmd(),
		newConfigInitCmd(),
		newConfigTestAuthCmd(a),
	)
	return cmd
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the config file path",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			fmt.Fprintln(os.Stdout, config.DefaultPath())
			return nil
		},
	}
}

func newConfigViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "view",
		Short: "Print the config with secrets redacted",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			f, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}
			for name, p := range f.Profiles {
				if p.Auth.Secret != "" {
					p.Auth.Secret = "***redacted***"
					f.Profiles[name] = p
				}
			}
			b, err := yaml.Marshal(f)
			if err != nil {
				return err
			}
			os.Stdout.Write(b)
			return nil
		},
	}
}

func newConfigGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get <dotted.key>",
		Short:   "Get a config value (e.g. current_context)",
		Example: "  pc config get current_context\n  pc config get profiles.home.server",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			v, err := config.GetValue(config.DefaultPath(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, v)
			return nil
		},
	}
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "set <dotted.key> <value>",
		Short:   "Set a config value",
		Example: "  pc config set current_context home\n  pc config set profiles.home.server https://pve1:8006",
		Args:    cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return config.SetValue(config.DefaultPath(), args[0], args[1])
		},
	}
}

func newConfigInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter config file",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			path := config.DefaultPath()
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("config already exists at %s (use --force to overwrite)", path)
			}
			f := &config.File{
				CurrentContext: "default",
				Contexts:       map[string]config.Context{"default": {Profile: "default"}},
				Profiles: map[string]config.Profile{"default": {
					Provider: "pve",
					Server:   "https://CHANGE-ME:8006",
					Auth:     config.AuthConfig{Type: "token", TokenID: "user@pam!name", SecretRef: "keyring://pve-cli/default"},
				}},
			}
			if err := config.Save(path, f); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "wrote %s — edit it or use `pc auth login`\n", path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config")
	return cmd
}

func newContextCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "context", Short: "Select which configured cluster to talk to"}
	cmd.AddCommand(
		&cobra.Command{
			Use: "list", Short: "List contexts", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				f, err := config.Load(config.DefaultPath())
				if err != nil {
					return err
				}
				for name, c := range f.Contexts {
					marker := "  "
					if name == f.CurrentContext {
						marker = "* "
					}
					fmt.Fprintf(os.Stdout, "%s%s -> profile %s\n", marker, name, c.Profile)
				}
				return nil
			},
		},
		&cobra.Command{
			Use: "current", Short: "Print the current context", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				f, err := config.Load(config.DefaultPath())
				if err != nil {
					return err
				}
				fmt.Fprintln(os.Stdout, f.CurrentContext)
				return nil
			},
		},
		&cobra.Command{
			Use: "use <name>", Short: "Set the current context", Args: cobra.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error {
				f, err := config.Load(config.DefaultPath())
				if err != nil {
					return err
				}
				if _, ok := f.Contexts[args[0]]; !ok {
					return fmt.Errorf("context %q not found", args[0])
				}
				return config.SetValue(config.DefaultPath(), "current_context", args[0])
			},
		},
	)
	return cmd
}

// newConfigTestAuthCmd diagnoses why auth/connection might be failing: it shows
// the resolved profile/context, whether the secret dereferenced (keyring/env),
// and (unless --offline) makes one authenticated request to confirm the
// credentials work. Helpful on headless hosts where a locked OS keyring yields
// an opaque error.
func newConfigTestAuthCmd(a *app) *cobra.Command {
	var offline bool
	cmd := &cobra.Command{
		Use:   "test-auth",
		Short: "Diagnose credential resolution, keyring, and connectivity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if _, err := config.Load(config.DefaultPath()); err != nil {
				return fmt.Errorf("config: cannot load %s: %w", config.DefaultPath(), err)
			}
			// Resolving settings dereferences the secret (keyring/env); a failure
			// here is usually the real problem (e.g. a locked/absent keyring).
			s, err := a.resolvedSettings()
			if err != nil {
				fmt.Fprintf(out, "credential resolution: FAILED\n  %v\n", err)
				return err
			}

			// The winning source (flag > flag ref > env > profile), as decided
			// by config.Resolve — not just whatever the profile declares.
			secretSrc := orElse(s.SecretSource, "(none)")
			id := s.TokenID
			if s.AuthType == "ticket" {
				id = s.User
			}
			fmt.Fprintf(out, "context:    %s\n", orElse(s.ContextName, "(none)"))
			fmt.Fprintf(out, "profile:    %s\n", orElse(s.ProfileName, "(none)"))
			fmt.Fprintf(out, "provider:   %s\n", s.Provider)
			fmt.Fprintf(out, "server:     %s\n", orElse(s.Server, "(unset)"))
			fmt.Fprintf(out, "auth type:  %s\n", orElse(s.AuthType, "token"))
			fmt.Fprintf(out, "identity:   %s\n", orElse(id, "(unset)"))
			fmt.Fprintf(out, "secret:     %s  (resolved: %t)\n", secretSrc, s.Secret != "")

			if s.Secret == "" {
				return fmt.Errorf("no credential resolved; check the profile's secret_ref/keyring, env vars, or pass --token-secret")
			}
			if s.Server == "" {
				return fmt.Errorf("no server configured; set --server or the profile's server")
			}
			if offline {
				fmt.Fprintln(out, "probe:      SKIPPED (--offline)")
				return nil
			}

			p, err := a.Provider()
			if err != nil {
				fmt.Fprintf(out, "client:     FAILED\n  %v\n", err)
				return err
			}
			if _, err := p.Raw(cmd.Context(), "GET", "/version", nil); err != nil {
				fmt.Fprintf(out, "probe:      FAILED (GET /version)\n  %v\n", err)
				return err
			}
			fmt.Fprintln(out, "probe:      OK — authenticated request to /version succeeded")
			return nil
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "skip the live authenticated request")
	return cmd
}

func newAuthCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Authenticate and store credentials"}
	cmd.AddCommand(newAuthLoginCmd(a))
	return cmd
}

func newAuthLoginCmd(a *app) *cobra.Command {
	var tokenID, secret, secretRef, user, password, passwordRef, profile, fingerprint, provider string
	var insecure bool
	cmd := &cobra.Command{
		Use:   "login <server-url>",
		Short: "Store a credential (API token or user/password) as a profile",
		Long: "Writes a profile + context, storing the credential in the OS keyring\n" +
			"(falling back to the config file if no keyring is available).\n\n" +
			"Choose one auth method:\n" +
			"  --token-id <user@realm!name>   API token (recommended; non-interactive)\n" +
			"  --user <user@realm>            ticket auth (prompts for the password)\n\n" +
			"The secret/password is prompted if not passed. To keep it off the command\n" +
			"line (shell history, the process table) in scripts, use\n" +
			"--token-secret-ref/--password-ref env:NAME|keyring://service/key, or pass\n" +
			"`-` to read it from stdin; a plaintext --token-secret/--password works but\n" +
			"warns. After login, verify with `pc config test-auth`.",
		Example: "  pc auth login https://pve1:8006 --token-id 'svc@pve!cli'\n" +
			"  pc auth login https://pve1:8006 --token-id 'svc@pve!cli' --token-secret-ref env:PVE_TOKEN\n" +
			"  pass show pve/token | pc auth login https://pve1:8006 --token-id 'svc@pve!cli' --token-secret -\n" +
			"  pc auth login https://pve1:8006 --user root@pam",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			server := args[0]
			if (tokenID == "") == (user == "") {
				return fmt.Errorf("provide exactly one of --token-id (token auth) or --user (ticket auth)")
			}
			if profile == "" {
				profile = "default"
			}

			// cred is the credential stored in the keyring: the token secret
			// (token auth) or the user's password (ticket auth).
			var cred string
			auth := config.AuthConfig{Type: "token", TokenID: tokenID}
			var err error
			if tokenID != "" {
				cred, err = loginSecret(secret, secretRef, "--token-secret", "Token secret: ")
			} else {
				auth = config.AuthConfig{Type: "ticket", User: user}
				cred, err = loginSecret(password, passwordRef, "--password", fmt.Sprintf("Password for %s: ", user))
			}
			if err != nil {
				return err
			}

			path := config.DefaultPath()
			f, err := config.Load(path)
			if err != nil {
				return err
			}
			if f.Profiles == nil {
				f.Profiles = map[string]config.Profile{}
			}
			if f.Contexts == nil {
				f.Contexts = map[string]config.Context{}
			}

			// Trust-on-first-use: when the user neither pinned a fingerprint nor
			// opted out of verification, probe the server cert. Fresh Proxmox
			// installs ship self-signed certs, so the common first-run outcome is
			// an untrusted cert — offer to pin it instead of forcing --insecure or
			// an out-of-band `openssl … -fingerprint` over SSH.
			if fingerprint == "" && !insecure {
				fp, trusted, perr := transport.ProbeServerCert(server, 10*time.Second)
				switch {
				case perr != nil:
					fmt.Fprintf(os.Stderr, "[pc] could not reach %s to check its certificate (%v); saving the profile anyway\n", server, perr)
				case trusted:
					// System-trusted cert (public CA / already-trusted) — nothing to pin.
				case isTTY():
					fmt.Fprintf(os.Stderr, "\nThe server's certificate is not trusted by your system (self-signed?).\n  SHA-256 fingerprint: %s\n", fp)
					if promptYesNo("Trust and pin this fingerprint? [y/N]: ") {
						fingerprint = fp
						fmt.Fprintln(os.Stderr, "[pc] pinned the server fingerprint for this profile.")
					} else {
						fmt.Fprintln(os.Stderr, "[pc] not pinned — commands will fail TLS verification until you pin a fingerprint (--fingerprint) or trust the CA.")
					}
				default:
					// Non-interactive + untrusted: never auto-pin without confirmation.
					fmt.Fprintf(os.Stderr, "[pc] server certificate is untrusted; re-run with --fingerprint %s to pin it (or --insecure).\n", fp)
				}
			}

			if err := keyring.Set(keyringService, profile, cred); err != nil {
				fmt.Fprintf(os.Stderr, "[pc] keyring unavailable (%v); storing credential in config file\n", err)
				auth.Secret = cred
			} else {
				auth.SecretRef = fmt.Sprintf("keyring://%s/%s", keyringService, profile)
			}

			prof := config.Profile{Provider: orElse(provider, "pve"), Server: server, Auth: auth}
			prof.TLS.Fingerprint = fingerprint
			if insecure {
				verify := false
				prof.TLS.Verify = &verify
			}
			f.Profiles[profile] = prof
			f.Contexts[profile] = config.Context{Profile: profile}
			f.CurrentContext = profile

			if err := config.Save(path, f); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "saved profile %q and set it as the current context (%s)\n", profile, path)
			// One-time onboarding nudge: the command tree is deep, so shell
			// completion is the fastest way to discover it. Stderr + TTY-gated
			// so it never pollutes scripted/CI output.
			if isTTY() {
				fmt.Fprintln(os.Stderr, "[pc] tip: enable shell completion for faster command discovery — see `pc completion --help` (e.g. `pc completion bash`)")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&tokenID, "token-id", "", "API token id (user@realm!name) — token auth")
	cmd.Flags().StringVar(&secret, "token-secret", "", "API token secret (prompted if omitted; `-` reads stdin; prefer --token-secret-ref)")
	cmd.Flags().StringVar(&secretRef, "token-secret-ref", "", "resolve the API token secret from env:NAME or keyring://service/key")
	cmd.Flags().StringVar(&user, "user", "", "user (user@realm) — ticket auth")
	cmd.Flags().StringVar(&password, "password", "", "ticket-auth password (prompted if omitted; `-` reads stdin; prefer --password-ref)")
	cmd.Flags().StringVar(&passwordRef, "password-ref", "", "resolve the ticket-auth password from env:NAME or keyring://service/key")
	cmd.MarkFlagsMutuallyExclusive("token-id", "user")
	cmd.MarkFlagsMutuallyExclusive("token-secret", "token-secret-ref")
	cmd.MarkFlagsMutuallyExclusive("password", "password-ref")
	cmd.Flags().StringVar(&profile, "profile", "default", "profile name to write")
	cmd.Flags().StringVar(&fingerprint, "fingerprint", "", "pin the server cert SHA-256 fingerprint")
	cmd.Flags().StringVar(&provider, "provider", "pve", "backend provider: pve|pdm")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "disable TLS verification for this profile")
	return cmd
}

// loginSecret resolves the login credential via the shared resolveSecretInput
// (ref / `-` stdin / warned plaintext / TTY prompt). When nothing is given and
// stdin isn't a terminal it still reads one line from stdin, preserving the
// pre-existing `echo secret | pc auth login …` behavior.
// An empty result (e.g. `--token-secret -` with blank stdin) is rejected rather
// than saved as an empty credential.
func loginSecret(value, ref, flag, label string) (string, error) {
	s, err := resolveSecretInput(value, ref, flag, label)
	if err == nil && s == "" && value == "" && ref == "" && !isInputTTY() {
		s, err = promptSecret(label)
	}
	if errors.Is(err, io.EOF) {
		// Empty stdin (promptSecret only reports EOF when no data was read):
		// fall through to the clearer "empty credential" error.
		s, err = "", nil
	}
	if err != nil {
		return "", err
	}
	if s == "" {
		return "", fmt.Errorf("empty credential: provide a non-empty %s (or %s-ref)", flag, flag)
	}
	return s, nil
}

// promptYesNo asks a y/N question on stderr and returns true only on an
// explicit yes. In non-interactive mode it returns false (never auto-confirm a
// security decision like fingerprint pinning).
func promptYesNo(prompt string) bool {
	if !isTTY() {
		return false
	}
	fmt.Fprint(os.Stderr, prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	s := strings.ToLower(strings.TrimSpace(line))
	return s == "y" || s == "yes"
}

// promptSecret reads a secret from stdin. On an interactive terminal it prints
// the prompt to stderr and reads with echo DISABLED (so the secret never lands
// in the screen, scrollback, or a shared session). On a pipe/redirect (e.g.
// `--client-key -`) it reads a single line and strips only the trailing newline
// — intentional leading/trailing spaces in a secret are preserved.
func promptSecret(prompt string) (string, error) {
	if isInputTTY() {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr) // the suppressed Enter leaves the cursor mid-line
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func orElse(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
