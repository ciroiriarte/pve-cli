package cli

import (
	"encoding/json"
	"testing"

	"github.com/ciroiriarte/pve-cli/internal/domain"
)

// guestConfigTable backs `vm/ct show|status|config`. Its json/yaml output must
// be the native config object (so callers can `jq '.cores'`), NOT an array of
// {key,value} pairs — that array shape is the human table only. Regression test
// for the scripting-contract bug found in the v0.10.x UX review.
func TestGuestConfigTableJSONIsNativeObject(t *testing.T) {
	cfg := map[string]any{"cores": 4, "name": "web-01", "agent": "1"}
	tab := guestConfigTable(domain.Guest{VMID: 100, Name: "web-01"}, cfg)

	b, err := json.Marshal(tab.Raw)
	if err != nil {
		t.Fatalf("marshal Raw: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatalf("expected a JSON object, got %s (%v)", b, err)
	}
	if obj["cores"] != float64(4) {
		t.Fatalf("expected .cores == 4 to be directly addressable, got %v in %s", obj["cores"], b)
	}

	// The human table layout (key/value rows) must still be present for
	// -o table/value/csv rendering.
	if len(tab.Columns) != 2 || tab.Columns[0] != "key" || tab.Columns[1] != "value" {
		t.Fatalf("expected key/value table columns, got %v", tab.Columns)
	}
	if len(tab.Rows) != len(cfg) {
		t.Fatalf("expected %d table rows, got %d", len(cfg), len(tab.Rows))
	}
}
