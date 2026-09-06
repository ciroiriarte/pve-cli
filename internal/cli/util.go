package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// stderrWriter returns the writer used for spinners and prompts.
func stderrWriter() io.Writer { return os.Stderr }

// secs converts a non-negative seconds count to a Duration (0 = unbounded).
func secs(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// confirm prompts for a destructive action unless --yes was passed. When not a
// TTY and --yes was not given, it refuses rather than silently proceeding.
func confirm(a *app, prompt string) error {
	if a.assumeYes {
		return nil
	}
	if !isTTY() {
		return fmt.Errorf("refusing destructive action in non-interactive mode; pass --yes to confirm: %s", prompt)
	}
	fmt.Fprintf(stderrWriter(), "%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	default:
		return fmt.Errorf("aborted")
	}
}

// ensurePVE refuses a PVE-only config write when the active backend is PDM,
// naming the current provider so the pve/pdm split isn't invisible (design
// decision #9). `op` is the human verb, e.g. "node network create".
func ensurePVE(p interface{ Name() string }, op, alt string) error {
	if p.Name() == "pdm" {
		msg := fmt.Sprintf("%s is only available on PVE (current provider: pdm); set provider: pve", op)
		if alt != "" {
			msg += ", " + alt
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// confirmWrite gates a non-GET escape-hatch call (`pc raw` / `pc api`) so a
// buried `--method DELETE` or an `api POST` can't mutate cluster state without a
// prompt. Read methods pass through untouched; --yes/-y skips the prompt.
func confirmWrite(a *app, method, path string) error {
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "OPTIONS":
		return nil
	}
	return confirm(a, fmt.Sprintf("%s %s — this may modify cluster state, proceed?", strings.ToUpper(method), path))
}
