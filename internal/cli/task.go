package cli

import (
	"sort"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/task"
)

func newTaskCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Inspect and wait on cluster tasks (UPIDs)",
	}
	cmd.AddCommand(newTaskListCmd(a), newTaskShowCmd(a), newTaskWaitCmd(a))
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
			h, err := protocol.ParseUPID(args[0])
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
			h, err := protocol.ParseUPID(args[0])
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
