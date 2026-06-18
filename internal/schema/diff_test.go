package schema

import "testing"

func ep(method, path string, required ...string) *Endpoint {
	e := &Endpoint{Path: path, Method: method}
	for _, r := range required {
		e.Parameters = append(e.Parameters, &Param{Name: r, Optional: false})
	}
	return e
}

func TestDiffAddedRemovedChanged(t *testing.T) {
	oldAPI := &API{Endpoints: []*Endpoint{
		ep("GET", "/version"),
		ep("POST", "/nodes/{node}/qemu", "vmid"),
		ep("DELETE", "/old/thing"),
	}}
	newAPI := &API{Endpoints: []*Endpoint{
		ep("GET", "/version"),
		ep("POST", "/nodes/{node}/qemu", "vmid", "name"), // changed: new required param
		ep("GET", "/brand/new"),                          // added
	}}

	d := Diff(oldAPI, newAPI)

	if len(d.Added) != 1 || d.Added[0] != "GET /brand/new" {
		t.Errorf("Added = %v", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0] != "DELETE /old/thing" {
		t.Errorf("Removed = %v", d.Removed)
	}
	if len(d.Changed) != 1 || d.Changed[0] != "POST /nodes/{node}/qemu" {
		t.Errorf("Changed = %v", d.Changed)
	}
	if !d.HasBreaking() {
		t.Error("removal/change should be breaking")
	}
}

func TestDiffNoBreaking(t *testing.T) {
	a := &API{Endpoints: []*Endpoint{ep("GET", "/version")}}
	b := &API{Endpoints: []*Endpoint{
		ep("GET", "/version"),
		ep("GET", "/extra"), // only an addition
	}}
	d := Diff(a, b)
	if d.HasBreaking() {
		t.Errorf("addition-only diff should not be breaking: %+v", d)
	}
	if len(d.Added) != 1 {
		t.Errorf("Added = %v", d.Added)
	}
}

func TestDiffOptionalParamNotBreaking(t *testing.T) {
	a := &API{Endpoints: []*Endpoint{ep("POST", "/x", "id")}}
	b := &API{Endpoints: []*Endpoint{{
		Path: "/x", Method: "POST",
		Parameters: []*Param{{Name: "id"}, {Name: "note", Optional: true}},
	}}}
	d := Diff(a, b)
	if d.HasBreaking() {
		t.Errorf("adding an optional param should not be breaking: %+v", d)
	}
}
