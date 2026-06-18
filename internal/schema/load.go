package schema

import (
	"encoding/json"
	"fmt"
	"os"
)

// Snapshot is the committed, embeddable on-disk form: provenance metadata plus
// the verbatim upstream apiSchema tree.
type Snapshot struct {
	Meta   Meta            `json:"meta"`
	Schema json.RawMessage `json:"schema"`
}

// LoadBytes parses a snapshot file's bytes into an API IR.
func LoadBytes(b []byte) (*API, error) {
	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, fmt.Errorf("decode snapshot: %w", err)
	}
	api, err := Parse(snap.Schema)
	if err != nil {
		return nil, err
	}
	api.Meta = snap.Meta
	return api, nil
}

// LoadFile reads and parses a snapshot file from disk.
func LoadFile(path string) (*API, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return LoadBytes(b)
}
