// Package pbs implements the Provider interface against a Proxmox Backup
// Server. PBS has no guests, nodes-as-cluster or remotes, so those operations
// return provider.ErrUnsupported (naming the provider); tasks and the raw
// escape hatch are fully supported. The curated datastore/GC/prune/verify
// commands are thin CLI wrappers over Raw (see internal/cli/pbs.go).
package pbs

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ciroiriarte/pve-cli/internal/domain"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
	"github.com/ciroiriarte/pve-cli/internal/transport"
)

func init() {
	provider.SetPBSFactory(func(cl *transport.Client) provider.Provider {
		return &PBS{cl: cl}
	})
}

// Node is the node segment used for PBS's node-scoped endpoints. A PBS server
// is a single host and its API accepts "localhost" for it, which avoids having
// to learn the hostname first.
const Node = "localhost"

// PBS is the Backup Server provider.
type PBS struct {
	cl *transport.Client
}

// Name returns the provider identifier.
func (p *PBS) Name() string { return "pbs" }

// Capabilities reports that PBS has no PDM remotes.
func (p *PBS) Capabilities() provider.Capabilities { return provider.Capabilities{} }

func unsupported(what string) error {
	return fmt.Errorf("%s are not available on Proxmox Backup Server (current provider: pbs); set provider: pve or pdm (%w)", what, provider.ErrUnsupported)
}

// ListNodes is unsupported: PBS is a single host, not a PVE cluster.
func (p *PBS) ListNodes(context.Context) ([]domain.Node, error) {
	return nil, unsupported("cluster nodes")
}

// ListGuests is unsupported: PBS stores backups, it doesn't run guests.
func (p *PBS) ListGuests(context.Context, provider.GuestFilter) ([]domain.Guest, error) {
	return nil, unsupported("guests")
}

// ResolveGuest is unsupported on PBS.
func (p *PBS) ResolveGuest(context.Context, int) (domain.Guest, error) {
	return domain.Guest{}, unsupported("guests")
}

// GuestPower is unsupported on PBS.
func (p *PBS) GuestPower(context.Context, domain.Guest, string) (protocol.TaskHandle, error) {
	return protocol.TaskHandle{}, unsupported("guests")
}

// GuestConfig is unsupported on PBS.
func (p *PBS) GuestConfig(context.Context, domain.Guest) (map[string]any, error) {
	return nil, unsupported("guests")
}

// ListRemotes is unsupported on PBS (a PDM feature).
func (p *PBS) ListRemotes(context.Context) ([]domain.Remote, error) {
	return nil, unsupported("remotes")
}

func taskPath(upid, suffix string) string {
	// Raw UPID: the transport URL-encodes the path exactly once.
	return fmt.Sprintf("/nodes/%s/tasks/%s/%s", Node, upid, suffix)
}

// TaskStatus polls a task. PBS's status object uses the same keys as PVE's
// (upid, node, type, id, user, status, exitstatus, starttime, pid).
func (p *PBS) TaskStatus(ctx context.Context, h protocol.TaskHandle) (protocol.TaskStatus, error) {
	var st protocol.TaskStatus
	err := p.cl.Do(ctx, &transport.Request{Method: "GET", Path: taskPath(h.UPID, "status")}, &st)
	return st, err
}

// pbsTask is a row from GET /nodes/{node}/tasks. Unlike PVE, PBS names the type
// and id worker_type/worker_id, and `status` is the exit status of a finished
// task (absent, with no endtime, while it is still running).
type pbsTask struct {
	UPID       string `json:"upid"`
	Node       string `json:"node"`
	PID        int    `json:"pid"`
	StartTime  int64  `json:"starttime"`
	EndTime    int64  `json:"endtime"`
	WorkerType string `json:"worker_type"`
	WorkerID   string `json:"worker_id"`
	User       string `json:"user"`
	Status     string `json:"status"`
}

// ListTasks lists recent tasks on the server (the node argument is ignored:
// PBS is a single host).
func (p *PBS) ListTasks(ctx context.Context, _ string) ([]protocol.TaskStatus, error) {
	var rows []pbsTask
	if err := p.cl.Do(ctx, &transport.Request{Method: "GET", Path: fmt.Sprintf("/nodes/%s/tasks", Node)}, &rows); err != nil {
		return nil, err
	}
	out := make([]protocol.TaskStatus, 0, len(rows))
	for _, r := range rows {
		st := protocol.TaskStatus{
			UPID: r.UPID, Node: r.Node, Type: r.WorkerType, ID: r.WorkerID,
			User: r.User, StartTime: r.StartTime, PID: r.PID, Status: "running",
		}
		if r.EndTime != 0 || r.Status != "" {
			st.Status, st.ExitStatus = "stopped", r.Status
		}
		// The list echoes the requested node ("localhost"); the UPID carries
		// the real hostname, matching what task show reports.
		if h, err := protocol.ParseUPID(r.UPID); err == nil {
			st.Node = h.Node
		}
		out = append(out, st)
	}
	return out, nil
}

// TaskLog returns task log lines (PBS 'start' is 0-based, like PVE).
func (p *PBS) TaskLog(ctx context.Context, h protocol.TaskHandle, opts provider.LogOptions) ([]protocol.LogLine, error) {
	q := url.Values{}
	if opts.Start > 0 {
		q.Set("start", strconv.Itoa(opts.Start-1))
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	var lines []protocol.LogLine
	err := p.cl.Do(ctx, &transport.Request{Method: "GET", Path: taskPath(h.UPID, "log"), Query: q}, &lines)
	return lines, err
}

// Raw issues an arbitrary PBS API call (backs `pc raw` / `pc api` and the
// curated PBS commands).
func (p *PBS) Raw(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req := &transport.Request{Method: strings.ToUpper(method), Path: strings.TrimPrefix(path, "/api2/json")}
	switch req.Method {
	case "GET", "HEAD":
		req.Query = params
	default:
		req.Form = params
	}
	return p.cl.DoRaw(ctx, req)
}
