// Package domain holds provider-neutral resource models. Providers (pve, pdm)
// map their API responses onto these types so the CLI layer never sees raw HTTP.
package domain

// GuestKind distinguishes QEMU VMs from LXC containers.
type GuestKind string

const (
	KindVM GuestKind = "qemu"
	KindCT GuestKind = "lxc"
)

// Guest is a VM or container as seen across a cluster.
type Guest struct {
	VMID   int       `json:"vmid"`
	Name   string    `json:"name"`
	Kind   GuestKind `json:"kind"`
	Node   string    `json:"node"`
	Status string    `json:"status"` // running, stopped, ...
	CPUs   int       `json:"cpus,omitempty"`
	MaxMem int64     `json:"maxmem,omitempty"` // bytes
	MaxCPU float64   `json:"maxcpu,omitempty"`
	Uptime int64     `json:"uptime,omitempty"` // seconds
	Tags   string    `json:"tags,omitempty"`
	Remote string    `json:"remote,omitempty"` // PDM only
}

// Node is a cluster member.
type Node struct {
	Name   string  `json:"node"`
	Status string  `json:"status"` // online, offline
	CPU    float64 `json:"cpu,omitempty"`
	MaxCPU int     `json:"maxcpu,omitempty"`
	Mem    int64   `json:"mem,omitempty"`
	MaxMem int64   `json:"maxmem,omitempty"`
	Uptime int64   `json:"uptime,omitempty"`
	Remote string  `json:"remote,omitempty"` // PDM only
}

// Remote is a cluster managed by Proxmox Datacenter Manager (PDM only).
type Remote struct {
	ID    string `json:"id"`
	Type  string `json:"type"` // pve | pbs
	Nodes string `json:"nodes,omitempty"`
	Web   string `json:"web-url,omitempty"`
}

// Storage is a configured storage backend.
type Storage struct {
	Name    string `json:"storage"`
	Type    string `json:"type"`
	Node    string `json:"node,omitempty"`
	Content string `json:"content,omitempty"`
	Active  bool   `json:"active,omitempty"`
	Total   int64  `json:"total,omitempty"`
	Used    int64  `json:"used,omitempty"`
	Avail   int64  `json:"avail,omitempty"`
}
