package pdm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedPDMSchemaParses(t *testing.T) {
	api, err := Schema()
	if err != nil {
		t.Fatalf("Schema(): %v", err)
	}
	if len(api.Endpoints) < 100 {
		t.Fatalf("endpoint count = %d, want >= 100", len(api.Endpoints))
	}
	// PDM's single "/" root must be flattened to top-level segments.
	roots := api.Roots()
	want := map[string]bool{"remotes": false, "resources": false, "pve": false}
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
	if api.Find("/remotes", "GET") == nil {
		t.Error("GET /remotes not found in PDM schema")
	}
}

const goldenPath = "testdata/schema_signature.golden"

func TestPDMSchemaSignatureGolden(t *testing.T) {
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
		t.Errorf("PDM schema signature drifted; regenerate with PVE_CLI_UPDATE_GOLDEN=1")
	}
}
