// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package errors

// ErrCode is type for multiple reconizable errors.
type ErrCode int

// error codes
const (
	// if error is unknown
	Unknown ErrCode = 0

	// if the item not found in the space
	NotFound ErrCode = 1

	// if the item already present in the space
	AlreadyExists ErrCode = 2

	// if the argument is not valid
	InvalidArgument ErrCode = 3
)
