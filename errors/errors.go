// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package errors

// get the error code if the error is
// associated to recognizable error types
func getErrCode(err error) ErrCode {
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

// IsNotFound returns true if err
// item isn't found in the space
func IsNotFound(err error) bool {
	return getErrCode(err) == NotFound
}

// IsAlreadyExists returns true if err
// item already exists in the space
func IsAlreadyExists(err error) bool {
	return getErrCode(err) == AlreadyExists
}

// IsInvalidArgument returns true if err
// item is invalid argument
func IsInvalidArgument(err error) bool {
	return getErrCode(err) == InvalidArgument
}
