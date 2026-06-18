// Package protocol handles the Proxmox wire format: response envelopes, error
// decoding, UPID/task parsing. It is transport-agnostic.
package protocol

import "fmt"

// Kind is the internal error taxonomy. It maps to process exit codes (see cli).
type Kind int

const (
	KindUnknown      Kind = iota
	KindAuth              // 401 / bad credentials
	KindPermission        // 403
	KindNotFound          // 404
	KindValidation        // 400 / parameter verification failed
	KindConflict          // 409 / locked / busy
	KindPrecondition      // 412
	KindTransport         // connection/TLS failure (no HTTP response)
	KindServer            // 5xx
	KindTaskFailed        // async task ended with a non-OK status
)

// APIError is a decoded Proxmox API failure. It preserves the raw body so the
// detail is never lost, while exposing a friendlier message by default.
type APIError struct {
	Kind       Kind
	StatusCode int
	Message    string            // human-friendly (possibly translated) message
	Errors     map[string]string // per-parameter validation errors, if any
	RawBody    string            // verbatim response body
}

func (e *APIError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if len(e.Errors) > 0 {
		return fmt.Sprintf("%s (%d): %s", e.Message, e.StatusCode, formatFieldErrors(e.Errors))
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("%s (HTTP %d)", e.Message, e.StatusCode)
	}
	return e.Message
}

func formatFieldErrors(m map[string]string) string {
	out := ""
	for k, v := range m {
		if out != "" {
			out += "; "
		}
		out += fmt.Sprintf("%s: %s", k, v)
	}
	return out
}

// kindFromStatus maps an HTTP status code to an error Kind.
func kindFromStatus(code int) Kind {
	switch {
	case code == 401:
		return KindAuth
	case code == 403:
		return KindPermission
	case code == 404:
		return KindNotFound
	case code == 400:
		return KindValidation
	case code == 409:
		return KindConflict
	case code == 412:
		return KindPrecondition
	case code >= 500:
		return KindServer
	default:
		return KindUnknown
	}
}
