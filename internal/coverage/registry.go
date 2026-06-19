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
	// per-remote PVE reads (proxied) — pc remote <verb> <id> [<node>]
	reg("remote resources", "GET", "/pve/remotes/{remote}/resources")
	reg("remote options", "GET", "/pve/remotes/{remote}/options")
	reg("remote next-id", "GET", "/pve/remotes/{remote}/cluster-nextid")
	reg("remote updates-list", "GET", "/pve/remotes/{remote}/updates")
	reg("remote firewall", "GET", "/pve/remotes/{remote}/firewall/rules")
	reg("remote node-storage", "GET", "/pve/remotes/{remote}/nodes/{node}/storage")
	reg("remote node-status", "GET", "/pve/remotes/{remote}/nodes/{node}/status")
	reg("remote node-network", "GET", "/pve/remotes/{remote}/nodes/{node}/network")

	// PDM control-plane domains (v0.8.0)
	// ceph (read-only monitoring)
	reg("ceph clusters", "GET", "/ceph/clusters")
	reg("ceph show", "GET", "/ceph/clusters/{cluster}")
	for _, s := range []string{"status", "summary", "pools", "osd-tree", "mon", "mgr", "mds", "fs", "flags"} {
		reg("ceph "+s, "GET", "/ceph/clusters/{cluster}/"+s)
	}
	// resources (aggregate views)
	reg("resources status", "GET", "/resources/status")
	reg("resources top-entities", "GET", "/resources/top-entities")
	reg("resources subscription", "GET", "/resources/subscription")
	reg("resources location-info", "GET", "/resources/location-info")
	// sdn
	reg("sdn zones", "GET", "/sdn/zones")
	reg("sdn vnets", "GET", "/sdn/vnets")
	reg("sdn controllers", "GET", "/sdn/controllers")
	reg("sdn create-zone", "POST", "/sdn/zones")
	reg("sdn create-vnet", "POST", "/sdn/vnets")
	// access (users, tokens, roles, acl, realms)
	reg("access user list", "GET", "/access/users")
	reg("access user create", "POST", "/access/users")
	reg("access user show", "GET", "/access/users/{userid}")
	reg("access user delete", "DELETE", "/access/users/{userid}")
	reg("access token list", "GET", "/access/users/{userid}/token")
	reg("access token show", "GET", "/access/users/{userid}/token/{token-name}")
	reg("access token create", "POST", "/access/users/{userid}/token/{token-name}")
	reg("access token delete", "DELETE", "/access/users/{userid}/token/{token-name}")
	reg("access roles", "GET", "/access/roles")
	reg("access permissions", "GET", "/access/permissions")
	reg("access acl list", "GET", "/access/acl")
	reg("access acl set", "PUT", "/access/acl")
	reg("access realm list", "GET", "/access/domains")
	reg("access realm sync", "POST", "/access/domains/{realm}/sync")
	reg("access tfa", "GET", "/access/tfa")
	// pbs
	reg("pbs remotes", "GET", "/pbs/remotes")
	reg("pbs show", "GET", "/pbs/remotes/{remote}")
	reg("pbs status", "GET", "/pbs/remotes/{remote}/status")
	reg("pbs datastore list", "GET", "/pbs/remotes/{remote}/datastore")
	reg("pbs datastore show", "GET", "/pbs/remotes/{remote}/datastore/{datastore}")
	reg("pbs datastore namespaces", "GET", "/pbs/remotes/{remote}/datastore/{datastore}/namespaces")
	reg("pbs datastore snapshots", "GET", "/pbs/remotes/{remote}/datastore/{datastore}/snapshots")
	// subscription
	reg("subscription node-status", "GET", "/subscriptions/node-status")
	reg("subscription key list", "GET", "/subscriptions/keys")
	reg("subscription key add", "POST", "/subscriptions/keys")
	reg("subscription key show", "GET", "/subscriptions/keys/{key}")
	reg("subscription key remove", "DELETE", "/subscriptions/keys/{key}")
	// server config (realms, acme, certificate, notes, views)
	for _, t := range []string{"ad", "ldap", "openid"} {
		reg("server realm list", "GET", "/config/access/"+t)
		reg("server realm create", "POST", "/config/access/"+t)
		reg("server realm show", "GET", "/config/access/"+t+"/{realm}")
		reg("server realm delete", "DELETE", "/config/access/"+t+"/{realm}")
	}
	reg("server acme accounts", "GET", "/config/acme/account")
	reg("server acme plugins", "GET", "/config/acme/plugins")
	reg("server acme directories", "GET", "/config/acme/directories")
	reg("server acme tos", "GET", "/config/acme/tos")
	reg("server certificate", "GET", "/config/certificate")
	reg("server notes", "GET", "/config/notes")
	reg("server notes --set", "PUT", "/config/notes")
	reg("server view list", "GET", "/config/views")
	reg("server view show", "GET", "/config/views/{id}")
	reg("server view create", "POST", "/config/views")
	reg("server view delete", "DELETE", "/config/views/{id}")
	// server node (PDM appliance host, read-only)
	reg("server node status", "GET", "/nodes/{node}/status")
	reg("server node time", "GET", "/nodes/{node}/time")
	reg("server node dns", "GET", "/nodes/{node}/dns")
	reg("server node network", "GET", "/nodes/{node}/network")
	reg("server node apt-versions", "GET", "/nodes/{node}/apt/versions")
	reg("server node apt-updates", "GET", "/nodes/{node}/apt/update")
	reg("server node subscription", "GET", "/nodes/{node}/subscription")
	reg("server node certificates", "GET", "/nodes/{node}/certificates/info")
	// auto-install
	reg("auto-install installations", "GET", "/auto-install/installations")
	reg("auto-install prepared", "GET", "/auto-install/prepared")
	reg("auto-install prepared-show", "GET", "/auto-install/prepared/{id}")
	reg("auto-install tokens", "GET", "/auto-install/tokens")
	reg("auto-install installation-delete", "DELETE", "/auto-install/installations/{uuid}")
	reg("auto-install prepared-delete", "DELETE", "/auto-install/prepared/{id}")
	reg("auto-install token-delete", "DELETE", "/auto-install/tokens/{id}")
}

// Classify reports the curated command for an endpoint, or ("", false) if the
// endpoint is reachable only via the raw/api escape hatch.
func Classify(method, path string) (string, bool) {
	c, ok := Curated[method+" "+path]
	return c, ok
}
