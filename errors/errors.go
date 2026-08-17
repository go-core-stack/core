// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package errors

import (
	base "errors"
	"fmt"
)

func Is(err error, target error) bool {
	return base.Is(err, target)
}

// get the error code if the error is
// associated to recognizable error types
//
// The lookup walks the error chain rather than inspecting only the outermost
// error, so a code still resolves after the error has been wrapped by
// fmt.Errorf("%w", ...) or by another *Error. The outermost *Error wins, which
// is what callers expect: a re-classification applied later overrides an
// earlier one.
func GetErrCode(err error) ErrCode {
	var val *Error
	if base.As(err, &val) {
		return ErrCode(val.code)
	}
	return Unknown
}

// base error structure
type Error struct {
	code ErrCode
	msg  string

	// cause is the underlying error this one was built from, when there is
	// one. It is preserved so that base.Is/base.As keep working across the
	// package boundary - without it a wrapped error is flattened to a string
	// and predicates like Is(err, context.DeadlineExceeded) can never match.
	cause error
}

// Error() prints out the error message string
func (e Error) Error() string {
	switch {
	case e.msg == "" && e.cause != nil:
		return e.cause.Error()
	case e.cause == nil:
		return e.msg
	default:
		return e.msg + ": " + e.cause.Error()
	}
}

// Unwrap returns the underlying cause, allowing errors created by WrapErr and
// WrapErrf to participate in base.Is and base.As. Errors created by New, Wrap
// and Wrapf carry no cause and unwrap to nil.
func (e Error) Unwrap() error {
	return e.cause
}

// Creates a new error msg without error code
func New(msg string) error {
	return &Error{
		msg: msg,
	}
}

// Wraps the error msg with recognized error codes
func Wrap(code ErrCode, msg string) error {
	return &Error{
		code: code,
		msg:  msg,
	}
}

// Wraps the error msg with recognized error codes
// using specified message format
func Wrapf(code ErrCode, format string, v ...any) error {
	return &Error{
		code: code,
		msg:  fmt.Sprintf(format, v...),
	}
}

// WrapErr tags err with a recognized error code while preserving err as the
// cause, so base.Is and base.As continue to see through to it.
//
// Prefer this over Wrap(code, err.Error()), which flattens the cause to a
// string and makes the classification one-shot: once the original error is
// gone, no caller can re-examine it if the classification turns out to be
// wrong.
func WrapErr(code ErrCode, err error) error {
	return &Error{
		code:  code,
		cause: err,
	}
}

// WrapErrf tags err with a recognized error code and additional context,
// preserving err as the cause. The formatted message and the cause are joined
// as "message: cause" by Error().
//
// Prefer this over Wrapf(code, "...: %s", ..., err) for the same reason
// WrapErr is preferred over Wrap.
func WrapErrf(code ErrCode, err error, format string, v ...any) error {
	return &Error{
		code:  code,
		msg:   fmt.Sprintf(format, v...),
		cause: err,
	}
}

// IsNotFound returns true if err
// item isn't found in the space
func IsNotFound(err error) bool {
	return GetErrCode(err) == NotFound
}

// IsAlreadyExists returns true if err
// item already exists in the space
func IsAlreadyExists(err error) bool {
	return GetErrCode(err) == AlreadyExists
}

// IsInvalidArgument returns true if err
// item is invalid argument
func IsInvalidArgument(err error) bool {
	return GetErrCode(err) == InvalidArgument
}

// IsUnauthorized returns true if err
// is due to Unauthorized request
func IsUnauthorized(err error) bool {
	return GetErrCode(err) == Unauthorized
}

// IsForbidden returns true if err
// is due to Forbidden action
func IsForbidden(err error) bool {
	return GetErrCode(err) == Forbidden
}

// IsUnavailable returns true if err is due to a transient or
// infrastructure-level failure (e.g. the datastore is unreachable or the
// request timed out). An Unavailable error does not imply the item is absent
// and is typically safe to retry.
func IsUnavailable(err error) bool {
	return GetErrCode(err) == Unavailable
}
