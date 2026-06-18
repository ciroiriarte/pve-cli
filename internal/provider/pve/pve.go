// Package pve implements the Provider interface against a Proxmox VE cluster's
// REST API. It registers itself with the provider package via init().
package pve

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
	provider.SetPVEFactory(func(cl *transport.Client) provider.Provider {
		return &PVE{cl: cl}
	})
}

// PVE is the direct-cluster provider.
type PVE struct {
	cl *transport.Client
}

// Name returns the provider identifier.
func (p *PVE) Name() string { return "pve" }

// Capabilities reports that a single cluster has no PDM remotes.
func (p *PVE) Capabilities() provider.Capabilities {
	return provider.Capabilities{Remotes: false}
}

// clusterResource is a row from GET /cluster/resources.
type clusterResource struct {
	Type   string  `json:"type"` // node, qemu, lxc, storage, ...
	ID     string  `json:"id"`
	Node   string  `json:"node"`
	VMID   int     `json:"vmid"`
	Name   string  `json:"name"`
	Status string  `json:"status"`
	MaxMem int64   `json:"maxmem"`
	Mem    int64   `json:"mem"`
	MaxCPU float64 `json:"maxcpu"`
	CPU    float64 `json:"cpu"`
	Uptime int64   `json:"uptime"`
	Tags   string  `json:"tags"`
}

func (p *PVE) clusterResources(ctx context.Context, typ string) ([]clusterResource, error) {
	q := url.Values{}
	if typ != "" {
		q.Set("type", typ)
	}
	var res []clusterResource
	err := p.cl.Do(ctx, &transport.Request{Method: "GET", Path: "/cluster/resources", Query: q}, &res)
	return res, err
}

// ListNodes returns cluster members.
func (p *PVE) ListNodes(ctx context.Context) ([]domain.Node, error) {
	res, err := p.clusterResources(ctx, "node")
	if err != nil {
		return nil, err
	}
	out := make([]domain.Node, 0, len(res))
	for _, r := range res {
		// Defensive client-side filter: don't rely on server honoring ?type=node.
		if r.Type != "node" {
			continue
		}
		out = append(out, domain.Node{
			Name:   r.Node,
			Status: r.Status,
			CPU:    r.CPU,
			MaxCPU: int(r.MaxCPU),
			Mem:    r.Mem,
			MaxMem: r.MaxMem,
			Uptime: r.Uptime,
		})
	}
	return out, nil
}

// ListGuests returns VMs and/or containers matching the filter.
func (p *PVE) ListGuests(ctx context.Context, f provider.GuestFilter) ([]domain.Guest, error) {
	res, err := p.clusterResources(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]domain.Guest, 0, len(res))
	for _, r := range res {
		kind, ok := kindFromType(r.Type)
		if !ok {
			continue
		}
		if f.Kind != "" && kind != f.Kind {
			continue
		}
		if f.Node != "" && r.Node != f.Node {
			continue
		}
		if f.Status != "" && r.Status != f.Status {
			continue
		}
		out = append(out, domain.Guest{
			VMID:   r.VMID,
			Name:   r.Name,
			Kind:   kind,
			Node:   r.Node,
			Status: r.Status,
			MaxMem: r.MaxMem,
			MaxCPU: r.MaxCPU,
			Uptime: r.Uptime,
			Tags:   r.Tags,
		})
	}
	return out, nil
}

// ResolveGuest locates the node hosting a vmid (the key UX abstraction that
// hides Proxmox's node-centric API from the user).
func (p *PVE) ResolveGuest(ctx context.Context, vmid int) (domain.Guest, error) {
	guests, err := p.ListGuests(ctx, provider.GuestFilter{})
	if err != nil {
		return domain.Guest{}, err
	}
	var matches []domain.Guest
	for _, g := range guests {
		if g.VMID == vmid {
			matches = append(matches, g)
		}
	}
	switch len(matches) {
	case 0:
		return domain.Guest{}, &protocol.APIError{Kind: protocol.KindNotFound, Message: fmt.Sprintf("no guest with id %d found in cluster", vmid)}
	case 1:
		return matches[0], nil
	default:
		nodes := make([]string, 0, len(matches))
		for _, m := range matches {
			nodes = append(nodes, m.Node)
		}
		return domain.Guest{}, &protocol.APIError{Kind: protocol.KindConflict, Message: fmt.Sprintf("guest id %d is ambiguous across nodes %s; pass --node", vmid, strings.Join(nodes, ", "))}
	}
}

// GuestPower issues a lifecycle action and returns the resulting task handle.
func (p *PVE) GuestPower(ctx context.Context, g domain.Guest, action string) (protocol.TaskHandle, error) {
	endpoint := guestEndpoint(g.Kind)
	if endpoint == "" {
		return protocol.TaskHandle{}, fmt.Errorf("unknown guest kind %q", g.Kind)
	}
	path := fmt.Sprintf("/nodes/%s/%s/%d/status/%s", g.Node, endpoint, g.VMID, action)
	var upid string
	if err := p.cl.Do(ctx, &transport.Request{Method: "POST", Path: path, Form: url.Values{}}, &upid); err != nil {
		return protocol.TaskHandle{}, err
	}
	h, err := protocol.ParseUPID(upid)
	if err != nil {
		// Some actions may not return a UPID; synthesize a handle.
		return protocol.TaskHandle{Backend: "pve", Node: g.Node, UPID: upid, Display: action}, nil
	}
	h.Display = fmt.Sprintf("%s %s %d", action, g.Kind, g.VMID)
	return h, nil
}

// GuestConfig returns the raw config map for a guest.
func (p *PVE) GuestConfig(ctx context.Context, g domain.Guest) (map[string]any, error) {
	endpoint := guestEndpoint(g.Kind)
	path := fmt.Sprintf("/nodes/%s/%s/%d/config", g.Node, endpoint, g.VMID)
	var cfg map[string]any
	err := p.cl.Do(ctx, &transport.Request{Method: "GET", Path: path}, &cfg)
	return cfg, err
}

// TaskStatus polls a task's status.
func (p *PVE) TaskStatus(ctx context.Context, h protocol.TaskHandle) (protocol.TaskStatus, error) {
	// Pass the UPID raw: the transport URL-encodes the path exactly once.
	// Pre-escaping here would double-encode the '!' in token UPIDs.
	path := fmt.Sprintf("/nodes/%s/tasks/%s/status", h.Node, h.UPID)
	var st protocol.TaskStatus
	err := p.cl.Do(ctx, &transport.Request{Method: "GET", Path: path}, &st)
	return st, err
}

// ListTasks lists recent tasks, optionally scoped to a node.
func (p *PVE) ListTasks(ctx context.Context, node string) ([]protocol.TaskStatus, error) {
	if node == "" {
		nodes, err := p.ListNodes(ctx)
		if err != nil {
			return nil, err
		}
		var all []protocol.TaskStatus
		for _, n := range nodes {
			ts, err := p.nodeTasks(ctx, n.Name)
			if err != nil {
				return nil, err
			}
			all = append(all, ts...)
		}
		return all, nil
	}
	return p.nodeTasks(ctx, node)
}

func (p *PVE) nodeTasks(ctx context.Context, node string) ([]protocol.TaskStatus, error) {
	var ts []protocol.TaskStatus
	err := p.cl.Do(ctx, &transport.Request{Method: "GET", Path: fmt.Sprintf("/nodes/%s/tasks", node)}, &ts)
	for i := range ts {
		ts[i].Node = node
	}
	return ts, err
}

// TaskLog returns task log lines via /nodes/{node}/tasks/{upid}/log.
func (p *PVE) TaskLog(ctx context.Context, h protocol.TaskHandle, opts provider.LogOptions) ([]protocol.LogLine, error) {
	q := url.Values{}
	if opts.Start > 0 {
		q.Set("start", strconv.Itoa(opts.Start-1)) // PVE 'start' is 0-based
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	path := fmt.Sprintf("/nodes/%s/tasks/%s/log", h.Node, h.UPID) // raw UPID; see TaskStatus
	var lines []protocol.LogLine
	err := p.cl.Do(ctx, &transport.Request{Method: "GET", Path: path, Query: q}, &lines)
	return lines, err
}

// Raw issues an arbitrary API call.
func (p *PVE) Raw(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	req := &transport.Request{Method: strings.ToUpper(method), Path: normalizePath(path)}
	switch req.Method {
	case "GET", "HEAD":
		req.Query = params
	default:
		req.Form = params
	}
	return p.cl.DoRaw(ctx, req)
}

func kindFromType(t string) (domain.GuestKind, bool) {
	switch t {
	case "qemu":
		return domain.KindVM, true
	case "lxc":
		return domain.KindCT, true
	default:
		return "", false
	}
}

func guestEndpoint(k domain.GuestKind) string {
	switch k {
	case domain.KindVM:
		return "qemu"
	case domain.KindCT:
		return "lxc"
	default:
		return ""
	}
}

func normalizePath(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.TrimPrefix(p, "/api2/json")
}
