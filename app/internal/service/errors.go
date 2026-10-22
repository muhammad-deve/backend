package service

import "errors"

// UserError carries a message that is safe and useful to show the user, such
// as a validation problem. Other errors are logged and replaced with a fixed
// message by the handlers.
type UserError struct {
	msg string
}

func (e *UserError) Error() string { return e.msg }

// NewUserError returns an error whose message is shown to the user as is.
func NewUserError(msg string) error { return &UserError{msg: msg} }

func userError(msg string) error { return NewUserError(msg) }

// UserMessage returns the user-facing message of err, if it has one.
func UserMessage(err error) (string, bool) {
	var userErr *UserError
	if errors.As(err, &userErr) {
		return userErr.msg, true
	}
	return "", false
}
