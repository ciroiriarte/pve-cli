package cli

import (
	"context"
	"errors"
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

// Snapshot rotation (#24). A pc-managed snapshot is named
//
//	pc-<label>-YYYYMMDD-HHMMSS   (UTC)
//
// AND carries the ownership marker "[pc-rotate:<label>]" in its description.
// Rotation only ever considers snapshots that have both, for the selected
// label: hand-made snapshots (even ones named like pc-auto-20260101-000000),
// other labels, and look-alikes are never touched. Removing the marker from a
// description makes a snapshot unmanaged, which is the safe direction.
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

// snapMarker is the ownership marker written into a managed snapshot's
// description. The closing bracket makes it label-exact ("auto" vs "auto-x").
func snapMarker(label string) string { return "[pc-rotate:" + label + "]" }

// hasMarker reports whether desc ends with label's ownership marker, ignoring
// trailing whitespace/newlines (PVE stores descriptions with a trailing "\n").
// A marker followed by other text does not count.
func hasMarker(desc, label string) bool {
	return strings.HasSuffix(strings.TrimRight(desc, " \t\r\n"), snapMarker(label))
}

// managedDescription appends the ownership marker to the user's description
// (or a default text), so the marker is present even with --description. A
// description that already ends with the marker is kept as is.
func managedDescription(desc, label string) string {
	desc = strings.TrimSpace(desc)
	if hasMarker(desc, label) {
		return desc
	}
	if desc == "" {
		desc = "pc auto-snapshot"
	}
	return desc + " " + snapMarker(label)
}

// autoSnapPattern matches exactly the managed names of one label. It is
// anchored at both ends, so another label sharing a prefix ("auto" vs
// "auto-x" or "autox") can never match.
func autoSnapPattern(label string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(autoSnapPrefix+label) + `-[0-9]{8}-[0-9]{6}$`)
}

// rotSnap is one existing snapshot considered for rotation.
type rotSnap struct {
	Name        string
	Snaptime    int64
	Description string
}

// isManaged reports whether s is a pc-managed snapshot of label: it needs both
// the anchored name and the ownership marker at the end of its description.
func isManaged(s rotSnap, label string) bool {
	return autoSnapPattern(label).MatchString(s.Name) && hasMarker(s.Description, label)
}

// planRotation selects which of the existing snaps to keep and delete for
// label: only managed snapshots (isManaged) are considered; they are ordered
// oldest first by snaptime (name, which embeds the timestamp, breaks ties) and
// all but the newest keep are deleted. Unmanaged snapshots never appear in
// either result. A snapshot about to be created is not part of snaps: the
// caller pins it as kept and passes keep-1, so it can never be pruned, even
// when another managed snapshot has a later (clock-skewed) snaptime.
func planRotation(snaps []rotSnap, label string, keep int) (kept, del []rotSnap) {
	var managed []rotSnap
	for _, s := range snaps {
		if isManaged(s, label) {
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
	// NotAttempted lists deletions skipped after a delete task timed out.
	NotAttempted []string `json:"not_attempted,omitempty" yaml:"not_attempted,omitempty"`
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
	for _, n := range r.NotAttempted {
		t.Rows = append(t.Rows, []string{"not-attempted", n})
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
	timeout     int // seconds to wait per task (0 = no limit)
}

// rotateGuestSnapshots plans the prune first, confirms it (decision #2), then
// optionally creates a managed snapshot (waiting for the task) and deletes the
// label's managed snapshots beyond keep, oldest first. The new snapshot is
// pinned as kept and counts toward keep. A refused confirmation or a failed
// create changes or prunes nothing; --dry-run never prompts or mutates.
func rotateGuestSnapshots(ctx context.Context, a *app, p provider.Provider, g domain.Guest, base string, o rotateOpts) error {
	if err := validateSnapLabel(o.label); err != nil {
		return err
	}
	if o.keep < 0 {
		return fmt.Errorf("--keep must be >= 0")
	}
	if o.vmstate && g.Kind != domain.KindVM {
		return fmt.Errorf("--vmstate is only supported for VMs (%d is a container)", g.VMID)
	}
	if !o.create && (o.description != "" || o.vmstate) {
		return fmt.Errorf("--description and --vmstate apply to the new snapshot; they cannot be used with --no-create")
	}
	kind := labelFromKind(g.Kind)
	if o.create {
		if o.keep == 0 {
			return fmt.Errorf("--keep 0 would delete the snapshot just created; use --keep >= 1, or --no-create to remove all %q snapshots", o.label)
		}
		if o.name == "" {
			o.name = autoSnapName(o.label, time.Now())
		} else if !autoSnapPattern(o.label).MatchString(o.name) {
			return fmt.Errorf("snapshot name %q is not managed by label %q: with --keep the name must be %s<YYYYMMDD-HHMMSS> (omit it to generate one), so rotation can never touch hand-made snapshots",
				o.name, o.label, autoSnapPrefix+o.label+"-")
		}
	}

	// Plan against the existing snapshots before creating anything.
	snaps, err := listRotSnaps(ctx, p, base)
	if err != nil {
		return err
	}
	keepExisting := o.keep
	if o.create {
		keepExisting-- // the new snapshot takes one of the N slots
	}
	kept, del := planRotation(snaps, o.label, keepExisting)

	res := snapshotRotateResult{VMID: g.VMID, Kind: kind, Node: g.Node, Remote: g.Remote,
		Label: o.label, Keep: o.keep, DryRun: o.dryRun, Kept: []string{}, Deleted: []string{}}
	for _, s := range kept {
		res.Kept = append(res.Kept, s.Name)
	}
	if o.create {
		res.Created = o.name
		res.Kept = append(res.Kept, o.name)
	}
	delNames := make([]string, 0, len(del))
	for _, s := range del {
		delNames = append(delNames, s.Name)
	}

	if o.dryRun {
		for _, n := range delNames {
			fmt.Fprintf(stderrWriter(), "dry-run: would delete snapshot %s of %s %d\n", n, kind, g.VMID)
		}
		res.Deleted = append(res.Deleted, delNames...)
		return a.render(rotateTable(res))
	}

	if len(del) > 0 {
		target := fmt.Sprintf("%s %d", kind, g.VMID)
		if g.Name != "" {
			target += " (" + g.Name + ")"
		}
		where := g.Node
		if g.Remote != "" {
			where = fmt.Sprintf("%s (remote %s)", g.Node, g.Remote)
		}
		list := confirmList(delNames, 5)
		msg := fmt.Sprintf("delete %d %q snapshot(s) of %s on %s: %s?", len(del), o.label, target, where, list)
		if o.create {
			msg = fmt.Sprintf("create %s on %s on %s and delete %d old %q snapshot(s): %s?",
				o.name, target, where, len(del), o.label, list)
		}
		if err := confirm(a, msg); err != nil {
			return err
		}
	}

	if o.create {
		params := url.Values{"snapname": {o.name}, "description": {managedDescription(o.description, o.label)}}
		if o.vmstate {
			params.Set("vmstate", "1")
		}
		label := fmt.Sprintf("snapshot %s %d as %s", kind, g.VMID, o.name)
		if err := mutateAndWait(ctx, p, "POST", base+"/snapshot", params, label, o.timeout); err != nil {
			return err // nothing is pruned when the create fails
		}
		fmt.Fprintf(stderrWriter(), "created snapshot %s of %s %d\n", o.name, kind, g.VMID)
	}

	for i, s := range del {
		label := fmt.Sprintf("delete snapshot %s of %s %d", s.Name, kind, g.VMID)
		if err := mutateAndWait(ctx, p, "DELETE", base+"/snapshot/"+s.Name, nil, label, o.timeout); err != nil {
			fmt.Fprintf(stderrWriter(), "%s: %v\n", label, err)
			res.Failed = append(res.Failed, s.Name)
			if errors.Is(err, context.DeadlineExceeded) {
				// A wait timeout doesn't cancel the PVE task, which keeps the
				// guest locked; further deletes would only fail on the lock.
				for _, r := range del[i+1:] {
					res.NotAttempted = append(res.NotAttempted, r.Name)
				}
				if len(res.NotAttempted) > 0 {
					fmt.Fprintf(stderrWriter(), "stopping after a timed-out delete (its task may still be running); not attempted: %s\n",
						strings.Join(res.NotAttempted, ", "))
				}
				break
			}
			continue
		}
		fmt.Fprintf(stderrWriter(), "deleted snapshot %s of %s %d\n", s.Name, kind, g.VMID)
		res.Deleted = append(res.Deleted, s.Name)
	}
	if err := a.render(rotateTable(res)); err != nil {
		return err
	}
	if len(res.Failed) > 0 {
		msg := fmt.Sprintf("%d of %d snapshot deletion(s) failed", len(res.Failed), len(del))
		if len(res.NotAttempted) > 0 {
			msg += fmt.Sprintf(", %d not attempted", len(res.NotAttempted))
		}
		return errors.New(msg)
	}
	return nil
}

// confirmList joins names for a prompt, listing at most max and summarizing
// the rest as "and N more".
func confirmList(names []string, max int) string {
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:max], ", "), len(names)-max)
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
		out = append(out, rotSnap{Name: name, Snaptime: t, Description: asString(s["description"])})
	}
	return out, nil
}

// mutateAndWait issues a mutation and, when it returns a task id, always waits
// for the task — regardless of output format — without rendering anything on
// stdout (a spinner goes to stderr on a TTY). Rotation needs this: the prune
// must not start before the create succeeded, and deletions on one guest must
// not overlap (PVE locks the guest per snapshot task). As with every task wait
// in pc, an exit status other than "OK" (including "WARNINGS: n") is a
// failure, so a create that ends with warnings stops the run before pruning.
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
	// The wait deadline is owned here (not by task.Wait) so an expired wait
	// is always recognisable via errors.Is(err, context.DeadlineExceeded),
	// even when the last status poll failed with an error that doesn't wrap
	// the context error (e.g. exhausted transport retries).
	waitCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, secs(timeout))
		defer cancel()
	}
	_, err = task.Wait(waitCtx, p.TaskStatus, h, task.WaitOptions{
		Spinner: isTTY(),
		Out:     stderrWriter(),
		Label:   label,
	})
	if err != nil {
		if cerr := waitCtx.Err(); cerr != nil && !errors.Is(err, cerr) {
			err = fmt.Errorf("%w: %w", cerr, err)
		}
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}
