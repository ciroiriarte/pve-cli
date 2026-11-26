package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
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
	localDisks bool     // live migration with attached local disks: send with-local-disks=1
	allowed    []string // precondition allowed_nodes (nil = unrestricted)
	expected   bool     // an expected skip (templates) that doesn't fail the run
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
			"RAM (and --vcpu-ratio allocated vCPUs per core, when set). --target forces a\n" +
			"single destination (still capacity-checked unless --force).\n\n" +
			"Running VMs are live-migrated; running containers use restart migration.\n" +
			"Guests that can't move are skipped and reported: HA-managed guests (let HA\n" +
			"move them, e.g. `ha-manager crm-command node-maintenance enable <node>`),\n" +
			"VMs with local devices, local CD-ROM ISOs or local disks (unless\n" +
			"--with-local-disks), locked guests and templates.\n\n" +
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
					_ = output.Render(os.Stderr, evacTable(items, false), output.Options{Format: output.Table})
				}
				prompt := fmt.Sprintf("migrate %d guest(s) off %s (%d VMs, %d CTs) to %s", len(move), src, vms, cts, strings.Join(sortedKeys(dests), ", "))
				if n := len(items) - len(move); n > 0 {
					prompt += fmt.Sprintf("; %d guest(s) will be skipped", n)
				}
				if err := confirm(a, prompt+"?"); err != nil {
					return err
				}
				runEvacuation(ctx, p, move, o.online, parallel, timeout, restartTimeout)
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
	cmd.Flags().Float64Var(&o.vcpuRatio, "vcpu-ratio", 0, "max allocated vCPUs per physical core on a destination (0 = no limit)")
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
		eligible, short := 0, ""
		var best *evacTarget
		var bestRoom int64
		for _, n := range names {
			if it.allowed != nil && !hasString(it.allowed, n) {
				continue
			}
			eligible++
			t := targets[n]
			limit := int64(float64(t.maxMem) * o.maxMemPct / 100)
			room := limit - t.mem
			if running && !o.force {
				if t.mem+it.MaxMem > limit {
					short = fmt.Sprintf("insufficient memory: needs %s, best headroom %s (--max-mem-pct %g)", humanBytes(it.MaxMem), humanBytes(maxRoom(targets, names, it.allowed, o.maxMemPct)), o.maxMemPct)
					continue
				}
				if o.vcpuRatio > 0 && t.vcpus+it.maxCPU > t.cores*o.vcpuRatio {
					short = fmt.Sprintf("insufficient vCPU headroom: needs %g vCPUs (--vcpu-ratio %g)", it.maxCPU, o.vcpuRatio)
					continue
				}
			}
			if best == nil || room > bestRoom {
				best, bestRoom = t, room
			}
		}
		switch {
		case eligible == 0:
			it.Skip, it.Reason = true, "no destination: storage or mapped resources unavailable on the other nodes"
		case best == nil:
			it.Skip, it.Reason = true, short
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
		AllowedNodes   *[]string `json:"allowed_nodes"`
		LocalResources []string  `json:"local_resources"`
		LocalDisks     []struct {
			Volid    string `json:"volid"`
			CDROM    any    `json:"cdrom"`
			IsUnused any    `json:"is_unused"`
		} `json:"local_disks"`
	}
	if err := protocol.DecodeData(body, &pre); err != nil {
		it.Skip, it.Reason = true, "precondition check failed: "+err.Error()
		return
	}
	if len(pre.LocalResources) > 0 {
		it.Skip, it.Reason = true, "local resources: "+strings.Join(pre.LocalResources, ",")
		return
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
		it.allowed = *pre.AllowedNodes
		if it.allowed == nil {
			it.allowed = []string{}
		}
	}
}

// runEvacuation migrates the planned items, at most `parallel` at a time, each
// task-polled to completion. Results are recorded on the items.
func runEvacuation(ctx context.Context, p provider.Provider, items []*evacItem, online bool, parallel, timeout, restartTimeout int) {
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
			upid, err := migrateOne(ctx, p, it, online, timeout, restartTimeout, label)
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

func migrateOne(ctx context.Context, p provider.Provider, it *evacItem, online bool, timeout, restartTimeout int, label string) (string, error) {
	params := url.Values{"target": {it.To}}
	switch it.Mode {
	case "online":
		if online {
			params.Set("online", "1")
		}
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

func maxRoom(targets map[string]*evacTarget, names, allowed []string, pct float64) int64 {
	var best int64
	for _, n := range names {
		if allowed != nil && !hasString(allowed, n) {
			continue
		}
		t := targets[n]
		if room := int64(float64(t.maxMem)*pct/100) - t.mem; room > best {
			best = room
		}
	}
	return best
}

func hasString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
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
