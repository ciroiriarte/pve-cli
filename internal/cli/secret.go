package cli

import (
	"fmt"

	"github.com/ciroiriarte/pve-cli/internal/config"
)

// resolveSecretInput turns a value/ref flag pair into a plaintext secret while
// keeping it off argv where possible. It is the shared mechanism behind every
// secret-bearing CLI flag (OIDC client key, guest agent password, remote token):
//
//   - ref != ""      → dereferenced via env:NAME | keyring://service/key
//   - value == "-"   → read from stdin (for `… | pc … --flag -`)
//   - value != ""    → used as-is, with a warning that argv leaks
//   - neither, TTY   → prompted without echo
//   - neither, no TTY → "" (caller decides whether that's an error)
//
// The two flags must be marked mutually exclusive by the caller. plaintextFlag
// is the flag name used in the leak warning; promptLabel is shown when prompting.
func resolveSecretInput(value, ref, plaintextFlag, promptLabel string) (string, error) {
	if ref != "" {
		v, err := config.ResolveSecretRef(ref)
		if err != nil {
			return "", err
		}
		if v == "" {
			return "", fmt.Errorf("%s resolved to an empty secret", ref)
		}
		return v, nil
	}
	if value == "-" {
		s, err := promptSecret("")
		if err != nil {
			return "", fmt.Errorf("read secret from stdin: %w", err)
		}
		return s, nil
	}
	if value != "" {
		fmt.Fprintf(stderrWriter(), "[pc] warning: plaintext %s leaks to shell history and the process table; prefer the -ref form (env:…/keyring://…) or `-` (stdin)\n", plaintextFlag)
		return value, nil
	}
	if isInputTTY() {
		return promptSecret(promptLabel)
	}
	return "", nil
}
