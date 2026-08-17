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

type outerErr struct{ cause error }

func (e *outerErr) Error() string { return "outer: " + e.cause.Error() }
func (e *outerErr) Unwrap() error { return e.cause }
