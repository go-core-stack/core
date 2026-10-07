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
	// mean the requested item is absent, and callers must not treat it as a
	// permanent, negative result.
	//
	// The outcome is unknown: a write that failed with Unavailable may or
	// may not have been applied. Retrying is safe only when applying the
	// request twice is harmless; otherwise read the state again first.
	// Busy, by contrast, guarantees nothing was applied.
	Unavailable ErrCode = 6

	// ResourceExhausted indicates a capacity limit has been reached:
	// rate limiting (HTTP 429), quota exceeded, connection pool
	// exhaustion, or gRPC ResourceExhausted. Unlike Unavailable the
	// service itself is reachable — the caller has exceeded an
	// allowance. The remedy is to back off or reduce concurrency, not
	// to retry immediately.
	ResourceExhausted ErrCode = 7

	// FailedPrecondition indicates a well-formed request, addressed to
	// something that exists, that the target's current state rules out:
	// for example changing something that is being deleted, or claiming a
	// job another claimant already holds. Waiting does not help; the same
	// request fails until something deliberately changes the state, or it
	// never succeeds.
	//
	// Not InvalidArgument (that request is wrong whatever the state), not
	// NotFound (the target is there), not AlreadyExists (nothing is being
	// created), not Forbidden (the caller is allowed; the state is what
	// stands in the way), and not Busy (a busy obstacle clears by itself).
	FailedPrecondition ErrCode = 8

	// Conflict indicates the state still allows this kind of change, but
	// the request was built on a view of the state that is no longer
	// current: for example a write based on a version that has since moved
	// on. Read the state again and build a new request from it; sending the
	// same request again fails the same way.
	//
	// Not FailedPrecondition (the state allows the change; only the
	// caller's view is stale), and not AlreadyExists (nothing collided on
	// insert).
	Conflict ErrCode = 9

	// Busy indicates the request is fine but the work could not start: a
	// lock it had to wait for was held by another holder for as long as the
	// caller could wait. The obstacle clears by itself, and the unchanged
	// request can then succeed.
	//
	// Busy is returned only when it is certain nothing was applied,
	// including by the wait itself, so sending the same request again is
	// safe, even for an increment. Where that is not certain (for example
	// the attempt to take the lock itself timed out), the error is
	// Unavailable. Not Unavailable itself: an Unavailable outcome is
	// unknown, while Busy's is known, as nothing was applied. Not
	// ResourceExhausted either: no allowance, quota or pool has run out; one
	// holder is blocking one piece of work.
	Busy ErrCode = 10
)
