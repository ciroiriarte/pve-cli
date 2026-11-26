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
	VMID   int       `json:"vmid" yaml:"vmid"`
	Name   string    `json:"name" yaml:"name"`
	Kind   GuestKind `json:"kind" yaml:"kind"`
	Node   string    `json:"node" yaml:"node"`
	Status string    `json:"status" yaml:"status"` // running, stopped, ...
	CPUs   int       `json:"cpus,omitempty" yaml:"cpus,omitempty"`
	MaxMem int64     `json:"maxmem,omitempty" yaml:"maxmem,omitempty"` // bytes
	MaxCPU float64   `json:"maxcpu,omitempty" yaml:"maxcpu,omitempty"`
	Uptime int64     `json:"uptime,omitempty" yaml:"uptime,omitempty"` // seconds
	Tags   string    `json:"tags,omitempty" yaml:"tags,omitempty"`
	Remote string    `json:"remote,omitempty" yaml:"remote,omitempty"` // PDM only
}

// Node is a cluster member.
type Node struct {
	Name   string  `json:"node" yaml:"node"`
	Status string  `json:"status" yaml:"status"` // online, offline
	CPU    float64 `json:"cpu,omitempty" yaml:"cpu,omitempty"`
	MaxCPU int     `json:"maxcpu,omitempty" yaml:"maxcpu,omitempty"`
	Mem    int64   `json:"mem,omitempty" yaml:"mem,omitempty"`
	MaxMem int64   `json:"maxmem,omitempty" yaml:"maxmem,omitempty"`
	Uptime int64   `json:"uptime,omitempty" yaml:"uptime,omitempty"`
	Remote string  `json:"remote,omitempty" yaml:"remote,omitempty"` // PDM only
}

// Remote is a cluster managed by Proxmox Datacenter Manager (PDM only).
type Remote struct {
	ID    string `json:"id" yaml:"id"`
	Type  string `json:"type" yaml:"type"` // pve | pbs
	Nodes string `json:"nodes,omitempty" yaml:"nodes,omitempty"`
	Web   string `json:"web-url,omitempty" yaml:"web-url,omitempty"`
}

// Storage is a configured storage backend.
type Storage struct {
	Name    string `json:"storage" yaml:"storage"`
	Type    string `json:"type" yaml:"type"`
	Node    string `json:"node,omitempty" yaml:"node,omitempty"`
	Content string `json:"content,omitempty" yaml:"content,omitempty"`
	Active  bool   `json:"active,omitempty" yaml:"active,omitempty"`
	Total   int64  `json:"total,omitempty" yaml:"total,omitempty"`
	Used    int64  `json:"used,omitempty" yaml:"used,omitempty"`
	Avail   int64  `json:"avail,omitempty" yaml:"avail,omitempty"`
}
