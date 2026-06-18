// Package coverage records which API endpoints the curated pc commands cover,
// so coverage vs the full schema is measurable (not guessed). Endpoints not
// listed here are reachable only via `pc raw` / `pc api`.
package coverage

import "fmt"

// Curated maps "METHOD /path" (using the schema's {param} placeholders) to the
// pc command that wraps it. Keep this in sync with the cobra command tree —
// the coverage matrix and its test are driven entirely by this map.
var Curated = map[string]string{}

func reg(cmd, method, path string) { Curated[method+" "+path] = cmd }

// regGuest registers an endpoint for both qemu (vm) and lxc (ct) command trees.
func regGuest(cmd, method, suffix string) {
	reg("vm "+cmd, method, "/nodes/{node}/qemu/{vmid}"+suffix)
	reg("ct "+cmd, method, "/nodes/{node}/lxc/{vmid}"+suffix)
}

func init() {
	// meta / cluster-wide reads
	reg("version", "GET", "/version")
	reg("node/guest list", "GET", "/cluster/resources")
	reg("node show", "GET", "/nodes")
	reg("node show", "GET", "/nodes/{node}")

	// guest listings + lifecycle (vm + ct)
	reg("vm list", "GET", "/nodes/{node}/qemu")
	reg("ct list", "GET", "/nodes/{node}/lxc")
	regGuest("create", "POST", "")            // POST /nodes/{node}/qemu
	regGuest("show/config", "GET", "/config") // also config (no --set)
	regGuest("config --set", "PUT", "/config")
	regGuest("clone", "POST", "/clone")
	regGuest("delete", "DELETE", "")
	regGuest("migrate", "POST", "/migrate")
	for _, a := range []string{"start", "stop", "shutdown", "reboot"} {
		regGuest(a, "POST", "/status/"+a)
	}
	regGuest("snapshot list/create", "GET", "/snapshot")
	regGuest("snapshot create", "POST", "/snapshot")
	regGuest("snapshot delete", "DELETE", "/snapshot/{snapname}")

	// storage + backup
	reg("storage list", "GET", "/storage")
	reg("storage show", "GET", "/storage/{storage}")
	reg("storage list --node", "GET", "/nodes/{node}/storage")
	reg("storage content list / backup list", "GET", "/nodes/{node}/storage/{storage}/content")
	reg("backup create", "POST", "/nodes/{node}/vzdump")

	// tasks
	reg("task list", "GET", "/nodes/{node}/tasks")
	reg("task show/wait", "GET", "/nodes/{node}/tasks/{upid}/status")
	reg("task log", "GET", "/nodes/{node}/tasks/{upid}/log")

	// PDM-specific curated
	reg("remote list/show", "GET", "/remotes/remote")
	reg("node/guest list (pdm)", "GET", "/resources/list")
}

// Classify reports the curated command for an endpoint, or ("", false) if the
// endpoint is reachable only via the raw/api escape hatch.
func Classify(method, path string) (string, bool) {
	c, ok := Curated[method+" "+path]
	return c, ok
}

// Key formats a method+path the way the registry stores it.
func Key(method, path string) string { return fmt.Sprintf("%s %s", method, path) }
