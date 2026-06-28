package cli

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseAge(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"30d", 30 * 24 * time.Hour, true},
		{"12w", 12 * 7 * 24 * time.Hour, true},
		{"48h", 48 * time.Hour, true},
		{"90m", 90 * time.Minute, true},
		{"1.5d", 36 * time.Hour, true},
		{"", 0, false},
		{"banana", 0, false},
		{"10x", 0, false},
	}
	for _, c := range cases {
		got, err := parseAge(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("parseAge(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("parseAge(%q) = %v; want error", c.in, got)
		}
	}
}

func TestParseDate(t *testing.T) {
	got, err := parseDate("2026-01-02")
	if err != nil {
		t.Fatalf("parseDate bare date: %v", err)
	}
	if got.Year() != 2026 || got.Month() != time.January || got.Day() != 2 {
		t.Fatalf("parseDate bare date = %v", got)
	}
	if _, err := parseDate("2026-13-99"); err == nil {
		t.Fatalf("expected error for invalid date")
	}
}

func TestPruneCutoffExclusive(t *testing.T) {
	if _, err := pruneCutoff("30d", "2026-01-01"); err == nil {
		t.Fatalf("expected error when both --older-than and --before given")
	}
	if _, err := pruneCutoff("", ""); err == nil {
		t.Fatalf("expected error when neither cutoff given")
	}
	if _, err := pruneCutoff("30d", ""); err != nil {
		t.Fatalf("--older-than alone should be valid: %v", err)
	}
}

func TestHumanizeAge(t *testing.T) {
	cases := map[int64]string{0: "-", -5: "-", 30: "30s", 120: "2m", 7200: "2h", 172800: "2d"}
	for sec, want := range cases {
		if got := humanizeAge(sec); got != want {
			t.Errorf("humanizeAge(%d) = %q; want %q", sec, got, want)
		}
	}
}

// snapshotTable's json/yaml payload (Raw) must be a native array of flat
// snapshot objects so callers can `jq '.[].age_seconds'` — not the string row
// projection (decision #1).
func TestSnapshotTableJSONIsNativeArray(t *testing.T) {
	rows := []snapshotRow{
		{VMID: 100, Guest: "web-01", Kind: "vm", Node: "pve-01", Snapshot: "preupgrade", Snaptime: 1700000000, AgeSeconds: 12345},
	}
	tab := snapshotTable(rows, false)

	b, err := json.Marshal(tab.Raw)
	if err != nil {
		t.Fatalf("marshal Raw: %v", err)
	}
	var arr []map[string]any
	if err := json.Unmarshal(b, &arr); err != nil {
		t.Fatalf("expected a JSON array of objects, got %s (%v)", b, err)
	}
	if len(arr) != 1 || arr[0]["vmid"] != float64(100) || arr[0]["snapshot"] != "preupgrade" {
		t.Fatalf("expected addressable native fields, got %s", b)
	}
	if arr[0]["age_seconds"] != float64(12345) {
		t.Fatalf("expected .age_seconds addressable, got %v in %s", arr[0]["age_seconds"], b)
	}
}

func TestSnapshotTableRemoteColumn(t *testing.T) {
	rows := []snapshotRow{{VMID: 100, Kind: "vm", Snapshot: "s1"}}
	if got := snapshotTable(rows, false); contains(got.Columns, "remote") {
		t.Errorf("remote column should be hidden when no remotes present: %v", got.Columns)
	}
	if got := snapshotTable(rows, true); !contains(got.Columns, "remote") {
		t.Errorf("remote column should show when requested: %v", got.Columns)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
