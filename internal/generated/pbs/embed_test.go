package pbs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedPBSSchemaParses(t *testing.T) {
	api, err := Schema()
	if err != nil {
		t.Fatalf("Schema(): %v", err)
	}
	if len(api.Endpoints) < 100 {
		t.Fatalf("endpoint count = %d, want >= 100", len(api.Endpoints))
	}
	roots := api.Roots()
	want := map[string]bool{"admin": false, "config": false, "nodes": false, "status": false}
	for _, r := range roots {
		if _, ok := want[r]; ok {
			want[r] = true
		}
	}
	for seg, found := range want {
		if !found {
			t.Errorf("expected top-level segment %q in roots %v", seg, roots)
		}
	}
	if api.Find("/admin/datastore", "GET") == nil {
		t.Error("GET /admin/datastore not found in PBS schema")
	}
}

// PBS composes many parameter objects with allOf; the parser must flatten them
// so `pc raw … --help` lists e.g. prune's backup group and keep-* options.
func TestPBSSchemaFlattensAllOfParams(t *testing.T) {
	api, err := Schema()
	if err != nil {
		t.Fatal(err)
	}
	ep := api.Find("/admin/datastore/{store}/prune", "POST")
	if ep == nil {
		t.Fatal("POST /admin/datastore/{store}/prune not found")
	}
	got := map[string]bool{}
	for _, p := range ep.Parameters {
		got[p.Name] = true
	}
	for _, name := range []string{"store", "backup-type", "backup-id", "keep-last", "dry-run"} {
		if !got[name] {
			t.Errorf("prune params missing %q (allOf not flattened?): %v", name, got)
		}
	}
}

const goldenPath = "testdata/schema_signature.golden"

func TestPBSSchemaSignatureGolden(t *testing.T) {
	api, err := Schema()
	if err != nil {
		t.Fatal(err)
	}
	got := api.Signature()
	if os.Getenv("PVE_CLI_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated golden: %s", goldenPath)
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (PVE_CLI_UPDATE_GOLDEN=1 to create): %v", err)
	}
	if got != string(want) {
		t.Errorf("PBS schema signature drifted; regenerate with PVE_CLI_UPDATE_GOLDEN=1")
	}
}
