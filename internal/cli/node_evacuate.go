package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/ciroiriarte/pve-cli/internal/domain"
	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
	"github.com/ciroiriarte/pve-cli/internal/task"
)

// maxEvacParallel bounds --parallel so a typo can't fire dozens of concurrent
// migrations at the cluster network.
const maxEvacParallel = 8

// cloudinitVol matches a generated cloud-init volume, which PVE recreates on
// the migration target instead of copying.
var cloudinitVol = regexp.MustCompile(`vm-\d+-cloudinit`)

// evacItem is one guest in an evacuation plan, and (after a real run) its
// outcome. It is the json/yaml contract for `pc node evacuate`: the plan (and
// the results) render as an array of these objects.
type evacItem struct {
	VMID   int              `json:"vmid" yaml:"vmid"`
	Name   string           `json:"name" yaml:"name"`
	Kind   domain.GuestKind `json:"kind" yaml:"kind"`
	Status string           `json:"status" yaml:"status"` // guest power state
	From   string           `json:"from" yaml:"from"`
	To     string           `json:"to,omitempty" yaml:"to,omitempty"`
	Mode   string           `json:"mode,omitempty" yaml:"mode,omitempty"` // online | restart | offline
	MaxMem int64            `json:"maxmem" yaml:"maxmem"`
	Skip   bool             `json:"skip" yaml:"skip"`
	Reason string           `json:"reason,omitempty" yaml:"reason,omitempty"`
	Result string           `json:"result,omitempty" yaml:"result,omitempty"` // migrated | failed | skipped (real run only)
	Error  string           `json:"error,omitempty" yaml:"error,omitempty"`
	UPID   string           `json:"upid,omitempty" yaml:"upid,omitempty"`

	maxCPU     float64
	localDisks bool              // live migration with attached local disks: send with-local-disks=1
	allowed    []string          // allowed destinations (nil = unrestricted)
	blocked    map[string]string // excluded node → why
	expected   bool              // an expected skip (templates) that doesn't fail the run
}

// evacRes is the subset of a /cluster/resources entry the planner needs.
type evacRes struct {
	Type     string  `json:"type"`
	Node     string  `json:"node"`
	Status   string  `json:"status"`
	Name     string  `json:"name"`
	VMID     int     `json:"vmid"`
	Template int     `json:"template"`
	Lock     string  `json:"lock"`
	HAState  string  `json:"hastate"`
	Mem      int64   `json:"mem"`
	MaxMem   int64   `json:"maxmem"`
	MaxCPU   float64 `json:"maxcpu"`
}

// evacTarget is a destination node with its projected usage, updated as
// guests are assigned to it.
type evacTarget struct {
	name   string
	mem    int64   // projected used memory (bytes)
	maxMem int64   // physical memory (bytes)
	vcpus  float64 // projected vCPUs allocated to running guests
	cores  float64 // physical cores
}

type evacOpts struct {
	all            bool
	target         string
	force          bool
	online         bool
	withLocalDisks bool
	maxMemPct      float64
	vcpuRatio      float64
}

func newNodeEvacuateCmd(a *app) *cobra.Command {
	var o evacOpts
	var dryRun bool
	var parallel, timeout, restartTimeout int
	cmd := &cobra.Command{
		Use:   "evacuate <node>",
		Short: "Migrate all guests off a node, choosing destinations by capacity",
		Long: "Migrates the guests on <node> to the other online cluster nodes, for\n" +
			"maintenance windows. Running guests are moved by default; --all also moves\n" +
			"stopped ones. Destinations are planned greedily, largest guest first, onto the\n" +
			"node with the most memory headroom, never exceeding --max-mem-pct of a node's\n" +
			"RAM (and --vcpu-ratio allocated vCPUs per logical CPU, when set). --target forces a\n" +
			"single destination (still capacity-checked unless --force).\n\n" +
			"Running VMs are live-migrated; running containers use restart migration.\n" +
			"Guests that can't move are skipped and reported: HA-managed guests (let HA\n" +
			"move them, e.g. `ha-manager crm-command node-maintenance enable <node>`),\n" +
			"VMs with unmapped local devices, local CD-ROM ISOs or (when running) local\n" +
			"disks without --with-local-disks, CTs with local bind/device mount points,\n" +
			"locked guests and templates. Destinations are restricted to the nodes PVE's\n" +
			"migrate preconditions allow, where a CT's storages are enabled, and (for a\n" +
			"running replicated VM) its replication targets.\n\n" +
			"--dry-run prints the plan without migrating. The exit code is non-zero when\n" +
			"any migration failed or any guest (other than a template) could not be moved.",
		Example: "  pc node evacuate pve-01 --dry-run\n" +
			"  pc node evacuate pve-01 --yes\n" +
			"  pc node evacuate pve-01 --all --target pve-02 --with-local-disks\n" +
			"  pc node evacuate pve-01 --parallel 2 -o json --yes",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if parallel < 1 || parallel > maxEvacParallel {
				return fmt.Errorf("--parallel must be between 1 and %d", maxEvacParallel)
			}
			if o.maxMemPct <= 0 || o.maxMemPct > 100 {
				return fmt.Errorf("--max-mem-pct must be in (0, 100]")
			}
			if o.vcpuRatio < 0 {
				return fmt.Errorf("--vcpu-ratio must be >= 0")
			}
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if err := ensurePVE(p, "node evacuate", "and target the cluster directly"); err != nil {
				return err
			}
			ctx := cmd.Context()
			src := args[0]
			items, err := planEvacuation(ctx, p, src, o)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				fmt.Fprintf(stderrWriter(), "no guests to evacuate on %s\n", src)
				return a.render(evacTable(items, false))
			}
			if dryRun {
				if err := a.render(evacTable(items, false)); err != nil {
					return err
				}
				return evacOutcome(src, items)
			}

			var move []*evacItem
			vms, cts := 0, 0
			dests := map[string]bool{}
			for _, it := range items {
				if it.Skip {
					continue
				}
				move = append(move, it)
				dests[it.To] = true
				if it.Kind == domain.KindVM {
					vms++
				} else {
					cts++
				}
			}
			if len(move) > 0 {
				if !a.assumeYes && isTTY() {
					// Show what is about to happen before asking.
					_ = output.Render(stderrWriter(), evacTable(items, false), output.Options{Format: output.Table})
				}
				prompt := fmt.Sprintf("migrate %d guest(s) off %s (%d VMs, %d CTs) to %s", len(move), src, vms, cts, strings.Join(sortedKeys(dests), ", "))
				if n := len(items) - len(move); n > 0 {
					prompt += fmt.Sprintf("; %d guest(s) will be skipped", n)
				}
				if o.force {
					prompt += "; capacity checks disabled (--force)"
				}
				if err := confirm(a, prompt+"?"); err != nil {
					return err
				}
				runEvacuation(ctx, p, move, parallel, timeout, restartTimeout)
			}
			for _, it := range items {
				if it.Skip {
					it.Result = "skipped"
				}
			}
			if err := a.render(evacTable(items, true)); err != nil {
				return err
			}
			return evacOutcome(src, items)
		},
	}
	cmd.Flags().BoolVar(&o.all, "all", false, "also migrate stopped guests (default: running only)")
	cmd.Flags().StringVar(&o.target, "target", "", "force a single destination node")
	cmd.Flags().BoolVar(&o.force, "force", false, "skip the capacity check (memory/vCPU limits)")
	cmd.Flags().BoolVar(&o.online, "online", true, "live-migrate running VMs (--online=false skips them)")
	cmd.Flags().BoolVar(&o.withLocalDisks, "with-local-disks", false, "migrate VMs with disks on local storage (copies them)")
	cmd.Flags().Float64Var(&o.maxMemPct, "max-mem-pct", 90, "max projected memory use of a destination, in % of its RAM")
	cmd.Flags().Float64Var(&o.vcpuRatio, "vcpu-ratio", 0, "max allocated vCPUs per logical CPU (thread) on a destination (0 = no limit)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan without migrating")
	cmd.Flags().IntVar(&parallel, "parallel", 1, fmt.Sprintf("concurrent migrations (1-%d)", maxEvacParallel))
	cmd.Flags().IntVar(&timeout, "wait-timeout", 0, "seconds to wait for each migration task (0 = no limit)")
	cmd.Flags().IntVar(&restartTimeout, "restart-timeout", 180, "seconds a running container gets to shut down for restart migration")
	return markTabular(cmd)
}

// planEvacuation enumerates the guests on src and assigns each a destination
// (or a skip reason). Items come back sorted by vmid.
func planEvacuation(ctx context.Context, p provider.Provider, src string, o evacOpts) ([]*evacItem, error) {
	body, err := p.Raw(ctx, "GET", "/cluster/resources", nil)
	if err != nil {
		return nil, err
	}
	var res []evacRes
	if err := protocol.DecodeData(body, &res); err != nil {
		return nil, err
	}

	var srcNode *evacRes
	targets := map[string]*evacTarget{}
	for i, r := range res {
		if r.Type != "node" {
			continue
		}
		if r.Node == src {
			srcNode = &res[i]
		} else if r.Status == "online" {
			targets[r.Node] = &evacTarget{name: r.Node, mem: r.Mem, maxMem: r.MaxMem, cores: r.MaxCPU}
		}
	}
	if srcNode == nil {
		return nil, fmt.Errorf("node %q not found", src)
	}
	if srcNode.Status != "online" {
		return nil, fmt.Errorf("node %q is %s; migration needs the source node online", src, srcNode.Status)
	}
	if o.target != "" {
		t, ok := targets[o.target]
		if !ok {
			return nil, fmt.Errorf("--target %q is not an online cluster node other than %s", o.target, src)
		}
		targets = map[string]*evacTarget{o.target: t}
	}

	var items []*evacItem
	for _, r := range res {
		if r.Type != "qemu" && r.Type != "lxc" {
			continue
		}
		if t, ok := targets[r.Node]; ok && r.Status == "running" {
			t.vcpus += r.MaxCPU
		}
		if r.Node != src || (!o.all && r.Status != "running") {
			continue
		}
		items = append(items, &evacItem{
			VMID: r.VMID, Name: r.Name, Kind: domain.GuestKind(r.Type), Status: r.Status,
			From: src, MaxMem: r.MaxMem, maxCPU: r.MaxCPU,
			Skip: r.Template == 1 || r.Lock != "" || r.HAState != "",
			Reason: func() string {
				switch {
				case r.Template == 1:
					return "template"
				case r.Lock != "":
					return "locked (" + r.Lock + ")"
				case r.HAState != "":
					return "HA-managed; let HA move it (ha-manager crm-command node-maintenance enable " + src + ")"
				}
				return ""
			}(),
			expected: r.Template == 1,
		})
	}
	if len(items) == 0 {
		return items, nil
	}

	// HA resources: /cluster/resources carries hastate, but the HA config is
	// authoritative (best-effort: a permission error falls back to hastate).
	ha := map[string]bool{}
	if body, err := p.Raw(ctx, "GET", "/cluster/ha/resources", nil); err == nil {
		var hr []struct {
			SID string `json:"sid"`
		}
		if protocol.DecodeData(body, &hr) == nil {
			for _, h := range hr {
				ha[h.SID] = true
			}
		}
	}
	for _, it := range items {
		if it.Skip {
			continue
		}
		prefix := "vm:"
		if it.Kind == domain.KindCT {
			prefix = "ct:"
		}
		if ha[prefix+strconv.Itoa(it.VMID)] {
			it.Skip, it.Reason = true, "HA-managed; let HA move it (ha-manager crm-command node-maintenance enable "+src+")"
		}
	}

	var cache evacCache
	targetSet := map[string]bool{}
	for n := range targets {
		targetSet[n] = true
	}
	for _, it := range items {
		if it.Skip {
			continue
		}
		if len(targets) == 0 {
			it.Skip, it.Reason = true, "no destination: no other online node"
			continue
		}
		running := it.Status == "running"
		switch {
		case it.Kind == domain.KindCT && running:
			it.Mode = "restart"
		case it.Kind == domain.KindVM && running:
			if !o.online {
				it.Skip, it.Reason = true, "running VM; live migration disabled (--online=false)"
				continue
			}
			it.Mode = "online"
		default:
			it.Mode = "offline"
		}
		if it.Kind == domain.KindVM {
			vmPrecondition(ctx, p, it, o.withLocalDisks)
		} else {
			if err := ctPrecondition(ctx, p, it, &cache, sortedKeys(targetSet)); err != nil {
				return nil, err
			}
		}
		// PVE refuses to live-migrate a replicated VM to a node that isn't one
		// of its replication targets (Qemu.pm migrate_vm, unless force).
		if !it.Skip && it.Kind == domain.KindVM && running {
			jobs, err := cache.replication(ctx, p)
			if err != nil {
				return nil, err
			}
			if tgts, ok := jobs[it.VMID]; ok {
				reasons := map[string]string{}
				for n := range targets {
					if !hasString(tgts, n) {
						reasons[n] = "not a replication target"
					}
				}
				it.restrict(tgts, reasons)
			}
		}
	}

	// Greedy placement: running guests first (they need the capacity now),
	// largest first, each onto the eligible node with the most headroom.
	order := make([]*evacItem, 0, len(items))
	for _, it := range items {
		if !it.Skip {
			order = append(order, it)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		ri, rj := order[i].Status == "running", order[j].Status == "running"
		if ri != rj {
			return ri
		}
		if order[i].MaxMem != order[j].MaxMem {
			return order[i].MaxMem > order[j].MaxMem
		}
		return order[i].VMID < order[j].VMID
	})
	names := make([]string, 0, len(targets))
	for n := range targets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, it := range order {
		running := it.Status == "running"
		var excluded, short []string
		var best *evacTarget
		var bestRoom int64
		for _, n := range names {
			if it.allowed != nil && !hasString(it.allowed, n) {
				why := it.blocked[n]
				if why == "" {
					why = "not allowed by the migration precondition"
				}
				excluded = append(excluded, n+": "+why)
				continue
			}
			t := targets[n]
			limit := int64(float64(t.maxMem) * o.maxMemPct / 100)
			room := limit - t.mem
			if running && !o.force {
				if t.mem+it.MaxMem > limit {
					short = append(short, fmt.Sprintf("%s: insufficient memory (needs %s, headroom %s at --max-mem-pct %g)",
						n, humanBytes(it.MaxMem), humanBytes(max(room, 0)), o.maxMemPct))
					continue
				}
				if o.vcpuRatio > 0 && t.vcpus+it.maxCPU > t.cores*o.vcpuRatio {
					short = append(short, fmt.Sprintf("%s: insufficient vCPU headroom (needs %g, %g of %g allocated at --vcpu-ratio %g)",
						n, it.maxCPU, t.vcpus, t.cores*o.vcpuRatio, o.vcpuRatio))
					continue
				}
			}
			if best == nil || room > bestRoom {
				best, bestRoom = t, room
			}
		}
		switch {
		case best == nil && len(short) == 0:
			it.Skip, it.Reason = true, "no allowed destination: "+strings.Join(excluded, "; ")
		case best == nil:
			it.Skip, it.Reason = true, strings.Join(append(short, excluded...), "; ")
		default:
			it.To = best.name
			best.mem += it.MaxMem
			if running {
				best.vcpus += it.maxCPU
			}
			if it.localDisks {
				it.Reason = "with local disks"
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].VMID < items[j].VMID })
	return items, nil
}

// vmPrecondition consults the qemu migrate precondition endpoint and marks the
// item skipped when it has local resources that block migration, or narrows
// its allowed destinations to the nodes PVE says can host its storage.
func vmPrecondition(ctx context.Context, p provider.Provider, it *evacItem, withLocalDisks bool) {
	body, err := p.Raw(ctx, "GET", fmt.Sprintf("/nodes/%s/qemu/%d/migrate", it.From, it.VMID), nil)
	if err != nil {
		it.Skip, it.Reason = true, "precondition check failed: "+err.Error()
		return
	}
	var pre struct {
		AllowedNodes    *[]string                 `json:"allowed_nodes"`
		NotAllowedNodes map[string]map[string]any `json:"not_allowed_nodes"`
		LocalResources  []string                  `json:"local_resources"`
		MappedInfo      map[string]map[string]any `json:"mapped-resource-info"`
		LocalDisks      []struct {
			Volid    string `json:"volid"`
			CDROM    any    `json:"cdrom"`
			IsUnused any    `json:"is_unused"`
		} `json:"local_disks"`
	}
	if err := protocol.DecodeData(body, &pre); err != nil {
		it.Skip, it.Reason = true, "precondition check failed: "+err.Error()
		return
	}
	// As QemuMigrate::prepare: local resources that are cluster mappings
	// don't block (missing mappings show up in not_allowed_nodes), but a
	// running VM can only live-migrate mapped devices flagged live-migration.
	var blocking, noLive []string
	for _, r := range pre.LocalResources {
		if _, mapped := pre.MappedInfo[r]; !mapped {
			blocking = append(blocking, r)
		}
	}
	if len(blocking) > 0 {
		it.Skip, it.Reason = true, "local resources: "+strings.Join(blocking, ",")
		return
	}
	if it.Mode == "online" {
		for _, k := range sortedMapKeys(pre.MappedInfo) {
			if !truthy(pre.MappedInfo[k]["live-migration"]) {
				noLive = append(noLive, k)
			}
		}
		if len(noLive) > 0 {
			it.Skip, it.Reason = true, "mapped resources not live-migratable: "+strings.Join(noLive, ",")
			return
		}
	}
	// Mirrors PVE's QemuMigrate rules: a cloud-init drive is regenerated on
	// the target, any other local CD-ROM blocks migration, and attached local
	// disks only need with-local-disks for a live migration (offline
	// migration copies them, as it does unused ones).
	var cdroms, attached []string
	for _, d := range pre.LocalDisks {
		switch {
		case truthy(d.CDROM) && cloudinitVol.MatchString(d.Volid):
		case truthy(d.CDROM):
			cdroms = append(cdroms, d.Volid)
		case !truthy(d.IsUnused):
			attached = append(attached, d.Volid)
		}
	}
	if len(cdroms) > 0 {
		it.Skip, it.Reason = true, "local CD-ROM ISO: "+strings.Join(cdroms, ",")
		return
	}
	if len(attached) > 0 && it.Mode == "online" {
		if !withLocalDisks {
			it.Skip, it.Reason = true, "local disks need --with-local-disks for live migration: "+strings.Join(attached, ",")
			return
		}
		it.localDisks = true
	}
	if pre.AllowedNodes != nil {
		it.restrict(*pre.AllowedNodes, notAllowedReasons(pre.NotAllowedNodes))
	}
}

// ctPrecondition checks a container the way LXC/Migrate.pm will: the LXC
// precondition endpoint (allowed-nodes, e.g. HA affinity; PVE 9+, skipped on
// older versions that don't have it), local bind/device mount points (refused
// unless marked shared), and every volume's storage being enabled on the
// target. It returns an error only for cluster-wide lookups that fail.
func ctPrecondition(ctx context.Context, p provider.Provider, it *evacItem, cache *evacCache, nodes []string) error {
	body, err := p.Raw(ctx, "GET", fmt.Sprintf("/nodes/%s/lxc/%d/migrate", it.From, it.VMID), nil)
	switch {
	case err == nil:
		var pre struct {
			AllowedNodes    *[]string                 `json:"allowed-nodes"`
			NotAllowedNodes map[string]map[string]any `json:"not-allowed-nodes"`
		}
		if err := protocol.DecodeData(body, &pre); err != nil {
			it.Skip, it.Reason = true, "precondition check failed: "+err.Error()
			return nil
		}
		if pre.AllowedNodes != nil {
			it.restrict(*pre.AllowedNodes, notAllowedReasons(pre.NotAllowedNodes))
		}
	case endpointMissing(err):
		// Older PVE without GET lxc/{vmid}/migrate: rely on the checks below.
	default:
		it.Skip, it.Reason = true, "precondition check failed: "+err.Error()
		return nil
	}

	body, err = p.Raw(ctx, "GET", fmt.Sprintf("/nodes/%s/lxc/%d/config", it.From, it.VMID), nil)
	if err != nil {
		it.Skip, it.Reason = true, "reading config failed: "+err.Error()
		return nil
	}
	var conf map[string]any
	if err := protocol.DecodeData(body, &conf); err != nil {
		it.Skip, it.Reason = true, "reading config failed: "+err.Error()
		return nil
	}
	var local, storages []string
	for _, k := range sortedMapKeys(conf) {
		if k != "rootfs" && !ctMountKey.MatchString(k) {
			continue
		}
		val, _ := conf[k].(string)
		fields := strings.Split(val, ",")
		vol := strings.TrimPrefix(fields[0], "volume=")
		if strings.HasPrefix(vol, "/") {
			if !hasString(fields[1:], "shared=1") {
				local = append(local, k)
			}
			continue
		}
		if i := strings.Index(vol, ":"); i > 0 && !hasString(storages, vol[:i]) {
			storages = append(storages, vol[:i])
		}
	}
	if len(local) > 0 {
		it.Skip, it.Reason = true, "local bind/device mount points: "+strings.Join(local, ",")
		return nil
	}
	cfg, err := cache.storage(ctx, p)
	if err != nil {
		return err
	}
	var ok []string
	reasons := map[string]string{}
	for _, n := range nodes {
		missing := ""
		for _, s := range storages {
			sc, known := cfg[s]
			if !known || sc.Disable == 1 || (sc.Nodes != "" && !hasString(strings.Split(sc.Nodes, ","), n)) {
				missing = s
				break
			}
		}
		if missing != "" {
			reasons[n] = "storage " + missing + " not available"
		} else {
			ok = append(ok, n)
		}
	}
	it.restrict(ok, reasons)
	return nil
}

// ctMountKey matches the container config keys that LXC/Migrate.pm walks
// (foreach_volume_full with unused included).
var ctMountKey = regexp.MustCompile(`^(mp|unused)\d+$`)

// endpointMissing reports an API error meaning the endpoint doesn't exist on
// this PVE version (404 / 501), as opposed to a real failure.
func endpointMissing(err error) bool {
	var apiErr *protocol.APIError
	return errors.As(err, &apiErr) && (apiErr.StatusCode == 404 || apiErr.StatusCode == 501)
}

// restrict narrows the item's allowed destinations to list (intersecting any
// earlier restriction) and records why other nodes were excluded.
func (it *evacItem) restrict(list []string, reasons map[string]string) {
	next := []string{}
	for _, n := range list {
		if it.allowed == nil || hasString(it.allowed, n) {
			next = append(next, n)
		}
	}
	it.allowed = next
	if it.blocked == nil {
		it.blocked = map[string]string{}
	}
	for n, r := range reasons {
		if it.blocked[n] == "" {
			it.blocked[n] = r
		}
	}
}

// notAllowedReasons flattens a precondition's not-allowed-nodes map
// ({node: {unavailable_storages: [...], blocking-ha-resources: [{sid,...}]}})
// into one human reason per node.
func notAllowedReasons(m map[string]map[string]any) map[string]string {
	out := map[string]string{}
	for node, info := range m {
		var parts []string
		for _, k := range sortedMapKeys(info) {
			var vals []string
			if list, ok := info[k].([]any); ok {
				for _, v := range list {
					if obj, ok := v.(map[string]any); ok {
						v = obj["sid"]
					}
					vals = append(vals, fmt.Sprint(v))
				}
			}
			parts = append(parts, strings.ReplaceAll(strings.ReplaceAll(k, "_", " "), "-", " ")+" "+strings.Join(vals, ","))
		}
		out[node] = strings.Join(parts, "; ")
	}
	return out
}

// evacCache holds the cluster-wide lookups the per-guest checks share,
// fetched at most once per plan.
type evacCache struct {
	repl map[int][]string
	stor map[string]evacStorage
}

type evacStorage struct {
	Storage string `json:"storage"`
	Nodes   string `json:"nodes"`
	Disable int    `json:"disable"`
}

// replication maps vmid → replication target nodes (/cluster/replication).
func (c *evacCache) replication(ctx context.Context, p provider.Provider) (map[int][]string, error) {
	if c.repl != nil {
		return c.repl, nil
	}
	body, err := p.Raw(ctx, "GET", "/cluster/replication", nil)
	if err != nil {
		return nil, fmt.Errorf("reading replication jobs: %w", err)
	}
	var jobs []struct {
		Guest  int    `json:"guest"`
		Target string `json:"target"`
	}
	if err := protocol.DecodeData(body, &jobs); err != nil {
		return nil, fmt.Errorf("reading replication jobs: %w", err)
	}
	c.repl = map[int][]string{}
	for _, j := range jobs {
		c.repl[j.Guest] = append(c.repl[j.Guest], j.Target)
	}
	return c.repl, nil
}

// storage maps storage id → its cluster config (/storage).
func (c *evacCache) storage(ctx context.Context, p provider.Provider) (map[string]evacStorage, error) {
	if c.stor != nil {
		return c.stor, nil
	}
	body, err := p.Raw(ctx, "GET", "/storage", nil)
	if err != nil {
		return nil, fmt.Errorf("reading storage config: %w", err)
	}
	var list []evacStorage
	if err := protocol.DecodeData(body, &list); err != nil {
		return nil, fmt.Errorf("reading storage config: %w", err)
	}
	c.stor = map[string]evacStorage{}
	for _, s := range list {
		c.stor[s.Storage] = s
	}
	return c.stor, nil
}

// runEvacuation migrates the planned items, at most `parallel` at a time, each
// task-polled to completion. Results are recorded on the items.
func runEvacuation(ctx context.Context, p provider.Provider, items []*evacItem, parallel, timeout, restartTimeout int) {
	var mu sync.Mutex
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(stderrWriter(), format, args...)
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, it := range items {
		it := it
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			label := fmt.Sprintf("migrate %s %d (%s) %s -> %s [%s]", kindLabel(it.Kind), it.VMID, it.Name, it.From, it.To, it.Mode)
			logf("→ %s\n", label)
			upid, err := migrateOne(ctx, p, it, timeout, restartTimeout, label)
			it.UPID = upid
			if err != nil {
				it.Result, it.Error = "failed", err.Error()
				logf("✘ %s: %v\n", label, err)
				return
			}
			it.Result = "migrated"
			logf("✔ %s\n", label)
		}()
	}
	wg.Wait()
}

func migrateOne(ctx context.Context, p provider.Provider, it *evacItem, timeout, restartTimeout int, label string) (string, error) {
	params := url.Values{"target": {it.To}}
	switch it.Mode {
	case "online":
		params.Set("online", "1")
	case "restart":
		params.Set("restart", "1")
		params.Set("timeout", strconv.Itoa(restartTimeout))
	}
	if it.localDisks {
		params.Set("with-local-disks", "1")
	}
	path := fmt.Sprintf("/nodes/%s/%s/%d/migrate", it.From, kindEndpoint(it.Kind), it.VMID)
	body, err := p.Raw(ctx, "POST", path, params)
	if err != nil {
		return "", err
	}
	data, err := mutationData(body, label)
	if err != nil {
		return "", err
	}
	upid, _ := data.(string)
	h, err := parseTaskArg(upid)
	if err != nil {
		return "", fmt.Errorf("unexpected migrate response: %s", strings.TrimSpace(string(body)))
	}
	_, err = task.Wait(ctx, p.TaskStatus, h, task.WaitOptions{Timeout: secs(timeout), Out: stderrWriter(), Label: label})
	return upid, err
}

// evacOutcome maps the plan/results to the exit code: failed migrations exit 5
// (task failed), unexpected skips exit 6 (the node could not be cleared).
func evacOutcome(src string, items []*evacItem) error {
	failed, skipped := 0, 0
	for _, it := range items {
		switch {
		case it.Result == "failed":
			failed++
		case it.Skip && !it.expected:
			skipped++
		}
	}
	if failed > 0 {
		return &protocol.APIError{Kind: protocol.KindTaskFailed,
			Message: fmt.Sprintf("evacuate %s: %d migration(s) failed, %d guest(s) skipped", src, failed, skipped)}
	}
	if skipped > 0 {
		return &protocol.APIError{Kind: protocol.KindPrecondition,
			Message: fmt.Sprintf("evacuate %s: %d guest(s) cannot be moved", src, skipped)}
	}
	return nil
}

func evacTable(items []*evacItem, ran bool) output.Tabular {
	raw := make([]evacItem, 0, len(items))
	t := output.Tabular{Columns: []string{"vmid", "name", "kind", "status", "from", "to", "mode", "maxmem", "action", "reason"}}
	for _, it := range items {
		raw = append(raw, *it)
		action := "migrate"
		if it.Skip {
			action = "skip"
		}
		reason := it.Reason
		if ran {
			action = it.Result
			if it.Error != "" {
				reason = it.Error
			}
		}
		to, mode := it.To, it.Mode
		if to == "" {
			to = "-"
		}
		if mode == "" || it.Skip {
			mode = "-"
		}
		t.Rows = append(t.Rows, []string{strconv.Itoa(it.VMID), it.Name, kindLabel(it.Kind), it.Status, it.From, to, mode, humanBytes(it.MaxMem), action, reason})
	}
	t.Raw = raw
	return t
}

func kindLabel(k domain.GuestKind) string {
	if k == domain.KindCT {
		return "CT"
	}
	return "VM"
}

func hasString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string { return sortedMapKeys(m) }

func sortedMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// truthy interprets a PVE boolean, which may arrive as 0/1 or true/false.
func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x == "1" || x == "true"
	case json.Number:
		return x.String() != "0"
	}
	return false
}
