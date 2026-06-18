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
				Status: r.Status, MaxMem: r.MaxMem, MaxCPU: r.MaxCPU, Uptime: r.Uptime, Remote: e.Remote,
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
		return domain.Guest{}, &protocol.APIError{Kind: protocol.KindConflict, Message: fmt.Sprintf("guest id %d exists on multiple remotes: %s", vmid, strings.Join(remotes, ", "))}
	}
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

// Proxied per-remote lifecycle is not wrapped yet; surface a clear pointer to
// the escape hatch rather than guessing the proxy semantics.
func (p *PDM) GuestPower(context.Context, domain.Guest, string) (protocol.TaskHandle, error) {
	return protocol.TaskHandle{}, lifecycleUnsupported("power")
}
func (p *PDM) GuestConfig(context.Context, domain.Guest) (map[string]any, error) {
	return nil, lifecycleUnsupported("config")
}
func (p *PDM) TaskStatus(context.Context, protocol.TaskHandle) (protocol.TaskStatus, error) {
	return protocol.TaskStatus{}, lifecycleUnsupported("task status")
}
func (p *PDM) ListTasks(context.Context, string) ([]protocol.TaskStatus, error) {
	return nil, lifecycleUnsupported("task list")
}
func (p *PDM) TaskLog(context.Context, protocol.TaskHandle, provider.LogOptions) ([]protocol.LogLine, error) {
	return nil, lifecycleUnsupported("task log")
}

func lifecycleUnsupported(op string) error {
	return fmt.Errorf("%s via PDM is not wrapped yet (%w); use `pc raw pve remotes <remote> ...` or target the cluster directly",
		op, provider.ErrUnsupported)
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
