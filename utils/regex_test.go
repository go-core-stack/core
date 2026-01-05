// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package utils

import (
	"testing"
)

func TestIsValidEmail(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"test@example.com", true},
		{"invalid-email", false},
		{"user@localhost", false}, // usually considered invalid in public contexts
		{"name.lastname@domain.co.uk", true},
		{"user@domain", false}, // missing TLD
		{"user@sub.domain.com", true},
		{"user+alias@domain.com", true},
		{"", false},
		{"@domain.com", false},
		{"user@.com", false},
		{"user@domain.c", false}, // TLD too short
	}

	for _, test := range tests {
		result := IsValidEmail(test.input)
		if result != test.expected {
			t.Errorf("IsValidEmail(%q) = %v; want %v", test.input, result, test.expected)
		}
	}
}
