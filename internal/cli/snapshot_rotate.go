package cli

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ciroiriarte/pve-cli/internal/domain"
	"github.com/ciroiriarte/pve-cli/internal/output"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
	"github.com/ciroiriarte/pve-cli/internal/task"
)

// Snapshot rotation (#24). pc-managed snapshots are named
//
//	pc-<label>-YYYYMMDD-HHMMSS   (UTC)
//
// and rotation only ever considers names that match that exact, anchored
// pattern for the selected label. Hand-made snapshots, other labels, and
// look-alikes (`pc-auto-latest`, `pc-autox-…`, `auto-…`) are never touched.
const (
	autoSnapPrefix     = "pc-"
	autoSnapTimeLayout = "20060102-150405"
	defaultSnapLabel   = "auto"
	// PVE snapshot names are [A-Za-z][A-Za-z0-9_-]* with a 40-char limit;
	// "pc-" + label + "-" + 15-char timestamp leaves 21 chars for the label.
	maxSnapLabelLen = 40 - len(autoSnapPrefix) - 1 - len(autoSnapTimeLayout)
)

var snapLabelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// validateSnapLabel checks that a rotation label yields valid PVE snapshot
// names. The prefix guarantees the leading letter.
func validateSnapLabel(label string) error {
	if label == "" {
		return fmt.Errorf("--label must not be empty")
	}
	if len(label) > maxSnapLabelLen {
		return fmt.Errorf("--label %q is too long (max %d characters; snapshot names are limited to 40)", label, maxSnapLabelLen)
	}
	if !snapLabelRe.MatchString(label) {
		return fmt.Errorf("--label %q is invalid: use letters, digits, '_' or '-', starting with a letter or digit", label)
	}
	return nil
}

// autoSnapName builds the managed snapshot name for label at t (in UTC).
func autoSnapName(label string, t time.Time) string {
	return autoSnapPrefix + label + "-" + t.UTC().Format(autoSnapTimeLayout)
}

// autoSnapPattern matches exactly the managed names of one label. It is
// anchored at both ends, so another label sharing a prefix ("auto" vs
// "auto-x" or "autox") can never match.
func autoSnapPattern(label string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(autoSnapPrefix+label) + `-[0-9]{8}-[0-9]{6}$`)
}

// rotSnap is one existing snapshot considered for rotation.
type rotSnap struct {
	Name     string
	Snaptime int64
}

// planRotation selects which of snaps to keep and delete for label: only names
// matching the label's managed pattern are considered; they are ordered oldest
// first by snaptime (name, which embeds the timestamp, breaks ties) and all
// but the newest keep are deleted. Non-matching snapshots never appear in
// either result.
func planRotation(snaps []rotSnap, label string, keep int) (kept, del []rotSnap) {
	re := autoSnapPattern(label)
	var managed []rotSnap
	for _, s := range snaps {
		if re.MatchString(s.Name) {
			managed = append(managed, s)
		}
	}
	sort.SliceStable(managed, func(i, j int) bool {
		if managed[i].Snaptime != managed[j].Snaptime {
			return managed[i].Snaptime < managed[j].Snaptime
		}
		return managed[i].Name < managed[j].Name
	})
	if keep < 0 {
		keep = 0
	}
	cut := len(managed) - keep
	if cut < 0 {
		cut = 0
	}
	return managed[cut:], managed[:cut]
}

// snapshotRotateResult is the native json/yaml object emitted by a rotation
// (decision #1). In a dry run, created/deleted are what would happen.
type snapshotRotateResult struct {
	VMID    int      `json:"vmid" yaml:"vmid"`
	Kind    string   `json:"kind" yaml:"kind"`
	Node    string   `json:"node,omitempty" yaml:"node,omitempty"`
	Remote  string   `json:"remote,omitempty" yaml:"remote,omitempty"`
	Label   string   `json:"label" yaml:"label"`
	Keep    int      `json:"keep" yaml:"keep"`
	DryRun  bool     `json:"dry_run" yaml:"dry_run"`
	Created string   `json:"created,omitempty" yaml:"created,omitempty"`
	Kept    []string `json:"kept" yaml:"kept"`
	Deleted []string `json:"deleted" yaml:"deleted"`
	Failed  []string `json:"failed,omitempty" yaml:"failed,omitempty"`
}

func rotateTable(r snapshotRotateResult) output.Tabular {
	t := output.Tabular{Columns: []string{"action", "snapshot"}, Raw: r}
	created, deleted := "created", "deleted"
	if r.DryRun {
		created, deleted = "would-create", "would-delete"
	}
	if r.Created != "" {
		t.Rows = append(t.Rows, []string{created, r.Created})
	}
	for _, n := range r.Kept {
		if n != r.Created {
			t.Rows = append(t.Rows, []string{"kept", n})
		}
	}
	for _, n := range r.Deleted {
		t.Rows = append(t.Rows, []string{deleted, n})
	}
	for _, n := range r.Failed {
		t.Rows = append(t.Rows, []string{"failed", n})
	}
	return t
}

// rotateOpts drives rotateGuestSnapshots.
type rotateOpts struct {
	label       string
	keep        int
	create      bool   // take a new snapshot before pruning
	name        string // explicit name for the new snapshot ("" = generated)
	dryRun      bool
	vmstate     bool
	description string
	timeout     int
}

// rotateGuestSnapshots optionally creates a managed snapshot (waiting for the
// task) and then deletes the label's managed snapshots beyond keep, oldest
// first. A failed create aborts before anything is deleted.
//
// An explicit --keep is the consent for deleting label-matched snapshots, so
// this never prompts (it is meant for cron); every deletion is reported on
// stderr and in the result, and --dry-run previews without mutating.
func rotateGuestSnapshots(ctx context.Context, a *app, p provider.Provider, g domain.Guest, base string, o rotateOpts) error {
	if err := validateSnapLabel(o.label); err != nil {
		return err
	}
	if o.keep < 0 {
		return fmt.Errorf("--keep must be >= 0")
	}
	kind := labelFromKind(g.Kind)
	if o.create {
		if o.keep == 0 {
			return fmt.Errorf("--keep 0 would delete the snapshot just created; use --keep >= 1, or --no-create to remove all %q snapshots", o.label)
		}
		if o.vmstate && g.Kind != domain.KindVM {
			return fmt.Errorf("--vmstate is only supported for VMs (%d is a container)", g.VMID)
		}
		if o.name == "" {
			o.name = autoSnapName(o.label, time.Now())
		} else if !autoSnapPattern(o.label).MatchString(o.name) {
			return fmt.Errorf("snapshot name %q is not managed by label %q: with --keep the name must be %s<YYYYMMDD-HHMMSS> (omit it to generate one), so rotation can never touch hand-made snapshots",
				o.name, o.label, autoSnapPrefix+o.label+"-")
		}
	}
	res := snapshotRotateResult{VMID: g.VMID, Kind: kind, Node: g.Node, Remote: g.Remote,
		Label: o.label, Keep: o.keep, DryRun: o.dryRun, Kept: []string{}, Deleted: []string{}}

	if o.create && !o.dryRun {
		params := url.Values{"snapname": {o.name}}
		desc := o.description
		if desc == "" {
			desc = fmt.Sprintf("pc auto-snapshot (label %s)", o.label)
		}
		params.Set("description", desc)
		if o.vmstate {
			params.Set("vmstate", "1")
		}
		label := fmt.Sprintf("snapshot %s %d as %s", kind, g.VMID, o.name)
		if err := mutateAndWait(ctx, p, "POST", base+"/snapshot", params, label, o.timeout); err != nil {
			return err // nothing is pruned when the create fails
		}
		fmt.Fprintf(stderrWriter(), "created snapshot %s of %s %d\n", o.name, kind, g.VMID)
	}
	if o.create {
		res.Created = o.name
	}

	snaps, err := listRotSnaps(ctx, p, base)
	if err != nil {
		if o.create && !o.dryRun {
			return fmt.Errorf("created %s but could not list snapshots to prune: %w", o.name, err)
		}
		return err
	}
	if o.create && o.dryRun {
		snaps = append(snaps, rotSnap{Name: o.name, Snaptime: time.Now().Unix()})
	}
	kept, del := planRotation(snaps, o.label, o.keep)
	for _, s := range kept {
		res.Kept = append(res.Kept, s.Name)
	}
	for _, s := range del {
		if o.dryRun {
			fmt.Fprintf(stderrWriter(), "dry-run: would delete snapshot %s of %s %d\n", s.Name, kind, g.VMID)
			res.Deleted = append(res.Deleted, s.Name)
			continue
		}
		label := fmt.Sprintf("delete snapshot %s of %s %d", s.Name, kind, g.VMID)
		if err := mutateAndWait(ctx, p, "DELETE", base+"/snapshot/"+s.Name, nil, label, o.timeout); err != nil {
			fmt.Fprintf(stderrWriter(), "%s: %v\n", label, err)
			res.Failed = append(res.Failed, s.Name)
			continue
		}
		fmt.Fprintf(stderrWriter(), "deleted snapshot %s of %s %d\n", s.Name, kind, g.VMID)
		res.Deleted = append(res.Deleted, s.Name)
	}
	if err := a.render(rotateTable(res)); err != nil {
		return err
	}
	if len(res.Failed) > 0 {
		return fmt.Errorf("%d of %d snapshot deletion(s) failed", len(res.Failed), len(del))
	}
	return nil
}

// listRotSnaps fetches a guest's snapshots, dropping PVE's synthetic "current".
func listRotSnaps(ctx context.Context, p provider.Provider, base string) ([]rotSnap, error) {
	body, err := p.Raw(ctx, "GET", base+"/snapshot", nil)
	if err != nil {
		return nil, err
	}
	var raw []map[string]any
	if err := protocol.DecodeData(body, &raw); err != nil {
		return nil, err
	}
	out := make([]rotSnap, 0, len(raw))
	for _, s := range raw {
		name := asString(s["name"])
		if name == "" || name == "current" {
			continue
		}
		t, _ := asInt64(s["snaptime"])
		out = append(out, rotSnap{Name: name, Snaptime: t})
	}
	return out, nil
}

// mutateAndWait issues a mutation and, when it returns a task id, always waits
// for the task — regardless of output format — without rendering anything on
// stdout (a spinner goes to stderr on a TTY). Rotation needs this: the prune
// must not start before the create succeeded, and deletions on one guest must
// not overlap (PVE locks the guest per snapshot task).
func mutateAndWait(ctx context.Context, p provider.Provider, method, path string, params url.Values, label string, timeout int) error {
	body, err := p.Raw(ctx, method, path, params)
	if err != nil {
		return err
	}
	data, err := mutationData(body, label)
	if err != nil {
		return err
	}
	upid, isString := data.(string)
	if data == nil || (isString && upid == "") {
		return nil
	}
	if !isString {
		return fmt.Errorf("%s: unexpected response: %s", label, strings.TrimSpace(string(body)))
	}
	h, err := parseTaskArg(upid)
	if err != nil {
		return fmt.Errorf("%s: malformed task id in response: %w", label, err)
	}
	_, err = task.Wait(ctx, p.TaskStatus, h, task.WaitOptions{
		Timeout: secs(timeout),
		Spinner: isTTY(),
		Out:     stderrWriter(),
		Label:   label,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}
