// Package coverage records which API endpoints the curated pc commands cover,
// so coverage vs the full schema is measurable (not guessed). Endpoints not
// listed here are reachable only via `pc raw` / `pc api`.
package coverage

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
	reg("vm create", "POST", "/nodes/{node}/qemu") // vmid is a body param, not a path segment
	reg("ct create", "POST", "/nodes/{node}/lxc")
	regGuest("show/config", "GET", "/config") // also config (no --set)
	regGuest("config --set", "PUT", "/config")
	regGuest("clone", "POST", "/clone")
	regGuest("delete", "DELETE", "")
	regGuest("migrate", "POST", "/migrate")
	for _, a := range []string{"start", "stop", "shutdown", "reboot", "suspend", "resume"} {
		regGuest(a, "POST", "/status/"+a)
	}
	regGuest("snapshot list/create", "GET", "/snapshot")
	regGuest("snapshot create", "POST", "/snapshot")
	regGuest("snapshot delete", "DELETE", "/snapshot/{snapname}")
	regGuest("snapshot rollback", "POST", "/snapshot/{snapname}/rollback")
	regGuest("status", "GET", "/status/current")
	regGuest("pending", "GET", "/pending")
	regGuest("rrddata", "GET", "/rrddata")
	regGuest("firewall rules", "GET", "/firewall/rules")
	regGuest("firewall options", "GET", "/firewall/options")
	regGuest("console", "POST", "/termproxy")
	regGuest("console", "GET", "/vncwebsocket")

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
	reg("remote add", "POST", "/remotes/remote")
	reg("remote update", "PUT", "/remotes/remote/{id}")
	reg("remote remove", "DELETE", "/remotes/remote/{id}")
	reg("remote updates", "GET", "/remotes/updates/summary")
	reg("node/guest list (pdm)", "GET", "/resources/list")
	// PDM proxied guest operations (power, snapshot, migrate, monitor) — the
	// same suffixes the curated commands build on the PVE provider, under the
	// /pve/remotes/{remote} proxy.
	for _, kind := range []string{"qemu", "lxc"} {
		gp := "/pve/remotes/{remote}/" + kind + "/{vmid}"
		for _, a := range []string{"start", "stop", "shutdown", "resume"} {
			reg("vm/ct "+a+" (pdm)", "POST", gp+"/"+a)
		}
		reg("vm/ct show (pdm)", "GET", gp+"/config")
		reg("vm/ct status (pdm)", "GET", gp+"/status")
		reg("vm/ct pending (pdm)", "GET", gp+"/pending")
		reg("vm/ct rrddata (pdm)", "GET", gp+"/rrddata")
		reg("vm/ct migrate (pdm)", "POST", gp+"/migrate")
		reg("vm/ct firewall rules (pdm)", "GET", gp+"/firewall/rules")
		reg("vm/ct firewall options (pdm)", "GET", gp+"/firewall/options")
		reg("vm/ct snapshot list (pdm)", "GET", gp+"/snapshot")
		reg("vm/ct snapshot create (pdm)", "POST", gp+"/snapshot")
		reg("vm/ct snapshot delete (pdm)", "DELETE", gp+"/snapshot/{snapname}")
		reg("vm/ct snapshot rollback (pdm)", "POST", gp+"/snapshot/{snapname}/rollback")
	}
	reg("remote cluster-status", "GET", "/pve/remotes/{remote}/cluster-status")
	reg("task list (pdm)", "GET", "/pve/remotes/{remote}/tasks")
	reg("task show/wait (pdm)", "GET", "/pve/remotes/{remote}/tasks/{upid}/status")
	reg("task log (pdm)", "GET", "/pve/remotes/{remote}/tasks/{upid}/log")
}

// Classify reports the curated command for an endpoint, or ("", false) if the
// endpoint is reachable only via the raw/api escape hatch.
func Classify(method, path string) (string, bool) {
	c, ok := Curated[method+" "+path]
	return c, ok
}
