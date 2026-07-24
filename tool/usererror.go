package tool

import (
	"errors"
	"fmt"
)

// UserError marks an error whose message is safe to show a human. Tools return
// it for actionable failures — an unknown resource type, a not-found object, a
// permission denial — whose text carries no endpoints, credentials, or other
// internal detail. Callers surface UserMessage() to the requester and fall back
// to a generic notice for any error that does not implement UserError.
type UserError interface {
	error
	// UserMessage is the chat-safe rendering of the failure.
	UserMessage() string
}

// userError is the default UserError. It keeps an optional wrapped cause so a
// tool can attach internal context (visible to Error and errors.Is) while the
// safe message stays in UserMessage.
type userError struct {
	msg string
	err error
}

// Error returns the safe message, joined with the wrapped cause when present,
// so logs and %w chains keep the detail that UserMessage deliberately omits.
func (e *userError) Error() string {
	if e.err != nil {
		return e.msg + ": " + e.err.Error()
	}
	return e.msg
}

// UserMessage returns only the chat-safe text.
func (e *userError) UserMessage() string { return e.msg }

// Unwrap exposes the wrapped cause for errors.Is/As.
func (e *userError) Unwrap() error { return e.err }

// NewUserError returns a UserError whose safe message is fmt.Sprintf(format,
// args...). Use it for a failure with no underlying error to preserve.
func NewUserError(format string, args ...any) error {
	return &userError{msg: fmt.Sprintf(format, args...)}
}

// WrapUserError returns a UserError with the safe message msg wrapping cause, so
// UserMessage stays clean while Error and errors.Is still see cause.
func WrapUserError(cause error, format string, args ...any) error {
	return &userError{msg: fmt.Sprintf(format, args...), err: cause}
}

// UserMessage reports whether err is (or wraps) a UserError and returns its safe
// message. A nil or non-user error yields ("", false).
func UserMessage(err error) (string, bool) {
	if ue, ok := errors.AsType[UserError](err); ok {
		return ue.UserMessage(), true
	}
	return "", false
}
