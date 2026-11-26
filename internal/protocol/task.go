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
	UPID       string `json:"upid" yaml:"upid"`
	Node       string `json:"node" yaml:"node"`
	Type       string `json:"type" yaml:"type"`
	ID         string `json:"id" yaml:"id"`
	User       string `json:"user" yaml:"user"`
	Status     string `json:"status" yaml:"status"`         // "running" | "stopped"
	ExitStatus string `json:"exitstatus" yaml:"exitstatus"` // "OK" or an error string when stopped
	StartTime  int64  `json:"starttime" yaml:"starttime"`
	PID        int    `json:"pid" yaml:"pid"`
}

// LogLine is one line of a task's log output.
type LogLine struct {
	N    int    `json:"n"` // 1-based line number
	Text string `json:"t"`
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
	// Guard against structurally-empty UPIDs (e.g. "UPID:::::::"): the routing
	// fields must actually be present, or polling would hit an empty node/type.
	if parts[1] == "" || parts[5] == "" {
		return TaskHandle{}, fmt.Errorf("malformed UPID (empty node or type): %q", upid)
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
