package output

import (
	"bytes"
	"strings"
	"testing"
)

func sample() Tabular {
	return Tabular{
		Columns: []string{"vmid", "name", "status"},
		Rows: [][]string{
			{"101", "db", "stopped"},
			{"100", "web", "running"},
		},
		Raw: []map[string]any{{"vmid": 101}, {"vmid": 100}},
	}
}

func TestRenderValueStripsHeaders(t *testing.T) {
	var b bytes.Buffer
	if err := Render(&b, sample(), Options{Format: Value, Columns: []string{"vmid"}}); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(b.String())
	if got != "101\n100" {
		t.Errorf("value output = %q", got)
	}
}

func TestRenderTableHasHeaders(t *testing.T) {
	var b bytes.Buffer
	if err := Render(&b, sample(), Options{Format: Table}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "VMID") {
		t.Errorf("expected header VMID in:\n%s", b.String())
	}
}

func TestColumnProjectionAndSort(t *testing.T) {
	var b bytes.Buffer
	err := Render(&b, sample(), Options{Format: Value, Columns: []string{"name", "vmid"}, SortBy: "vmid:asc"})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if lines[0] != "web 100" {
		t.Errorf("first line = %q, want 'web 100' after sort+project", lines[0])
	}
}

func TestRenderJSONUsesRaw(t *testing.T) {
	var b bytes.Buffer
	if err := Render(&b, sample(), Options{Format: JSON}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"vmid": 101`) {
		t.Errorf("json output missing raw data:\n%s", b.String())
	}
}

func TestParseFormat(t *testing.T) {
	if _, err := ParseFormat("bogus"); err == nil {
		t.Error("expected error for bogus format")
	}
	if f, _ := ParseFormat("JSON"); f != JSON {
		t.Error("format parse should be case-insensitive")
	}
}
