// Package output renders tabular data in multiple formats. json/yaml are the
// stable scripting contract; the table layout is explicitly not guaranteed.
package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Format enumerates supported renderings.
type Format string

const (
	Table Format = "table"
	JSON  Format = "json"
	YAML  Format = "yaml"
	CSV   Format = "csv"
	Value Format = "value" // headerless, whitespace-separated fields
)

// ParseFormat validates a format string.
func ParseFormat(s string) (Format, error) {
	switch Format(strings.ToLower(s)) {
	case Table:
		return Table, nil
	case JSON:
		return JSON, nil
	case YAML:
		return YAML, nil
	case CSV:
		return CSV, nil
	case Value:
		return Value, nil
	default:
		return "", fmt.Errorf("unknown format %q (want table|json|yaml|csv|value)", s)
	}
}

// Table is an ordered set of columns and rows. Cells are pre-stringified for
// the human/CSV/value renderers; the Raw field feeds json/yaml.
type Tabular struct {
	Columns []string
	Rows    [][]string
	Raw     any // original typed data for json/yaml output
}

// Options controls rendering.
type Options struct {
	Format    Format
	Columns   []string // subset/order of columns (case-insensitive); empty = all
	NoHeaders bool
	SortBy    string // column name, optional ":desc" suffix
}

// Render writes t to w according to opts.
func Render(w io.Writer, t Tabular, opts Options) error {
	view := project(t, opts.Columns)
	if opts.SortBy != "" {
		sortRows(&view, opts.SortBy)
	}

	switch opts.Format {
	case JSON:
		return writeJSON(w, t.Raw)
	case YAML:
		return writeYAML(w, t.Raw)
	case CSV:
		return writeCSV(w, view, opts.NoHeaders)
	case Value:
		return writeValue(w, view)
	case Table, "":
		return writeTable(w, view, opts.NoHeaders)
	default:
		return fmt.Errorf("unsupported format %q", opts.Format)
	}
}

// project selects and reorders columns. Unknown column names are an error
// surfaced as an empty column rather than failing the whole render.
func project(t Tabular, cols []string) Tabular {
	if len(cols) == 0 {
		return t
	}
	idx := make(map[string]int, len(t.Columns))
	for i, c := range t.Columns {
		idx[strings.ToLower(c)] = i
	}
	out := Tabular{Raw: t.Raw}
	var keep []int
	for _, c := range cols {
		if i, ok := idx[strings.ToLower(c)]; ok {
			out.Columns = append(out.Columns, t.Columns[i])
			keep = append(keep, i)
		} else {
			out.Columns = append(out.Columns, c)
			keep = append(keep, -1)
		}
	}
	for _, row := range t.Rows {
		nr := make([]string, len(keep))
		for j, i := range keep {
			if i >= 0 && i < len(row) {
				nr[j] = row[i]
			}
		}
		out.Rows = append(out.Rows, nr)
	}
	return out
}

func sortRows(t *Tabular, spec string) {
	name, desc := spec, false
	if strings.HasSuffix(strings.ToLower(spec), ":desc") {
		name = spec[:len(spec)-5]
		desc = true
	} else if strings.HasSuffix(strings.ToLower(spec), ":asc") {
		name = spec[:len(spec)-4]
	}
	col := -1
	for i, c := range t.Columns {
		if strings.EqualFold(c, name) {
			col = i
			break
		}
	}
	if col < 0 {
		return
	}
	sort.SliceStable(t.Rows, func(a, b int) bool {
		less := t.Rows[a][col] < t.Rows[b][col]
		if desc {
			return !less
		}
		return less
	})
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeYAML(w io.Writer, v any) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	defer enc.Close()
	return enc.Encode(v)
}

func writeCSV(w io.Writer, t Tabular, noHeaders bool) error {
	cw := csv.NewWriter(w)
	if !noHeaders {
		if err := cw.Write(t.Columns); err != nil {
			return err
		}
	}
	for _, r := range t.Rows {
		if err := cw.Write(r); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func writeValue(w io.Writer, t Tabular) error {
	for _, r := range t.Rows {
		if _, err := fmt.Fprintln(w, strings.Join(r, " ")); err != nil {
			return err
		}
	}
	return nil
}

func writeTable(w io.Writer, t Tabular, noHeaders bool) error {
	widths := make([]int, len(t.Columns))
	for i, c := range t.Columns {
		widths[i] = len(c)
	}
	for _, r := range t.Rows {
		for i, cell := range r {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	if !noHeaders {
		writeRow(w, upper(t.Columns), widths)
	}
	for _, r := range t.Rows {
		writeRow(w, r, widths)
	}
	return nil
}

func writeRow(w io.Writer, cells []string, widths []int) {
	var b strings.Builder
	for i, cell := range cells {
		if i > 0 {
			b.WriteString("   ")
		}
		b.WriteString(cell)
		if i < len(cells)-1 && i < len(widths) {
			for pad := len(cell); pad < widths[i]; pad++ {
				b.WriteByte(' ')
			}
		}
	}
	fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
}

func upper(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToUpper(s)
	}
	return out
}
