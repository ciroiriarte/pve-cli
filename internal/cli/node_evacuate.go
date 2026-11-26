package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	Result string           `json:"result,omitempty" yaml:"result,omitempty"` // migrated | failed | unknown | not-attempted | skipped (real run only)
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
	cores  float64 // logical CPUs (maxcpu)
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
			"node with the most memory headroom. A running guest is never placed past\n" +
			"--max-mem-pct of a node's RAM (or --vcpu-ratio allocated vCPUs per logical CPU,\n" +
			"when set). Stopped guests (--all) use no RAM until started, so they are placed\n" +
			"after the running ones without a capacity check: check the destination before\n" +
			"starting them. --target forces a single destination (running guests still\n" +
			"capacity-checked unless --force).\n\n" +
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
	cmd.Flags().BoolVar(&o.force, "force", false, "skip the capacity check for running guests (memory/vCPU limits)")
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
		// Every guest on src is enumerated; without --all the running-only
		// filter runs after the preconditions refresh the live state.
		if r.Node != src {
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

	// Live power state from the source node itself (GET /nodes/<src>/qemu
	// and /lxc report vmstatus, the same as status/current), since
	// /cluster/resources can lag by several seconds. It decides the
	// running-only filter, before any per-guest precondition, so stopped
	// guests cost nothing in default mode. Fail closed: if a list call
	// fails, a guest of that kind that /cluster/resources shows as stopped
	// stays as a reported skip in default mode, since it may be running.
	live := map[domain.GuestKind]map[int]string{}
	liveErr := map[domain.GuestKind]error{}
	for _, k := range []domain.GuestKind{domain.KindVM, domain.KindCT} {
		live[k], liveErr[k] = listLiveStatus(ctx, p, src, k)
	}
	kept := items[:0]
	for _, it := range items {
		var unknown string
		if err := liveErr[it.Kind]; err != nil {
			unknown = "listing " + kindEndpoint(it.Kind) + " guests on " + src + " failed: " + err.Error()
		} else {
			st, ok := live[it.Kind][it.VMID]
			switch {
			case !ok:
				continue // no longer on src (moved or deleted since the inventory)
			case st == "running" || st == "stopped":
				it.Status = st
				if o.all || st == "running" {
					kept = append(kept, it)
				}
				continue
			default:
				unknown = fmt.Sprintf("live status %q on %s", st, src)
			}
		}
		// Unconfirmed: keep the /cluster/resources state; in default mode a
		// guest it shows as stopped may be running, so it stays as a skip.
		switch {
		case o.all || it.Status == "running":
		case it.expected:
			continue // a stopped template can't be running
		case !it.Skip:
			it.Skip, it.Reason = true, "power state unknown ("+unknown+")"
		}
		kept = append(kept, it)
	}
	items = kept
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
	stoppedNow := map[*evacItem]bool{} // listed running, precondition says stopped
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
		// Initial mode from /cluster/resources; the preconditions below
		// refresh it from the live running flag (and apply --online=false).
		running := it.Status == "running"
		switch {
		case it.Kind == domain.KindCT && running:
			it.Mode = "restart"
		case it.Kind == domain.KindVM && running:
			it.Mode = "online"
		default:
			it.Mode = "offline"
		}
		if it.Kind == domain.KindVM {
			if err := vmPrecondition(ctx, p, it, o.online, o.withLocalDisks, &cache); err != nil {
				return nil, err
			}
		} else {
			if err := ctPrecondition(ctx, p, it, &cache, sortedKeys(targetSet)); err != nil {
				return nil, err
			}
		}
		if !o.all && it.Status != "running" {
			stoppedNow[it] = true
			continue
		}
		// PVE refuses to live-migrate a replicated VM to a node that isn't one
		// of its replication targets (Qemu.pm migrate_vm, unless force).
		if !it.Skip && it.Kind == domain.KindVM && it.Status == "running" {
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

	// Default mode is running-only: a guest the precondition found stopped
	// leaves the plan like one stopped at listing (not planned, not a skip).
	if len(stoppedNow) > 0 {
		kept := items[:0]
		for _, it := range items {
			if !stoppedNow[it] {
				kept = append(kept, it)
			}
		}
		items = kept
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
	for _, it := range items {
		if it.Skip {
			it.Mode = "" // no migration, no mode: same in default and --all
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].VMID < items[j].VMID })
	return items, nil
}

// vmPrecondition consults the qemu migrate precondition endpoint and marks the
// item skipped when it has local resources that block migration, or narrows
// its allowed destinations to the nodes PVE says can host its storage.
func vmPrecondition(ctx context.Context, p provider.Provider, it *evacItem, online, withLocalDisks bool, cache *evacCache) error {
	body, err := p.Raw(ctx, "GET", fmt.Sprintf("/nodes/%s/qemu/%d/migrate", it.From, it.VMID), nil)
	if err != nil {
		it.Skip, it.Reason = true, "precondition check failed: "+err.Error()
		return nil
	}
	var pre struct {
		Running         any                       `json:"running"`
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
		return nil
	}
	// Running-state policy uses the precondition's live flag, not the
	// possibly stale /cluster/resources status.
	it.applyRunning(pre.Running)
	if it.Status == "running" && !online {
		it.Skip, it.Reason = true, "running VM; live migration disabled (--online=false)"
		return nil
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
		return nil
	}
	if it.Mode == "online" {
		for _, k := range sortedMapKeys(pre.MappedInfo) {
			if !truthy(pre.MappedInfo[k]["live-migration"]) {
				noLive = append(noLive, k)
			}
		}
		if len(noLive) > 0 {
			it.Skip, it.Reason = true, "mapped resources not live-migratable: "+strings.Join(noLive, ",")
			return nil
		}
	}
	// Mirrors PVE's QemuMigrate rules: a cloud-init drive is regenerated on
	// the target, any other local CD-ROM blocks migration, and attached local
	// disks only need with-local-disks for a live migration (offline
	// migration copies them, as it does unused ones).
	var cdroms, attached, copied []string
	for _, d := range pre.LocalDisks {
		switch {
		case truthy(d.CDROM) && cloudinitVol.MatchString(d.Volid):
		case truthy(d.CDROM):
			cdroms = append(cdroms, d.Volid)
		case !truthy(d.IsUnused):
			attached = append(attached, d.Volid)
			copied = append(copied, d.Volid)
		default:
			copied = append(copied, d.Volid)
		}
	}
	// Copied disks land on the same storage id on the target, which must
	// allow content type images (target_storage_check_available).
	if len(copied) > 0 {
		cfg, err := cache.storage(ctx, p)
		if err != nil {
			return err
		}
		for _, v := range copied {
			s, _, _ := strings.Cut(v, ":")
			if sc, known := cfg[s]; known && !sc.hasContent("images") {
				it.Skip, it.Reason = true, "storage "+s+" does not allow content type images ("+v+")"
				return nil
			}
		}
	}
	if len(cdroms) > 0 {
		it.Skip, it.Reason = true, "local CD-ROM ISO: "+strings.Join(cdroms, ",")
		return nil
	}
	if len(attached) > 0 && it.Mode == "online" {
		if !withLocalDisks {
			it.Skip, it.Reason = true, "local disks need --with-local-disks for live migration: "+strings.Join(attached, ",")
			return nil
		}
		it.localDisks = true
	}
	if pre.AllowedNodes != nil {
		it.restrict(*pre.AllowedNodes, notAllowedReasons(pre.NotAllowedNodes))
	}
	return nil
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
			Running         any                       `json:"running"`
			AllowedNodes    *[]string                 `json:"allowed-nodes"`
			NotAllowedNodes map[string]map[string]any `json:"not-allowed-nodes"`
		}
		if err := protocol.DecodeData(body, &pre); err != nil {
			it.Skip, it.Reason = true, "precondition check failed: "+err.Error()
			return nil
		}
		it.applyRunning(pre.Running)
		if pre.AllowedNodes != nil {
			it.restrict(*pre.AllowedNodes, notAllowedReasons(pre.NotAllowedNodes))
		}
	case endpointMissing(err):
		// Older PVE without GET lxc/{vmid}/migrate: rely on the checks below.
	default:
		it.Skip, it.Reason = true, "precondition check failed: "+err.Error()
		return nil
	}

	// LXC/Migrate.pm: prepare() walks the active config (current=1): it
	// refuses non-shared bind/device mounts and requires each volume's
	// storage enabled on the target with content rootdir. sync_volumes()
	// then also checks pending volumes (the default view, pending applied)
	// on non-shared storage for target availability.
	var local, storages, activeStor []string
	for _, query := range []url.Values{{"current": {"1"}}, nil} {
		body, err := p.Raw(ctx, "GET", fmt.Sprintf("/nodes/%s/lxc/%d/config", it.From, it.VMID), query)
		if err != nil {
			it.Skip, it.Reason = true, "reading config failed: "+err.Error()
			return nil
		}
		var conf map[string]any
		if err := protocol.DecodeData(body, &conf); err != nil {
			it.Skip, it.Reason = true, "reading config failed: "+err.Error()
			return nil
		}
		active := query != nil
		for _, k := range sortedMapKeys(conf) {
			if k != "rootfs" && !ctMountKey.MatchString(k) {
				continue
			}
			val, _ := conf[k].(string)
			vol, shared := parseCTMount(val)
			if strings.HasPrefix(vol, "/") {
				// prepare() refuses non-shared bind/device mounts of the
				// active config only; the pending walk ignores them.
				if active && !shared && !hasString(local, k) {
					local = append(local, k)
				}
				continue
			}
			// A pending new-disk spec (local-lvm:8) doesn't exist yet;
			// PVE skips it ($NEW_DISK_RE).
			if !active && newDiskSpec.MatchString(vol) {
				continue
			}
			if i := strings.Index(vol, ":"); i > 0 {
				s := vol[:i]
				if active && !hasString(activeStor, s) {
					activeStor = append(activeStor, s)
				}
				if !hasString(storages, s) {
					storages = append(storages, s)
				}
			}
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
	// prepare() requires content type rootdir on every active volume's
	// storage (same cluster-wide config on source and target).
	for _, s := range activeStor {
		if sc, known := cfg[s]; known && !sc.hasContent("rootdir") {
			it.Skip, it.Reason = true, "storage "+s+" does not allow content type rootdir"
			return nil
		}
	}
	var ok []string
	reasons := map[string]string{}
	for _, n := range nodes {
		missing := ""
		for _, s := range storages {
			// A storage absent from /storage is one this token can't see
			// (no Datastore.Audit): leave that decision to PVE.
			sc, known := cfg[s]
			if known && sc.Shared == 1 && !hasString(activeStor, s) {
				continue // pending-only volume on shared storage: not checked by PVE
			}
			if known && (sc.Disable == 1 || (sc.Nodes != "" && !hasString(strings.Split(sc.Nodes, ","), n))) {
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

// parseCTMount splits a CT mount point value ("<volume>,mp=/x,shared=1" or
// "mp=/x,volume=<volume>", options in any order) into its volume and whether
// it is marked shared.
func parseCTMount(val string) (vol string, shared bool) {
	for i, f := range strings.Split(val, ",") {
		k, v, hasEq := strings.Cut(f, "=")
		switch {
		case !hasEq && i == 0:
			vol = f
		case k == "volume":
			vol = v
		case k == "shared":
			switch strings.ToLower(v) {
			case "1", "yes", "on", "true":
				shared = true
			}
		}
	}
	return vol, shared
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

// listLiveStatus returns vmid → live status ("running", "stopped", ...) for
// the guests of one kind on node, from GET /nodes/<node>/<kind>.
func listLiveStatus(ctx context.Context, p provider.Provider, node string, k domain.GuestKind) (map[int]string, error) {
	body, err := p.Raw(ctx, "GET", fmt.Sprintf("/nodes/%s/%s", node, kindEndpoint(k)), nil)
	if err != nil {
		return nil, err
	}
	var list []struct {
		VMID   int    `json:"vmid"`
		Status string `json:"status"`
	}
	if err := protocol.DecodeData(body, &list); err != nil {
		return nil, err
	}
	out := make(map[int]string, len(list))
	for _, g := range list {
		out[g.VMID] = g.Status
	}
	return out, nil
}

// applyRunning updates the guest's power state and migration mode from a
// precondition's live `running` flag. /cluster/resources can lag by several
// seconds (e.g. right after a restart migration), and planning a running CT as
// offline makes PVE refuse the migration.
func (it *evacItem) applyRunning(v any) {
	if v == nil {
		return
	}
	running := truthy(v)
	if running == (it.Status == "running") {
		return
	}
	switch {
	case running && it.Kind == domain.KindCT:
		it.Status, it.Mode = "running", "restart"
	case running:
		it.Status, it.Mode = "running", "online"
	default:
		it.Status, it.Mode = "stopped", "offline"
	}
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
	Content string `json:"content"`
	Disable int    `json:"disable"`
	Shared  int    `json:"shared"`
}

// hasContent reports whether the storage allows content type c. An empty
// content list (not reported) is treated as allowed.
func (s evacStorage) hasContent(c string) bool {
	return s.Content == "" || hasString(strings.Split(s.Content, ","), c)
}

// newDiskSpec matches a pending new-disk spec such as "local-lvm:8"
// (PVE::LXC::NEW_DISK_RE / qemu's equivalent).
var newDiskSpec = regexp.MustCompile(`^[^:\s]+:\d+(\.\d+)?$`)

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
//
// A wait that ends without a terminal task status (--wait-timeout, a
// cancelled context, a status-poll error) may leave that migration running in
// PVE while its slot frees up here, so dispatching more could exceed
// --parallel. After one, no new migration starts: the rest are marked
// not-attempted.
func runEvacuation(ctx context.Context, p provider.Provider, items []*evacItem, parallel, timeout, restartTimeout int) {
	var mu sync.Mutex
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(stderrWriter(), format, args...)
	}
	const notAttempted = "not attempted: an earlier migration ended without a final task status"
	var gate evacGate
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, it := range items {
		it := it
		sem <- struct{}{}
		if gate.isHalted() {
			<-sem
			it.Result, it.Error = "not-attempted", notAttempted
			logf("- migrate %s %d (%s): not attempted (an earlier migration ended without a final task status)\n", kindLabel(it.Kind), it.VMID, it.Name)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if evacBeforePost != nil {
				evacBeforePost(it.VMID)
			}
			label := fmt.Sprintf("migrate %s %d (%s) %s -> %s [%s]", kindLabel(it.Kind), it.VMID, it.Name, it.From, it.To, it.Mode)
			upid, posted, startUnknown, err := migrateOne(ctx, p, it, &gate, timeout, restartTimeout, label)
			if !posted {
				// A worker admitted before the halt never POSTs after it.
				it.Result, it.Error = "not-attempted", notAttempted
				logf("- %s: not attempted (an earlier migration ended without a final task status)\n", label)
				return
			}
			logf("→ %s\n", label)
			it.UPID = upid
			if err != nil {
				it.Result, it.Error = "failed", err.Error()
				// Contract: once a task exists, only a terminal task status
				// settles it, and task.Wait reports a non-OK terminal status
				// as KindTaskFailed. Any other wait error (timeout, poll
				// failure, auth expiry) leaves the migration possibly running.
				// Before a task id exists, startUnknown marks a POST whose
				// outcome is unknown (no answer, or an unreadable success).
				var apiErr *protocol.APIError
				switch {
				case startUnknown: // the gate was halted with the POST
					it.Result = "unknown"
					it.Error = fmt.Sprintf("no usable answer to the migrate request; the migration may have started in PVE (check `pc task list --node %s`): %v", it.From, err)
				case upid != "" && !(errors.As(err, &apiErr) && apiErr.Kind == protocol.KindTaskFailed):
					it.Result = "unknown"
					it.Error = "no final task status; the migration may still be running in PVE (check `pc task show " + upid + "`): " + err.Error()
					gate.halt()
				}
				logf("✘ %s: %v\n", label, err)
				return
			}
			it.Result = "migrated"
			logf("✔ %s\n", label)
		}()
	}
	wg.Wait()
}

// evacGate serializes the halt flag with migration POSTs: the check and the
// POST happen under one lock, and halt() takes the same lock, so no POST can
// start once a halt is recorded.
type evacGate struct {
	mu     sync.Mutex
	halted bool
}

func (g *evacGate) isHalted() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.halted
}

func (g *evacGate) halt() {
	g.mu.Lock()
	g.halted = true
	g.mu.Unlock()
	if evacHalted != nil {
		evacHalted()
	}
}

// post runs fn unless halted; ran reports whether it did.
// post runs fn unless halted; ran reports whether it did. fn returns whether
// its POST's outcome is unknown; if so the halt is recorded before the lock is
// released, so no other worker can POST between that answer and the halt.
func (g *evacGate) post(fn func() (unknown bool)) (ran bool) {
	g.mu.Lock()
	if g.halted {
		g.mu.Unlock()
		return false
	}
	halt := fn()
	if halt {
		g.halted = true
	}
	g.mu.Unlock()
	if halt && evacHalted != nil {
		evacHalted()
	}
	return true
}

// Test seams for deterministic concurrency tests (nil in production). They
// are package globals: tests that set them must not use t.Parallel().
var (
	evacBeforePost func(vmid int)
	evacAfterPost  func(vmid int) // inside the gate, after the POST answer
	evacHalted     func()
)

// migratePostRejected reports whether a migrate POST error is PVE's own
// refusal (an HTTP error answer), so no task was started. Anything else may
// follow a request PVE already acted on: a transport error (POSTs are never
// retried, and the connection can drop after the request was sent), a
// cancelled request, or a gateway status (502/503/504, pveproxy's 595/596;
// the transport also treats 503 as transient).
func migratePostRejected(err error) bool {
	var apiErr *protocol.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode == 0 {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 595, 596:
		return false
	}
	return true
}

// migrateOne POSTs one migration and waits for its task. It returns the task
// id (once known), whether the POST ran (false: halted before it), whether the
// POST's outcome is unknown (startUnknown: the migration may have started but
// no task id was obtained), and the error.
func migrateOne(ctx context.Context, p provider.Provider, it *evacItem, gate *evacGate, timeout, restartTimeout int, label string) (upid string, posted, startUnknown bool, err error) {
	params := url.Values{"target": {it.To}}
	switch it.Mode {
	case "online":
		params.Set("online", "1")
	}
	// restart only takes effect for a running CT (LXC/Migrate.pm), so send it
	// for every CT: one that started after planning is restart-migrated
	// instead of refused.
	if it.Kind == domain.KindCT {
		params.Set("restart", "1")
		params.Set("timeout", strconv.Itoa(restartTimeout))
	}
	if it.localDisks {
		params.Set("with-local-disks", "1")
	}
	path := fmt.Sprintf("/nodes/%s/%s/%d/migrate", it.From, kindEndpoint(it.Kind), it.VMID)
	var h protocol.TaskHandle
	posted = gate.post(func() bool {
		body, perr := p.Raw(ctx, "POST", path, params)
		if evacAfterPost != nil {
			evacAfterPost(it.VMID)
		}
		upid, h, startUnknown, err = readMigratePost(body, perr, label)
		return startUnknown
	})
	if !posted {
		return "", false, false, nil
	}
	if err != nil {
		return "", true, startUnknown, err
	}
	_, err = task.Wait(ctx, p.TaskStatus, h, task.WaitOptions{Timeout: secs(timeout), Out: stderrWriter(), Label: label})
	return upid, true, false, err
}

// readMigratePost classifies a migrate POST answer. A PVE error answer is a
// refusal (no task). Any other error, or a success answer (PVE accepted the
// request) whose task id can't be read, leaves the outcome unknown.
func readMigratePost(body []byte, err error, label string) (upid string, h protocol.TaskHandle, unknown bool, _ error) {
	if err != nil {
		return "", h, !migratePostRejected(err), err
	}
	data, err := mutationData(body, label)
	if err != nil {
		return "", h, true, err
	}
	upid, _ = data.(string)
	h, err = parseTaskArg(upid)
	if err != nil {
		return "", h, true, fmt.Errorf("unexpected migrate response: %s", strings.TrimSpace(string(body)))
	}
	return upid, h, false, nil
}

// evacOutcome maps the plan/results to the exit code: failed migrations exit 5
// (task failed), unexpected skips exit 6 (the node could not be cleared).
func evacOutcome(src string, items []*evacItem) error {
	failed, skipped := 0, 0
	for _, it := range items {
		switch {
		case it.Result == "failed" || it.Result == "unknown" || it.Result == "not-attempted":
			failed++
		case it.Skip && !it.expected:
			skipped++
		}
	}
	if failed > 0 {
		return &protocol.APIError{Kind: protocol.KindTaskFailed,
			Message: fmt.Sprintf("evacuate %s: %d migration(s) failed or not completed, %d guest(s) skipped", src, failed, skipped)}
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
