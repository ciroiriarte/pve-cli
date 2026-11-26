package protocol

import (
	"fmt"
	"strings"
)

// TaskHandle identifies a long-running Proxmox operation. Proxmox encodes all
// the routing information inside the UPID string itself.
//
// It is also the json/yaml payload a task-producing mutation prints when it
// doesn't wait (--no-wait, or -o json/yaml), so its tags are part of the
// scripting contract: keys match TaskStatus (`upid`, `node`), and `.upid` can be
// passed to `pc task wait` (for PDM it is the full `pve:<remote>!UPID:…` id).
type TaskHandle struct {
	Backend string `json:"backend" yaml:"backend"` // "pve" | "pdm" | "pbs"
	Node    string `json:"node" yaml:"node"`
	UPID    string `json:"upid" yaml:"upid"`
	Remote  string `json:"remote,omitempty" yaml:"remote,omitempty"`   // PDM only
	Display string `json:"display,omitempty" yaml:"display,omitempty"` // human label, e.g. "start VM 100"
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

// ParseUPID extracts routing fields from a UPID. Two formats exist:
//
//	PVE:      UPID:<node>:<pid>:<pstart>:<starttime>:<type>:<id>:<user>:
//	PBS/PDM:  UPID:<node>:<pid>:<pstart>:<task_id>:<starttime>:<type>:<id>:<authid>:
//
// The PBS form (proxmox-rest-server, also used by PDM's own tasks) carries an
// extra hex task counter, so its type sits one field later. It is recognised by
// its field count plus the fixed-width hex starttime in the sixth field (where
// PVE has the alphabetic task type); colons inside PBS worker ids are escaped,
// so the count is reliable.
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
	typ, id := parts[5], parts[6]
	if len(parts) == 10 && isHex8(parts[4]) && isHex8(parts[5]) {
		typ, id = parts[6], parts[7]
		if typ == "" {
			return TaskHandle{}, fmt.Errorf("malformed UPID (empty type): %q", upid)
		}
	}
	h.Display = strings.TrimSpace(typ + " " + id)
	return h, nil
}

// isHex8 reports whether s is an 8-digit hex field (PBS task_id/starttime).
func isHex8(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
