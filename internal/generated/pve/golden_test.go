package pve

import (
	"os"
	"path/filepath"
	"testing"
)

// goldenPath is the committed signature of the embedded schema. Regenerate it
// (after intentionally updating the schema snapshot) with:
//
//	PVE_CLI_UPDATE_GOLDEN=1 go test ./internal/generated/pve/...
const goldenPath = "testdata/schema_signature.golden"

func TestSchemaSignatureGolden(t *testing.T) {
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
		t.Fatalf("read golden (run with PVE_CLI_UPDATE_GOLDEN=1 to create): %v", err)
	}
	if got != string(want) {
		t.Errorf("schema signature drifted from golden.\n"+
			"If the schema snapshot was updated intentionally, regenerate with:\n"+
			"  PVE_CLI_UPDATE_GOLDEN=1 go test ./internal/generated/pve/...\n"+
			"got %d bytes, want %d bytes", len(got), len(want))
	}
}
