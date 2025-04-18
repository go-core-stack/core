// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package errors

import (
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
