package cli

import (
	"errors"

	"github.com/ciroiriarte/pve-cli/internal/config"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
)

// Exit codes (documented contract). Scripts may rely on these.
const (
	ExitOK         = 0
	ExitGeneric    = 1
	ExitAuthConfig = 2
	ExitNotFound   = 3
	ExitAPIServer  = 4
	ExitTaskFailed = 5
	ExitValidation = 6
	// `pc cluster check` verdicts (the report itself is on stdout).
	ExitCheckWarn = 7
	ExitCheckCrit = 8
)

// ExitCodeFor maps an error to its process exit code.
func ExitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	var chk *checkExit
	if errors.As(err, &chk) {
		return chk.code
	}
	var cfgErr *config.Error
	if errors.As(err, &cfgErr) {
		return ExitAuthConfig
	}
	var apiErr *protocol.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Kind {
		case protocol.KindAuth, protocol.KindPermission:
			return ExitAuthConfig
		case protocol.KindNotFound:
			return ExitNotFound
		case protocol.KindValidation, protocol.KindPrecondition:
			return ExitValidation
		case protocol.KindConflict, protocol.KindServer, protocol.KindTransport:
			return ExitAPIServer
		case protocol.KindTaskFailed:
			return ExitTaskFailed
		}
	}
	return ExitGeneric
}
