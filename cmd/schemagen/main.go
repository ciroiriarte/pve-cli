// Command schemagen ingests the upstream Proxmox API schema (apidoc.js) and
// writes a normalized, committed JSON snapshot that the binary embeds. It can
// also diff two snapshots to surface breaking changes across PVE versions.
//
// Usage:
//
//	schemagen -in apidoc.js [-url URL] -out internal/schema/pvedata/apischema.json -version 8
//	schemagen -diff old.json new.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ciroiriarte/pve-cli/internal/schema"
)

const defaultURL = "https://pve.proxmox.com/pve-docs/api-viewer/apidoc.js"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "schemagen:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		in      = flag.String("in", "", "path to a local apidoc.js (overrides -url)")
		url     = flag.String("url", defaultURL, "URL to fetch apidoc.js from")
		out     = flag.String("out", "internal/generated/pve/apischema.json", "snapshot output path")
		version = flag.String("version", "unknown", "version label to record in metadata")
		diff    = flag.Bool("diff", false, "diff mode: schemagen -diff OLD.json NEW.json")
	)
	flag.Parse()

	if *diff {
		args := flag.Args()
		if len(args) != 2 {
			return fmt.Errorf("diff mode needs exactly two snapshot paths")
		}
		return runDiff(args[0], args[1])
	}

	js, source, err := loadJS(*in, *url)
	if err != nil {
		return err
	}
	tree, err := extractArray(js)
	if err != nil {
		return err
	}
	// Validate by parsing into the IR.
	api, err := schema.Parse(tree)
	if err != nil {
		return fmt.Errorf("validate schema: %w", err)
	}

	snap := schema.Snapshot{
		Meta: schema.Meta{
			Source:  source,
			Fetched: time.Now().UTC().Format(time.RFC3339),
			Version: *version,
		},
		Schema: indentJSON(tree),
	}
	if err := writeSnapshot(*out, snap); err != nil {
		return err
	}
	fmt.Printf("wrote %s: %d endpoints (version %s)\n", *out, len(api.Endpoints), *version)
	return nil
}

func loadJS(in, url string) (data []byte, source string, err error) {
	if in != "" {
		b, err := os.ReadFile(in)
		return b, "file://" + in, err
	}
	resp, err := http.Get(url) //nolint:gosec // fixed upstream docs URL
	if err != nil {
		return nil, url, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, url, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	return b, url, err
}

// extractArray returns the JSON array assigned to `const apiSchema = [...]`,
// using string-aware bracket matching so nested arrays/strings don't confuse it.
func extractArray(js []byte) ([]byte, error) {
	s := string(js)
	start := indexOfArray(s)
	if start < 0 {
		return nil, fmt.Errorf("could not locate `apiSchema = [` in input")
	}
	depth := 0
	inStr := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return []byte(s[start : i+1]), nil
			}
		}
	}
	return nil, fmt.Errorf("unterminated apiSchema array")
}

// indexOfArray finds the offset of the `[` that opens the apiSchema array. It
// anchors on the assignment `apiSchema =` (allowing whitespace) rather than the
// bare token, so a stray `apiSchema` mention (e.g. in a comment) before the
// real declaration cannot mislocate the array.
func indexOfArray(s string) int {
	const marker = "apiSchema"
	off := 0
	for {
		rel := strings.Index(s[off:], marker)
		if rel < 0 {
			return -1
		}
		after := off + rel + len(marker)
		j := after
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		if j < len(s) && s[j] == '=' {
			if open := strings.IndexByte(s[j:], '['); open >= 0 {
				return j + open
			}
			return -1
		}
		off = after // not an assignment; keep scanning
	}
}

func indentJSON(raw []byte) json.RawMessage {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return raw
	}
	return b
}

func writeSnapshot(path string, snap schema.Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

func runDiff(oldPath, newPath string) error {
	oldAPI, err := schema.LoadFile(oldPath)
	if err != nil {
		return fmt.Errorf("load old: %w", err)
	}
	newAPI, err := schema.LoadFile(newPath)
	if err != nil {
		return fmt.Errorf("load new: %w", err)
	}
	d := schema.Diff(oldAPI, newAPI)
	fmt.Print(d.String())
	if d.HasBreaking() {
		os.Exit(2)
	}
	return nil
}
