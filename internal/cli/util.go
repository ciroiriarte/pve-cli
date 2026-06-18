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
