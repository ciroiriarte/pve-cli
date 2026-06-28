// Package pdm implements the Provider interface against Proxmox Datacenter
// Manager, which aggregates multiple PVE clusters ("remotes"). It supports
// discovery/aggregation (remotes + cross-remote resource listing) over the
// shared transport. Proxied per-remote lifecycle is deferred — use the `pc raw`
// / `pc api` escape hatch (the PDM API is reachable through them) meanwhile.
package pdm

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
	provider.SetPDMFactory(func(cl *transport.Client) provider.Provider {
		return &PDM{cl: cl}
	})
}

// PDM is the Datacenter Manager provider.
type PDM struct {
	cl *transport.Client
}

func (p *PDM) Name() string                        { return "pdm" }
func (p *PDM) Capabilities() provider.Capabilities { return provider.Capabilities{Remotes: true} }

// ListRemotes returns the clusters managed by this PDM. The list lives at
// /remotes/remote (GET /remotes is just the API index), and each remote's
// "nodes" is an array of "host:port,fingerprint=..." strings.
func (p *PDM) ListRemotes(ctx context.Context) ([]domain.Remote, error) {
	var raw []struct {
		ID    string   `json:"id"`
		Type  string   `json:"type"`
		Nodes []string `json:"nodes"`
	}
	if err := p.cl.Do(ctx, &transport.Request{Method: "GET", Path: "/remotes/remote"}, &raw); err != nil {
		return nil, err
	}
	out := make([]domain.Remote, 0, len(raw))
	for _, r := range raw {
		out = append(out, domain.Remote{ID: r.ID, Type: r.Type, Nodes: shortNodes(r.Nodes)})
	}
	return out, nil
}

// shortNodes renders a remote's node list compactly (short hostnames), dropping
// the ":port,fingerprint=..." suffix.
func shortNodes(ns []string) string {
	parts := make([]string, 0, len(ns))
	for _, n := range ns {
		h := n
		if i := strings.IndexAny(h, ":,"); i >= 0 {
			h = h[:i]
		}
		if i := strings.IndexByte(h, '.'); i >= 0 {
			h = h[:i]
		}
		parts = append(parts, h)
	}
	return strings.Join(parts, ",")
}

// resourceEntry is one per-remote group from GET /resources/list.
type resourceEntry struct {
	Remote    string        `json:"remote"`
	Error     string        `json:"error"`
	Resources []pdmResource `json:"resources"`
}

type pdmResource struct {
	Type   string  `json:"type"` // qemu, lxc, node, storage, ...
	VMID   int     `json:"vmid"`
	Name   string  `json:"name"`
	Node   string  `json:"node"`
	Status string  `json:"status"`
	MaxMem int64   `json:"maxmem"`
	MaxCPU float64 `json:"maxcpu"`
	Uptime int64   `json:"uptime"`
	Tags   string  `json:"tags"`
}

func (p *PDM) resources(ctx context.Context) ([]resourceEntry, error) {
	var entries []resourceEntry
	err := p.cl.Do(ctx, &transport.Request{Method: "GET", Path: "/resources/list"}, &entries)
	return entries, err
}

// ListNodes returns nodes across all remotes (each tagged with its remote).
func (p *PDM) ListNodes(ctx context.Context) ([]domain.Node, error) {
	entries, err := p.resources(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.Node
	for _, e := range entries {
		for _, r := range e.Resources {
			if r.Type != "pve-node" && r.Type != "node" {
				continue
			}
			out = append(out, domain.Node{Name: r.Node, Status: r.Status, MaxMem: r.MaxMem, Uptime: r.Uptime, Remote: e.Remote})
		}
	}
	return out, nil
}

// ListGuests returns VMs/containers across all remotes (tagged with remote).
func (p *PDM) ListGuests(ctx context.Context, f provider.GuestFilter) ([]domain.Guest, error) {
	entries, err := p.resources(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.Guest
	for _, e := range entries {
		for _, r := range e.Resources {
			kind, ok := kindFromType(r.Type)
			if !ok || (f.Kind != "" && kind != f.Kind) {
				continue
			}
			if f.Node != "" && r.Node != f.Node {
				continue
			}
			if f.Status != "" && r.Status != f.Status {
				continue
			}
			out = append(out, domain.Guest{
				VMID: r.VMID, Name: r.Name, Kind: kind, Node: r.Node,
				Status: r.Status, MaxMem: r.MaxMem, MaxCPU: r.MaxCPU, Uptime: r.Uptime, Tags: r.Tags, Remote: e.Remote,
			})
		}
	}
	return out, nil
}

// ResolveGuest finds a vmid across remotes; ambiguous if present on several.
func (p *PDM) ResolveGuest(ctx context.Context, vmid int) (domain.Guest, error) {
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
		return domain.Guest{}, &protocol.APIError{Kind: protocol.KindNotFound, Message: fmt.Sprintf("no guest with id %d found across remotes", vmid)}
	case 1:
		return matches[0], nil
	default:
		remotes := make([]string, 0, len(matches))
		for _, m := range matches {
			remotes = append(remotes, m.Remote)
		}
		return domain.Guest{}, &protocol.APIError{Kind: protocol.KindConflict, Message: fmt.Sprintf("guest id %d exists on multiple remotes: %s (use --remote to choose one)", vmid, strings.Join(remotes, ", "))}
	}
}

// ResolveGuestInRemote finds a vmid scoped to a single remote, disambiguating a
// vmid that exists on several remotes (backs the --remote flag).
func (p *PDM) ResolveGuestInRemote(ctx context.Context, vmid int, remote string) (domain.Guest, error) {
	guests, err := p.ListGuests(ctx, provider.GuestFilter{})
	if err != nil {
		return domain.Guest{}, err
	}
	for _, g := range guests {
		if g.VMID == vmid && strings.EqualFold(g.Remote, remote) {
			return g, nil
		}
	}
	return domain.Guest{}, &protocol.APIError{Kind: protocol.KindNotFound, Message: fmt.Sprintf("no guest with id %d found on remote %q", vmid, remote)}
}

// Raw issues an arbitrary PDM API call (escape hatch; also reaches proxied
// per-remote PVE endpoints under /pve/remotes/...).
func (p *PDM) Raw(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	req := &transport.Request{Method: strings.ToUpper(method), Path: normalizePath(path)}
	switch req.Method {
	case "GET", "HEAD":
		req.Query = params
	default:
		req.Form = params
	}
	return p.cl.DoRaw(ctx, req)
}

// PDM proxies a subset of guest power actions (start/stop/shutdown/resume) at
// /pve/remotes/{remote}/{kind}/{vmid}/{action}. reboot and provisioning are not
// proxied — those return ErrUnsupported.
var pdmPowerActions = map[string]bool{"start": true, "stop": true, "shutdown": true, "resume": true}

// GuestPower performs a proxied power action on the guest's remote.
func (p *PDM) GuestPower(ctx context.Context, g domain.Guest, action string) (protocol.TaskHandle, error) {
	if !pdmPowerActions[action] {
		return protocol.TaskHandle{}, fmt.Errorf("%q is not available via PDM (it proxies start/stop/shutdown/resume); use `pc raw` or target the cluster (%w)", action, provider.ErrUnsupported)
	}
	if g.Remote == "" {
		return protocol.TaskHandle{}, fmt.Errorf("guest %d has no remote; resolve it via PDM first", g.VMID)
	}
	path := fmt.Sprintf("/pve/remotes/%s/%s/%d/%s", g.Remote, guestEndpoint(g.Kind), g.VMID, action)
	var upid string
	if err := p.cl.Do(ctx, &transport.Request{Method: "POST", Path: path, Form: url.Values{}}, &upid); err != nil {
		return protocol.TaskHandle{}, err
	}
	h, err := protocol.ParseUPID(upid)
	if err != nil {
		h = protocol.TaskHandle{UPID: upid, Node: g.Node}
	}
	h.Backend, h.Remote = "pdm", g.Remote
	if h.Node == "" {
		h.Node = g.Node
	}
	h.Display = fmt.Sprintf("%s %s %d on %s", action, g.Kind, g.VMID, g.Remote)
	return h, nil
}

// GuestConfig reads a guest's config via the proxy.
func (p *PDM) GuestConfig(ctx context.Context, g domain.Guest) (map[string]any, error) {
	path := fmt.Sprintf("/pve/remotes/%s/%s/%d/config", g.Remote, guestEndpoint(g.Kind), g.VMID)
	// PDM's proxied config endpoint requires the mandatory `state` enum param
	// (unlike the direct PVE API); "active" returns the running/current config.
	var cfg map[string]any
	err := p.cl.Do(ctx, &transport.Request{Method: "GET", Path: path, Query: url.Values{"state": {"active"}}}, &cfg)
	return cfg, err
}

// TaskStatus polls a proxied task. The handle must carry its remote.
func (p *PDM) TaskStatus(ctx context.Context, h protocol.TaskHandle) (protocol.TaskStatus, error) {
	if h.Remote == "" {
		return protocol.TaskStatus{}, fmt.Errorf("PDM task status needs a remote (pass --remote or use a task from a PDM action)")
	}
	path := fmt.Sprintf("/pve/remotes/%s/tasks/%s/status", h.Remote, h.UPID)
	var st protocol.TaskStatus
	err := p.cl.Do(ctx, &transport.Request{Method: "GET", Path: path}, &st)
	return st, err
}

// TaskLog fetches a proxied task's log.
func (p *PDM) TaskLog(ctx context.Context, h protocol.TaskHandle, opts provider.LogOptions) ([]protocol.LogLine, error) {
	if h.Remote == "" {
		return nil, fmt.Errorf("PDM task log needs a remote")
	}
	q := url.Values{}
	if opts.Start > 0 {
		q.Set("start", strconv.Itoa(opts.Start-1))
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	var lines []protocol.LogLine
	err := p.cl.Do(ctx, &transport.Request{Method: "GET", Path: fmt.Sprintf("/pve/remotes/%s/tasks/%s/log", h.Remote, h.UPID), Query: q}, &lines)
	return lines, err
}

// ListTasks aggregates recent tasks across all remotes.
func (p *PDM) ListTasks(ctx context.Context, _ string) ([]protocol.TaskStatus, error) {
	remotes, err := p.ListRemotes(ctx)
	if err != nil {
		return nil, err
	}
	var all []protocol.TaskStatus
	for _, r := range remotes {
		var ts []protocol.TaskStatus
		if err := p.cl.Do(ctx, &transport.Request{Method: "GET", Path: "/pve/remotes/" + r.ID + "/tasks"}, &ts); err == nil {
			all = append(all, ts...)
		}
	}
	return all, nil
}

func guestEndpoint(k domain.GuestKind) string {
	if k == domain.KindCT {
		return "lxc"
	}
	return "qemu"
}

func kindFromType(t string) (domain.GuestKind, bool) {
	switch t {
	case "pve-qemu", "qemu":
		return domain.KindVM, true
	case "pve-lxc", "lxc":
		return domain.KindCT, true
	default:
		return "", false
	}
}

func normalizePath(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.TrimPrefix(p, "/api2/json")
}
