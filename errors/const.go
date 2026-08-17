// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
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

	// Unauthorized request error
	Unauthorized ErrCode = 4

	// Forbidden action error
	Forbidden ErrCode = 5

	// Unavailable indicates a transient or infrastructure-level failure,
	// e.g. the backing datastore is unreachable, a request timed out, or a
	// network error occurred. Unlike NotFound, an Unavailable error does NOT
	// mean the requested item is absent - the request could not be completed
	// and is typically safe to retry. Callers must not treat Unavailable as a
	// permanent, negative result.
	Unavailable ErrCode = 6
)
