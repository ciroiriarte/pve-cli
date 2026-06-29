// Command genchangelog derives the OBS packaging changelogs
// (packaging/obs/debian/changelog and packaging/obs/pve-cli.changes) from the
// single source of truth: the root CHANGELOG.md (entry text) plus the git tags
// (authoritative versions + dates). This keeps the Debian/RPM changelogs from
// silently freezing — `set_version` only patches the version number, never the
// entry text, which is how they drifted to 0.5.x before.
//
// Run from the repo root: `go run ./cmd/genchangelog` (or `make changelog`).
// CI runs it and fails on any diff, so a release that updates CHANGELOG.md but
// forgets to regenerate cannot merge.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maintainer   = "Ciro Iriarte <ciro.iriarte+software@gmail.com>"
	debChangelog = "packaging/obs/debian/changelog"
	rpmChanges   = "packaging/obs/pve-cli.changes"
)

// floor is the oldest version emitted by the generator; older history predates
// (or is the start of) OBS packaging and is captured by the static footer.
var floor = semver{0, 6, 0}

// debFooter / rpmFooter are the original initial-packaging entries, kept verbatim
// as the tail of each changelog (these predate the floor).
const debFooter = `pve-cli (0.5.0-1) unstable; urgency=medium

  * Initial OBS packaging: ships the ` + "`pc`" + ` binary, man pages, and shell
    completions. Built offline from vendored Go modules.

 -- ` + maintainer + `  Thu, 18 Jun 2026 00:00:00 +0000
`

const rpmFooter = `-------------------------------------------------------------------
Thu Jun 18 2026 ` + maintainer + ` - 0.5.5-1

- Initial OBS packaging: pc binary, man pages, shell completions.
- Vendor Go modules in-tree for fully offline distro builds.
- Add Debian source control (.dsc) so OBS drives the .deb build.
- Own zsh/fish completion directories (openSUSE filelist check).
`

type semver struct{ major, minor, patch int }

func (v semver) less(o semver) bool {
	if v.major != o.major {
		return v.major < o.major
	}
	if v.minor != o.minor {
		return v.minor < o.minor
	}
	return v.patch < o.patch
}

func (v semver) String() string { return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch) }

func parseSemver(s string) (semver, bool) {
	m := regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`).FindStringSubmatch(s)
	if m == nil {
		return semver{}, false
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	c, _ := strconv.Atoi(m[3])
	return semver{a, b, c}, true
}

type release struct {
	v     semver
	date  time.Time
	title string
}

type heading struct {
	title string
	date  time.Time // zero if the heading carries no explicit date
	dated bool
}

func main() {
	headings, err := parseChangelog("CHANGELOG.md")
	if err != nil {
		fail(err)
	}
	tagDates, err := tagDates()
	if err != nil {
		fail(err)
	}
	// Union of versions known to the changelog and to git tags, at/above floor.
	versions := map[semver]bool{}
	for v := range headings {
		versions[v] = true
	}
	for v := range tagDates {
		versions[v] = true
	}

	var rels []release
	for v := range versions {
		if v.less(floor) {
			continue
		}
		h := headings[v]
		// Date precedence: explicit CHANGELOG date (lets an in-flight release be
		// generated before its tag exists) > git tag commit date.
		var date time.Time
		switch {
		case h.dated:
			date = h.date
		case !tagDates[v].IsZero():
			date = tagDates[v]
		default:
			fail(fmt.Errorf("version %s has neither a git tag nor a date in its CHANGELOG heading (add `## [%s] - YYYY-MM-DD — title`)", v, v))
		}
		title := h.title
		if title == "" {
			title = "Release " + v.String()
		}
		rels = append(rels, release{v: v, date: date, title: title})
	}
	if len(rels) == 0 {
		fail(fmt.Errorf("no versions at or above %s found in CHANGELOG.md or git tags", floor))
	}
	// Newest first.
	sort.Slice(rels, func(i, j int) bool { return rels[j].v.less(rels[i].v) })

	if err := os.WriteFile(debChangelog, []byte(renderDebian(rels)), 0o644); err != nil {
		fail(err)
	}
	if err := os.WriteFile(rpmChanges, []byte(renderRPM(rels)), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("wrote %s and %s (%d versions)\n", debChangelog, rpmChanges, len(rels))
}

// parseChangelog maps version -> heading from CHANGELOG.md. Accepted forms:
//
//	## [X.Y.Z] — title
//	## [X.Y.Z] - YYYY-MM-DD — title   (date optional, Keep a Changelog style)
func parseChangelog(path string) (map[semver]heading, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`^##\s+\[(\d+\.\d+\.\d+)\]\s*(?:-\s*(\d{4}-\d{2}-\d{2}))?\s*(?:[—-]\s*(.*?))?\s*$`)
	out := map[semver]heading{}
	for _, line := range strings.Split(string(b), "\n") {
		m := re.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		v, ok := parseSemver(m[1])
		if !ok {
			continue
		}
		h := heading{title: strings.TrimSpace(m[3])}
		if m[2] != "" {
			d, derr := time.Parse("2006-01-02", m[2])
			if derr != nil {
				return nil, fmt.Errorf("bad date in heading for %s: %w", v, derr)
			}
			h.date, h.dated = d.UTC(), true
		}
		out[v] = h
	}
	return out, nil
}

// tagDates maps version -> commit date for every vX.Y.Z git tag.
func tagDates() (map[semver]time.Time, error) {
	out, err := exec.Command("git", "tag", "--list", "v*").Output()
	if err != nil {
		return nil, fmt.Errorf("git tag: %w", err)
	}
	dates := map[semver]time.Time{}
	for _, t := range strings.Fields(string(out)) {
		v, ok := parseSemver(strings.TrimPrefix(t, "v"))
		if !ok {
			continue
		}
		iso, err := exec.Command("git", "log", "-1", "--format=%cI", t).Output()
		if err != nil {
			return nil, fmt.Errorf("git date for %s: %w", t, err)
		}
		d, err := time.Parse(time.RFC3339, strings.TrimSpace(string(iso)))
		if err != nil {
			return nil, fmt.Errorf("parse date for %s: %w", t, err)
		}
		dates[v] = d
	}
	return dates, nil
}

func renderDebian(rels []release) string {
	var b strings.Builder
	for _, r := range rels {
		fmt.Fprintf(&b, "pve-cli (%s-1) unstable; urgency=medium\n\n", r.v)
		fmt.Fprintf(&b, "  * %s\n\n", r.title)
		fmt.Fprintf(&b, " -- %s  %s\n\n", maintainer, r.date.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	}
	b.WriteString(debFooter)
	return b.String()
}

func renderRPM(rels []release) string {
	var b strings.Builder
	for _, r := range rels {
		b.WriteString("-------------------------------------------------------------------\n")
		fmt.Fprintf(&b, "%s %s - %s-1\n\n", r.date.Format("Mon Jan 2 2006"), maintainer, r.v)
		fmt.Fprintf(&b, "- %s\n\n", r.title)
	}
	b.WriteString(rpmFooter)
	return b.String()
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "genchangelog:", err)
	os.Exit(1)
}
