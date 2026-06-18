package config

import "fmt"

// Error marks a configuration/credential problem so the CLI can map it to the
// auth/config exit code.
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

func errf(format string, args ...any) *Error {
	return &Error{msg: fmt.Sprintf(format, args...)}
}
