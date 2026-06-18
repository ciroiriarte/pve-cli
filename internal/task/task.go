// Package task orchestrates waiting on Proxmox long-running operations (UPIDs),
// with an optional TTY spinner. Polling itself is delegated to a provider.
package task

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/ciroiriarte/pve-cli/internal/protocol"
)

// PollFunc fetches the current status of a task.
type PollFunc func(ctx context.Context, h protocol.TaskHandle) (protocol.TaskStatus, error)

// WaitOptions controls Wait behavior.
type WaitOptions struct {
	Interval time.Duration // poll interval (default 1s)
	Timeout  time.Duration // 0 = no timeout
	Spinner  bool          // show a spinner (TTY only)
	Out      io.Writer     // spinner sink (default os.Stderr)
	Label    string        // human label, e.g. "Starting VM 100"
}

var spinnerFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

// Wait polls until the task finishes, times out, or ctx is cancelled. It
// returns the final status. A task that ends with a non-OK exit status yields a
// protocol.APIError of Kind KindTaskFailed.
func Wait(ctx context.Context, poll PollFunc, h protocol.TaskHandle, opt WaitOptions) (protocol.TaskStatus, error) {
	interval := opt.Interval
	if interval == 0 {
		interval = time.Second
	}
	if opt.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opt.Timeout)
		defer cancel()
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	frame := 0
	for {
		st, err := poll(ctx, h)
		if err != nil {
			clearSpinner(opt)
			return protocol.TaskStatus{}, err
		}
		if st.Done() {
			clearSpinner(opt)
			if !st.OK() {
				return st, &protocol.APIError{
					Kind:    protocol.KindTaskFailed,
					Message: fmt.Sprintf("task failed: %s", st.ExitStatus),
				}
			}
			return st, nil
		}
		drawSpinner(opt, &frame)

		select {
		case <-ctx.Done():
			clearSpinner(opt)
			return protocol.TaskStatus{}, fmt.Errorf("waiting for task %s: %w", h.UPID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func drawSpinner(opt WaitOptions, frame *int) {
	if !opt.Spinner || opt.Out == nil {
		return
	}
	fmt.Fprintf(opt.Out, "\r%c %s…", spinnerFrames[*frame%len(spinnerFrames)], opt.Label)
	*frame++
}

func clearSpinner(opt WaitOptions) {
	if !opt.Spinner || opt.Out == nil {
		return
	}
	fmt.Fprint(opt.Out, "\r\033[K")
}
