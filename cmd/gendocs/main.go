// Command gendocs generates the man pages, shell completions, and markdown
// command reference from the cobra command tree. Output goes to an output dir
// (default "dist") that packaging consumes. Run via `make docs`.
package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/spf13/cobra/doc"

	"github.com/ciroiriarte/pve-cli/internal/cli"
)

func main() {
	out := "dist"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}

	root := cli.NewRootCmd()
	root.DisableAutoGenTag = true // reproducible output (no date footer)

	manDir := filepath.Join(out, "man", "man1")
	mdDir := filepath.Join(out, "docs")
	compDir := filepath.Join(out, "completions")
	for _, d := range []string{manDir, mdDir, compDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Fatal(err)
		}
	}

	if err := doc.GenMarkdownTree(root, mdDir); err != nil {
		log.Fatalf("markdown: %v", err)
	}
	header := &doc.GenManHeader{Title: "PC", Section: "1", Source: "pve-cli", Manual: "pve-cli Manual"}
	if err := doc.GenManTree(root, header, manDir); err != nil {
		log.Fatalf("man: %v", err)
	}
	if err := root.GenBashCompletionFileV2(filepath.Join(compDir, "pc"), true); err != nil {
		log.Fatalf("bash completion: %v", err)
	}
	if err := root.GenZshCompletionFile(filepath.Join(compDir, "_pc")); err != nil {
		log.Fatalf("zsh completion: %v", err)
	}
	if err := root.GenFishCompletionFile(filepath.Join(compDir, "pc.fish"), true); err != nil {
		log.Fatalf("fish completion: %v", err)
	}

	log.Printf("generated man pages, completions, and markdown reference in %s/", out)
}
