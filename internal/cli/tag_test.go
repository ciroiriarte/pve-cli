package cli

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ciroiriarte/pve-cli/internal/domain"
)

func TestSplitTags(t *testing.T) {
	cases := map[string][]string{
		"a;b;c":       {"a", "b", "c"},
		"a, b ,c":     {"a", "b", "c"},
		"a b\tc":      {"a", "b", "c"},
		"":            {},
		"  ;; , ,  ":  {},
		"prod;prod;x": {"prod", "prod", "x"}, // split doesn't dedup
	}
	for in, want := range cases {
		got := splitTags(in)
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("splitTags(%q) = %v; want %v", in, got, want)
		}
	}
}

func TestNormalizeAddRemoveTags(t *testing.T) {
	if got := normalizeTags([]string{"b", "a", "a", " c "}); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("normalizeTags = %v", got)
	}
	if got := addTags([]string{"a", "b"}, []string{"b", "c"}); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("addTags = %v", got)
	}
	if got := removeTags([]string{"a", "b", "c"}, []string{"b", "z"}); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Errorf("removeTags = %v", got)
	}
	if got := removeTags([]string{"a"}, []string{"a"}); len(got) != 0 {
		t.Errorf("removeTags to empty = %v; want []", got)
	}
}

func TestGuestHasAnyTag(t *testing.T) {
	if !guestHasAnyTag("prod;web", []string{"web"}) {
		t.Error("expected match on web")
	}
	if guestHasAnyTag("prod;web", []string{"db"}) {
		t.Error("expected no match on db")
	}
	if !guestHasAnyTag("anything", nil) {
		t.Error("empty want should match everything")
	}
}

func TestFilterGuestsByTags(t *testing.T) {
	guests := []domain.Guest{
		{VMID: 100, Tags: "prod;web"},
		{VMID: 101, Tags: "dev"},
		{VMID: 102, Tags: ""},
	}
	got := filterGuestsByTags(guests, []string{"prod"})
	if len(got) != 1 || got[0].VMID != 100 {
		t.Errorf("filterGuestsByTags = %v", got)
	}
	if len(filterGuestsByTags(guests, nil)) != 3 {
		t.Error("nil filter should pass all through")
	}
}

func TestParseIDSet(t *testing.T) {
	got, err := parseIDSet([]string{"100", " 101 ", "102"})
	if err != nil || !got[100] || !got[101] || !got[102] {
		t.Fatalf("parseIDSet = %v, %v", got, err)
	}
	if s, _ := parseIDSet(nil); s != nil {
		t.Errorf("empty should be nil, got %v", s)
	}
	if _, err := parseIDSet([]string{"100", "x"}); err == nil {
		t.Error("expected error on non-numeric id")
	}
}

func TestTagEntryMismatch(t *testing.T) {
	g := domain.Guest{VMID: 100, Name: "web", Kind: domain.KindVM}
	// Same identity → no mismatch.
	if tagEntryMismatch(tagExportEntry{VMID: 100, Name: "web", Kind: "vm"}, g) {
		t.Error("identical identity should not mismatch")
	}
	// Recycled vmid: name differs → mismatch.
	if !tagEntryMismatch(tagExportEntry{VMID: 100, Name: "db", Kind: "vm"}, g) {
		t.Error("name change should mismatch")
	}
	// Kind differs → mismatch.
	if !tagEntryMismatch(tagExportEntry{VMID: 100, Name: "web", Kind: "ct"}, g) {
		t.Error("kind change should mismatch")
	}
	// Legacy export without name/kind → treated as don't-care.
	if tagEntryMismatch(tagExportEntry{VMID: 100, Tags: []string{"a"}}, g) {
		t.Error("empty backup identity should not mismatch (back-compat)")
	}
}

// tag export entries must serialize to a native array of flat objects with a
// real tags array — the scripting contract (decision #1).
func TestTagExportJSONShape(t *testing.T) {
	entries := []tagExportEntry{{VMID: 100, Name: "web", Kind: "vm", Node: "pve-01", Tags: []string{"prod", "web"}}}
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back []map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tags, ok := back[0]["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "prod" {
		t.Fatalf("expected tags array, got %s", b)
	}
}
