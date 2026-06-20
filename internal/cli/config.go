package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"
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
			f, err := config.Load(config.DefaultPath())
			if err != nil {
				return fmt.Errorf("config: cannot load %s: %w", config.DefaultPath(), err)
			}
			// Resolving settings dereferences the secret (keyring/env); a failure
			// here is usually the real problem (e.g. a locked/absent keyring).
			s, err := a.resolvedSettings()
			if err != nil {
				fmt.Fprintf(out, "credential resolution: FAILED\n  %v\n", err)
				return err
			}

			secretSrc := ""
			if prof, ok := f.Profiles[s.ProfileName]; ok {
				switch {
				case prof.Auth.SecretRef != "":
					secretSrc = prof.Auth.SecretRef
				case prof.Auth.Secret != "":
					secretSrc = "inline (plaintext in config)"
				}
			}
			if secretSrc == "" {
				// No profile-based secret; it came from env (PVE_CLI_*) or a flag,
				// or nowhere.
				if s.Secret != "" {
					secretSrc = "env or flag"
				} else {
					secretSrc = "(none)"
				}
			}
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
	var tokenID, secret, profile, fingerprint, provider string
	var insecure bool
	cmd := &cobra.Command{
		Use:   "login <server-url>",
		Short: "Store an API token credential as a profile",
		Long: "Stores the token secret in the OS keyring (falling back to the config file\n" +
			"if no keyring is available) and writes a profile + context.",
		Example: "  pc auth login https://pve1:8006 --token-id 'svc@pve!cli' --token-secret XXXX",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			server := args[0]
			if tokenID == "" {
				return fmt.Errorf("--token-id is required")
			}
			if secret == "" {
				s, err := promptSecret("Token secret: ")
				if err != nil {
					return err
				}
				secret = s
			}
			if profile == "" {
				profile = "default"
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

			auth := config.AuthConfig{Type: "token", TokenID: tokenID}
			if err := keyring.Set(keyringService, profile, secret); err != nil {
				fmt.Fprintf(os.Stderr, "[pc] keyring unavailable (%v); storing secret in config file\n", err)
				auth.Secret = secret
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
	cmd.Flags().StringVar(&tokenID, "token-id", "", "API token id (user@realm!name)")
	cmd.Flags().StringVar(&secret, "token-secret", "", "API token secret (prompted if omitted)")
	cmd.Flags().StringVar(&profile, "profile", "default", "profile name to write")
	cmd.Flags().StringVar(&fingerprint, "fingerprint", "", "pin the server cert SHA-256 fingerprint")
	cmd.Flags().StringVar(&provider, "provider", "pve", "backend provider: pve|pdm")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "disable TLS verification for this profile")
	return cmd
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

func promptSecret(prompt string) (string, error) {
	if isTTY() {
		fmt.Fprint(os.Stderr, prompt)
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func orElse(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
