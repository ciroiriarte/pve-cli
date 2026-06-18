// Package pdm embeds the committed Proxmox Datacenter Manager API schema
// snapshot and exposes it as a parsed IR (mirrors internal/generated/pve).
package pdm

import (
	_ "embed"
	"sync"

	"github.com/ciroiriarte/pve-cli/internal/schema"
)

//go:embed apischema.json
var snapshot []byte

var (
	once   sync.Once
	cached *schema.API
	cerr   error
)

// Schema returns the embedded PDM API schema, parsed once and cached.
func Schema() (*schema.API, error) {
	once.Do(func() {
		cached, cerr = schema.LoadBytes(snapshot)
	})
	return cached, cerr
}
