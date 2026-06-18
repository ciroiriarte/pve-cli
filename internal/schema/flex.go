package schema

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// flexInt accepts a JSON number or a numeric string (the upstream schema mixes
// `"optional": 1` and `"optional": "1"`).
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*f = 0
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s == "" {
			*f = 0
			return nil
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			// Non-numeric but present → treat as truthy.
			*f = 1
			return nil
		}
		*f = flexInt(n)
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}

// flexStrings accepts a JSON array whose elements may be strings, numbers, or
// booleans, stringifying each (upstream enums are not always strings).
type flexStrings []string

func (f *flexStrings) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	var raw []any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, fmt.Sprintf("%v", v))
	}
	*f = out
	return nil
}
