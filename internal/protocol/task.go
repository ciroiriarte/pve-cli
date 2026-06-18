package protocol

import (
	"fmt"
	"strings"
)

// TaskHandle identifies a long-running Proxmox operation. Proxmox encodes all
// the routing information inside the UPID string itself.
type TaskHandle struct {
	Backend string // "pve" | "pdm"
	Node    string
	UPID    string
	Remote  string // PDM only
	Display string // human label, e.g. "start VM 100"
}

// TaskStatus is the polled state of a task.
type TaskStatus struct {
	UPID       string `json:"upid"`
	Node       string `json:"node"`
	Type       string `json:"type"`
	ID         string `json:"id"`
	User       string `json:"user"`
	Status     string `json:"status"`     // "running" | "stopped"
	ExitStatus string `json:"exitstatus"` // "OK" or an error string when stopped
	StartTime  int64  `json:"starttime"`
	PID        int    `json:"pid"`
}

// Done reports whether the task has finished running.
func (t TaskStatus) Done() bool { return t.Status == "stopped" }

// OK reports whether a finished task succeeded.
func (t TaskStatus) OK() bool { return t.Done() && t.ExitStatus == "OK" }

// IsUPID reports whether s looks like a Proxmox UPID string.
func IsUPID(s string) bool {
	return strings.HasPrefix(s, "UPID:")
}

// ParseUPID extracts routing fields from a UPID. Format:
//
//	UPID:<node>:<pid>:<pstart>:<starttime>:<type>:<id>:<user>:
func ParseUPID(upid string) (TaskHandle, error) {
	if !IsUPID(upid) {
		return TaskHandle{}, fmt.Errorf("not a UPID: %q", upid)
	}
	parts := strings.Split(upid, ":")
	// UPID has 8 colon-separated fields plus the trailing empty segment.
	if len(parts) < 8 {
		return TaskHandle{}, fmt.Errorf("malformed UPID: %q", upid)
	}
	h := TaskHandle{
		Backend: "pve",
		Node:    parts[1],
		UPID:    upid,
	}
	typ := parts[5]
	id := parts[6]
	h.Display = strings.TrimSpace(typ + " " + id)
	return h, nil
}
