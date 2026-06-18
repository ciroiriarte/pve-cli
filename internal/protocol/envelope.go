package protocol

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Envelope is the standard Proxmox JSON wrapper: {"data": ..., "errors": ...}.
type Envelope struct {
	Data   json.RawMessage   `json:"data"`
	Errors map[string]string `json:"errors,omitempty"`
}

// DecodeData unwraps a Proxmox success envelope into v.
func DecodeData(body []byte, v any) error {
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("decode envelope: %w", err)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		return fmt.Errorf("decode data: %w", err)
	}
	return nil
}

// DecodeError builds an APIError from a failed HTTP response, translating a few
// notoriously cryptic Proxmox messages into clearer ones. The raw body is kept.
func DecodeError(statusCode int, body []byte) *APIError {
	e := &APIError{
		Kind:       kindFromStatus(statusCode),
		StatusCode: statusCode,
		RawBody:    string(body),
	}

	// Proxmox returns parameter errors in the envelope's "errors" map.
	var env Envelope
	if err := json.Unmarshal(body, &env); err == nil && len(env.Errors) > 0 {
		e.Errors = env.Errors
		if e.Kind == KindUnknown || e.Kind == KindServer {
			e.Kind = KindValidation
		}
	}

	e.Message = translate(statusCode, string(body))

	// A "locked" message is a conflict regardless of the HTTP status Proxmox used.
	if lockRe.MatchString(string(body)) {
		e.Kind = KindConflict
	}
	return e
}

var lockRe = regexp.MustCompile(`(?i)is locked \(([^)]+)\)`)

// translate produces a friendlier message for common Proxmox error strings.
func translate(statusCode int, body string) string {
	trimmed := strings.TrimSpace(body)

	if m := lockRe.FindStringSubmatch(trimmed); m != nil {
		// e.g. "VM 100 is locked (backup)"
		guest := guestPrefix(trimmed)
		return fmt.Sprintf("%s is locked by an active '%s' task", guest, m[1])
	}
	if strings.Contains(trimmed, "no such") || strings.Contains(trimmed, "does not exist") {
		return cleanProxmoxMessage(trimmed)
	}
	switch statusCode {
	case 401:
		return "authentication failed: check token id/secret or ticket"
	case 403:
		return "permission denied: the token/user lacks the required privilege"
	case 404:
		return cleanProxmoxMessage(trimmed)
	}
	if msg := cleanProxmoxMessage(trimmed); msg != "" {
		return msg
	}
	return fmt.Sprintf("request failed with HTTP %d", statusCode)
}

var guestRe = regexp.MustCompile(`(?i)(VM|CT|container)\s+\d+`)

func guestPrefix(s string) string {
	if m := guestRe.FindString(s); m != "" {
		return m
	}
	return "guest"
}

// cleanProxmoxMessage strips the boilerplate that Proxmox prepends/appends to
// error strings (status lines, HTML) to surface the core message.
func cleanProxmoxMessage(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "{") || strings.HasPrefix(s, "<") {
		return ""
	}
	// Drop a trailing newline-delimited stack of detail; keep the first line.
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
