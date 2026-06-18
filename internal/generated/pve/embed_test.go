package pve

import "testing"

func TestEmbeddedSchemaParses(t *testing.T) {
	api, err := Schema()
	if err != nil {
		t.Fatalf("Schema(): %v", err)
	}
	if len(api.Endpoints) < 300 {
		t.Fatalf("endpoint count = %d, want >= 300", len(api.Endpoints))
	}
	if api.Meta.Version == "" {
		t.Error("snapshot metadata version is empty")
	}
}

func TestKnownEndpointCloneRequiresNewid(t *testing.T) {
	api, err := Schema()
	if err != nil {
		t.Fatal(err)
	}
	ep := api.Find("/nodes/{node}/qemu/{vmid}/clone", "POST")
	if ep == nil {
		t.Fatal("POST /nodes/{node}/qemu/{vmid}/clone not found in schema")
	}
	found, required := false, false
	for _, p := range ep.Parameters {
		if p.Name == "newid" {
			found = true
			required = !p.Optional
		}
	}
	if !found {
		t.Fatal("clone endpoint missing 'newid' parameter")
	}
	if !required {
		t.Error("'newid' should be a required parameter on clone")
	}
}

func TestParamOrderingRequiredFirst(t *testing.T) {
	api, _ := Schema()
	ep := api.Find("/nodes/{node}/qemu/{vmid}/clone", "POST")
	if ep == nil || len(ep.Parameters) < 2 {
		t.Skip("clone endpoint or params unavailable")
	}
	// Once an optional param appears, no required param may follow.
	seenOptional := false
	for _, p := range ep.Parameters {
		if p.Optional {
			seenOptional = true
		} else if seenOptional {
			t.Errorf("required param %q appears after an optional param", p.Name)
		}
	}
}
