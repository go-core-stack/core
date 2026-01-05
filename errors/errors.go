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
func GetErrCode(err error) ErrCode {
	val, ok := err.(*Error)
	if ok {
		return ErrCode(val.code)
	}
	return Unknown
}

// base error structure
type Error struct {
	code ErrCode
	msg  string
}

// Error() prints out the error message string
func (e Error) Error() string {
	return e.msg
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
