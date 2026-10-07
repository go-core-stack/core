// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package errors

import (
	"context"
	base "errors"
	"fmt"
	"testing"
)

func Test_ErrorValidations(t *testing.T) {
	err := fmt.Errorf("%s", "test error from fmt")
	if GetErrCode(err) != Unknown {
		t.Errorf("expected error type unknown, got %v", GetErrCode(err))
	}

	err = New("test error from errors pkg")
	if GetErrCode(err) != Unknown {
		t.Errorf("expected error type unknown, got %v", GetErrCode(err))
	}

	err = Wrap(AlreadyExists, "test wrap error from errors pkg")
	if !IsAlreadyExists(err) {
		t.Errorf("expected error type Already exists")
	}

	err = Wrapf(NotFound, "%s", "test wrapf error from errors pkg")
	if !IsNotFound(err) {
		t.Errorf("expected error type Not Found")
	}
}

// Test_UnwrapPreservesCause pins the behaviour issue #123 was filed for: an
// error tagged with a code must remain inspectable with base.Is/base.As, so a
// classification made at one layer can be re-examined at another.
func Test_UnwrapPreservesCause(t *testing.T) {
	cause := context.DeadlineExceeded

	err := WrapErr(Unavailable, cause)
	if !IsUnavailable(err) {
		t.Errorf("expected Unavailable, got %v", GetErrCode(err))
	}
	if !base.Is(err, context.DeadlineExceeded) {
		t.Errorf("WrapErr must preserve the cause for base.Is")
	}

	err = WrapErrf(Unknown, cause, "failed to find entry with key %v", 42)
	if !base.Is(err, context.DeadlineExceeded) {
		t.Errorf("WrapErrf must preserve the cause for base.Is")
	}
	if want := "failed to find entry with key 42: context deadline exceeded"; err.Error() != want {
		t.Errorf("expected %q, got %q", want, err.Error())
	}

	// Wrap/Wrapf carry no cause and must keep unwrapping to nil.
	if base.Unwrap(Wrap(NotFound, "plain")) != nil {
		t.Errorf("Wrap must not invent a cause")
	}
	if got := Wrap(NotFound, "plain").Error(); got != "plain" {
		t.Errorf("Wrap message changed: got %q", got)
	}
}

// Test_GetErrCodeSeesThroughWrapping covers the second half of #123: the code
// lookup must walk the chain rather than inspecting only the outermost error.
func Test_GetErrCodeSeesThroughWrapping(t *testing.T) {
	inner := Wrap(NotFound, "row absent")

	wrapped := fmt.Errorf("loading integration: %w", inner)
	if !IsNotFound(wrapped) {
		t.Errorf("expected NotFound through fmt.Errorf wrapping, got %v", GetErrCode(wrapped))
	}

	// A custom error type that wraps must work too.
	if !IsNotFound(&outerErr{cause: wrapped}) {
		t.Errorf("expected NotFound through a custom wrapper")
	}

	// The outermost classification wins when codes are nested, so a later
	// re-classification overrides an earlier one.
	reclassified := WrapErr(Unavailable, inner)
	if !IsUnavailable(reclassified) {
		t.Errorf("expected outermost code Unavailable, got %v", GetErrCode(reclassified))
	}
	if !IsNotFound(base.Unwrap(reclassified)) {
		t.Errorf("expected inner NotFound to remain reachable")
	}

	// An unrelated error still reports Unknown.
	if GetErrCode(fmt.Errorf("no code here")) != Unknown {
		t.Errorf("expected Unknown for an untagged error")
	}
	if GetErrCode(nil) != Unknown {
		t.Errorf("expected Unknown for a nil error")
	}
}

func Test_ResourceExhausted(t *testing.T) {
	err := Wrap(ResourceExhausted, "rate limited")
	if !IsResourceExhausted(err) {
		t.Errorf("expected ResourceExhausted, got %v", GetErrCode(err))
	}

	err = WrapErr(ResourceExhausted, fmt.Errorf("429 too many requests"))
	if !IsResourceExhausted(err) {
		t.Errorf("expected ResourceExhausted through WrapErr")
	}
	if !base.Is(err, err.(*Error).cause) {
		t.Errorf("WrapErr must preserve the cause")
	}

	wrapped := fmt.Errorf("calling embedding API: %w", Wrap(ResourceExhausted, "quota exceeded"))
	if !IsResourceExhausted(wrapped) {
		t.Errorf("expected ResourceExhausted through fmt.Errorf wrapping, got %v", GetErrCode(wrapped))
	}

	if IsResourceExhausted(Wrap(Unavailable, "connection refused")) {
		t.Errorf("Unavailable must not be classified as ResourceExhausted")
	}
}

// The state-dependent codes keep their values, are found through every kind
// of wrapping, and are never mistaken for one another or for their
// neighbours.
func Test_StateDependentCodes(t *testing.T) {
	cases := []struct {
		code  ErrCode
		value int
		is    func(error) bool
	}{
		{FailedPrecondition, 8, IsFailedPrecondition},
		{Conflict, 9, IsConflict},
		{Busy, 10, IsBusy},
	}
	all := []ErrCode{Unknown, NotFound, AlreadyExists, InvalidArgument, Unauthorized, Forbidden,
		Unavailable, ResourceExhausted, FailedPrecondition, Conflict, Busy}
	for _, c := range cases {
		if int(c.code) != c.value {
			t.Errorf("code %d moved to %d; codes are never renumbered", c.value, c.code)
		}

		if !c.is(Wrap(c.code, "refused")) {
			t.Errorf("code %d not found through Wrap", c.code)
		}
		cause := fmt.Errorf("underlying")
		err := WrapErr(c.code, cause)
		if !c.is(err) || !base.Is(err, cause) {
			t.Errorf("code %d: WrapErr must keep both the code and the cause", c.code)
		}
		wrapped := fmt.Errorf("outer context: %w", Wrap(c.code, "refused"))
		if !c.is(wrapped) {
			t.Errorf("code %d not found through fmt.Errorf wrapping, got %v", c.code, GetErrCode(wrapped))
		}
		var e *Error
		if !base.As(wrapped, &e) || e.code != c.code {
			t.Errorf("code %d not found through errors.As", c.code)
		}

		for _, other := range all {
			if other != c.code && c.is(Wrap(other, "other")) {
				t.Errorf("code %d is mistaken for code %d", other, c.code)
			}
		}
	}
}

type outerErr struct{ cause error }

func (e *outerErr) Error() string { return "outer: " + e.cause.Error() }
func (e *outerErr) Unwrap() error { return e.cause }
