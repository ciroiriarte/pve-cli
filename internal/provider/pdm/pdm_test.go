package pdm

import (
	"encoding/json"
	"testing"
)

// PDM's /resources/list returns guest tags as a JSON array of strings, unlike
// PVE's /cluster/resources which returns a ';'-separated string. tagsString must
// normalize both into the ';'-joined form domain.Guest uses. Regression guard for
// the live-found decode bug (array into Go string field).
func TestTagsString(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"pdm array", `["ntp","tst-cly"]`, "ntp;tst-cly"},
		{"pve string", `"prod;web"`, "prod;web"},
		{"empty array", `[]`, ""},
		{"null", `null`, ""},
		{"missing", ``, ""},
		{"single", `["only"]`, "only"},
	}
	for _, c := range cases {
		got := tagsString(json.RawMessage(c.raw))
		if got != c.want {
			t.Errorf("%s: tagsString(%q) = %q; want %q", c.name, c.raw, got, c.want)
		}
	}
}
