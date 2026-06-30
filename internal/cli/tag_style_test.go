package cli

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseTagStyleRoundTrip(t *testing.T) {
	in := "case-sensitive=1,color-map=app:0000ff;db:ff0000:ffffff,ordering=alphabetical,shape=full"
	ts := parseTagStyle(in)
	if ts.Shape != "full" || ts.Ordering != "alphabetical" {
		t.Fatalf("shape/ordering = %q/%q", ts.Shape, ts.Ordering)
	}
	if ts.CaseSensitive == nil || !*ts.CaseSensitive {
		t.Fatalf("case-sensitive = %v", ts.CaseSensitive)
	}
	want := []tagColor{{Tag: "app", Background: "0000ff"}, {Tag: "db", Background: "ff0000", Text: "ffffff"}}
	if !reflect.DeepEqual(ts.ColorMap, want) {
		t.Fatalf("color-map = %#v", ts.ColorMap)
	}
	// Serialization is canonical (key order + color-map sorted by tag) and round-trips.
	if got := ts.String(); got != in {
		t.Fatalf("String() = %q; want %q", got, in)
	}
}

// GET /cluster/options returns tag-style as a parsed object (color-map kept as a
// sub-list string), while PUT takes a property string — readTagStyle must accept
// the object form or reads come back empty (the live bug this guards).
func TestTagStyleFromAnyObject(t *testing.T) {
	obj := map[string]any{
		"shape":          "full",
		"ordering":       "alphabetical",
		"case-sensitive": float64(1),
		"color-map":      "app:0000ff;db:ff0000:ffffff",
	}
	ts := tagStyleFromAny(obj)
	if ts.Shape != "full" || ts.Ordering != "alphabetical" {
		t.Fatalf("shape/ordering = %q/%q", ts.Shape, ts.Ordering)
	}
	if ts.CaseSensitive == nil || !*ts.CaseSensitive {
		t.Fatalf("case-sensitive = %v", ts.CaseSensitive)
	}
	want := []tagColor{{Tag: "app", Background: "0000ff"}, {Tag: "db", Background: "ff0000", Text: "ffffff"}}
	if !reflect.DeepEqual(ts.ColorMap, want) {
		t.Fatalf("color-map = %#v", ts.ColorMap)
	}
	// String form still parses (older/raw responses).
	if got := tagStyleFromAny("shape=circle"); got.Shape != "circle" {
		t.Fatalf("string form: %#v", got)
	}
	// Absent/garbage → empty.
	if !tagStyleFromAny(nil).isEmpty() {
		t.Fatal("nil should yield empty style")
	}
}

func TestParseTagStyleEmpty(t *testing.T) {
	ts := parseTagStyle("")
	if !ts.isEmpty() {
		t.Fatalf("empty input should be empty style, got %#v", ts)
	}
	if ts.String() != "" {
		t.Fatalf("String() of empty = %q", ts.String())
	}
}

func TestTagStyleStringSortsColors(t *testing.T) {
	ts := tagStyle{ColorMap: []tagColor{{Tag: "zeta", Background: "111111"}, {Tag: "alpha", Background: "222222"}}}
	if got := ts.String(); got != "color-map=alpha:222222;zeta:111111" {
		t.Fatalf("String() = %q", got)
	}
}

func TestNormalizeHexColor(t *testing.T) {
	for _, in := range []string{"ff0000", "#FF0000", " #Ff0000 "} {
		got, err := normalizeHexColor(in)
		if err != nil || got != "ff0000" {
			t.Fatalf("normalizeHexColor(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"f00", "gggggg", "0000000", "#12345", ""} {
		if _, err := normalizeHexColor(bad); err == nil {
			t.Errorf("normalizeHexColor(%q) should have errored", bad)
		}
	}
}

func TestTagStyleSetRemoveColors(t *testing.T) {
	var ts tagStyle
	ts.setColor(tagColor{Tag: "prod", Background: "ff0000"})
	ts.setColor(tagColor{Tag: "test", Background: "808080"})
	ts.setColor(tagColor{Tag: "prod", Background: "00ff00", Text: "ffffff"}) // replace, not duplicate
	if len(ts.ColorMap) != 2 {
		t.Fatalf("expected 2 entries, got %#v", ts.ColorMap)
	}
	if ts.ColorMap[0].Tag != "prod" || ts.ColorMap[0].Background != "00ff00" || ts.ColorMap[0].Text != "ffffff" {
		t.Fatalf("replace failed: %#v", ts.ColorMap[0])
	}
	if n := ts.removeColors([]string{"prod", "absent"}); n != 1 {
		t.Fatalf("removeColors returned %d; want 1", n)
	}
	if len(ts.ColorMap) != 1 || ts.ColorMap[0].Tag != "test" {
		t.Fatalf("after remove: %#v", ts.ColorMap)
	}
}

func TestTagStyleEmptyAfterClear(t *testing.T) {
	ts := tagStyle{ColorMap: []tagColor{{Tag: "a", Background: "ffffff"}}}
	ts.ColorMap = nil
	if !ts.isEmpty() {
		t.Fatal("style with only a (now removed) color-map should be empty -> triggers delete=tag-style")
	}
	// A style keeping shape is NOT empty.
	if (tagStyle{Shape: "circle"}).isEmpty() {
		t.Fatal("style with shape should not be empty")
	}
}

func TestValidateEnum(t *testing.T) {
	if err := validateEnum("shape", "circle", tagShapes); err != nil {
		t.Fatalf("circle should be valid: %v", err)
	}
	if err := validateEnum("shape", "square", tagShapes); err == nil {
		t.Fatal("square should be invalid")
	}
}

// tagColorTable must emit a native JSON array of {tag,background,text} objects,
// never a [{key,value}] projection — the scripting contract (decision #1).
func TestTagColorTableJSONIsNativeArray(t *testing.T) {
	tab := tagColorTable([]tagColor{{Tag: "db", Background: "ff0000", Text: "ffffff"}, {Tag: "app", Background: "0000ff"}})
	b, err := json.Marshal(tab.Raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back []map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("Raw is not an array of objects: %s", b)
	}
	// sorted by tag: app first.
	if len(back) != 2 || back[0]["tag"] != "app" || back[0]["background"] != "0000ff" {
		t.Fatalf("unexpected shape: %s", b)
	}
	if _, hasKey := back[0]["key"]; hasKey {
		t.Fatalf("color rows leaked a key/value projection: %s", b)
	}
}

// tagStyleTable's Raw must be the native tag-style object so `jq '.shape'` works.
func TestTagStyleTableJSONIsNativeObject(t *testing.T) {
	cs := true
	tab := tagStyleTable(tagStyle{Shape: "full", Ordering: "config", CaseSensitive: &cs, ColorMap: []tagColor{{Tag: "x", Background: "ffffff"}}})
	b, err := json.Marshal(tab.Raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatalf("Raw is not an object: %s", b)
	}
	if obj["shape"] != "full" || obj["ordering"] != "config" || obj["case_sensitive"] != true {
		t.Fatalf("unexpected object: %s", b)
	}
}
