package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
	"github.com/ciroiriarte/pve-cli/internal/task"
)

func newTaskCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Inspect and wait on cluster tasks (UPIDs)",
	}
	cmd.AddCommand(newTaskListCmd(a), newTaskShowCmd(a), newTaskWaitCmd(a), newTaskLogCmd(a))
	return cmd
}

func newTaskListCmd(a *app) *cobra.Command {
	var node string
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List recent tasks",
		Example: "  pc task list\n  pc task list --node pve-01",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			tasks, err := p.ListTasks(cmd.Context(), node)
			if err != nil {
				return err
			}
			sort.Slice(tasks, func(i, j int) bool { return tasks[i].StartTime > tasks[j].StartTime })
			return a.render(tasksTable(tasks))
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "limit to a node")
	return cmd
}

func newTaskShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "show <upid>",
		Short:   "Show a task's status",
		Example: "  pc task show 'UPID:pve-01:...'",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			h, err := parseTaskArg(args[0])
			if err != nil {
				return err
			}
			st, err := p.TaskStatus(cmd.Context(), h)
			if err != nil {
				return err
			}
			return a.render(taskStatusTable(st))
		},
	}
}

func newTaskWaitCmd(a *app) *cobra.Command {
	var timeout int
	cmd := &cobra.Command{
		Use:     "wait <upid>",
		Short:   "Wait for a task to finish",
		Example: "  pc task wait 'UPID:pve-01:...' --timeout 600",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			h, err := parseTaskArg(args[0])
			if err != nil {
				return err
			}
			st, err := task.Wait(cmd.Context(), p.TaskStatus, h, task.WaitOptions{
				Timeout: secs(timeout),
				Spinner: isTTY(),
				Out:     stderrWriter(),
				Label:   "waiting for " + h.Display,
			})
			if err != nil {
				return err
			}
			return a.render(taskStatusTable(st))
		},
	}
	cmd.Flags().IntVar(&timeout, "timeout", 0, "seconds to wait (0 = no limit)")
	return cmd
}

func newTaskLogCmd(a *app) *cobra.Command {
	var follow bool
	var limit int
	cmd := &cobra.Command{
		Use:     "log <upid>",
		Short:   "Print a task's log output",
		Example: "  pc task log 'UPID:pve-01:...'\n  pc task log 'UPID:pve-01:...' --follow",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			h, err := parseTaskArg(args[0])
			if err != nil {
				return err
			}
			return streamTaskLog(cmd.Context(), p, h, follow, limit)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream new log lines until the task stops")
	cmd.Flags().IntVar(&limit, "limit", 0, "max lines per fetch (0 = server default)")
	return cmd
}

// parseTaskArg parses a task id argument. PDM proxied actions emit a
// remote-scoped id of the form "pve:<remote>!UPID:..." which carries the remote
// the task commands need; PDM's task endpoint also expects that full prefixed id
// in the path. A bare "UPID:..." is parsed directly (PVE).
func parseTaskArg(arg string) (protocol.TaskHandle, error) {
	if i := strings.Index(arg, "!"); i > 0 && strings.HasPrefix(arg, "pve:") && strings.Contains(arg[i:], "UPID:") {
		remote := strings.TrimPrefix(arg[:i], "pve:")
		h, err := protocol.ParseUPID(arg[i+1:])
		if err != nil {
			h = protocol.TaskHandle{UPID: arg[i+1:]}
		}
		h.Backend, h.Remote, h.UPID = "pdm", remote, arg
		return h, nil
	}
	return protocol.ParseUPID(arg)
}

// streamTaskLog prints task log lines. With follow, it polls for new lines
// until the task stops.
func streamTaskLog(ctx context.Context, p provider.Provider, h protocol.TaskHandle, follow bool, limit int) error {
	next := 1
	printFrom := func() error {
		lines, err := p.TaskLog(ctx, h, provider.LogOptions{Start: next, Limit: limit})
		if err != nil {
			return err
		}
		for _, l := range lines {
			fmt.Fprintln(os.Stdout, l.Text)
			if l.N >= next {
				next = l.N + 1
			}
		}
		return nil
	}
	if err := printFrom(); err != nil {
		return err
	}
	if !follow {
		return nil
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		// Read status BEFORE draining the log, then drain, then check Done: any
		// lines written up to (and after) the status read are captured by the
		// drain that follows it, so a task that stops mid-cycle loses no trailing
		// lines.
		st, err := p.TaskStatus(ctx, h)
		if err != nil {
			return err
		}
		if err := printFrom(); err != nil {
			return err
		}
		if st.Done() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func taskHandleTable(h protocol.TaskHandle) output.Tabular {
	return output.Tabular{
		Columns: []string{"task_id", "node"},
		Rows:    [][]string{{h.UPID, h.Node}},
		Raw:     h,
	}
}

func taskStatusTable(st protocol.TaskStatus) output.Tabular {
	return output.Tabular{
		Columns: []string{"upid", "node", "type", "status", "exitstatus"},
		Rows:    [][]string{{st.UPID, st.Node, st.Type, st.Status, st.ExitStatus}},
		Raw:     st,
	}
}

func tasksTable(tasks []protocol.TaskStatus) output.Tabular {
	t := output.Tabular{
		Columns: []string{"upid", "node", "type", "status", "exitstatus"},
		Raw:     tasks,
	}
	for _, st := range tasks {
		exit := st.ExitStatus
		if exit == "" && st.Status == "running" {
			exit = "-"
		}
		t.Rows = append(t.Rows, []string{shorten(st.UPID, 48), st.Node, st.Type, st.Status, exit})
	}
	return t
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
