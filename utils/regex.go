// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package utils

import "regexp"

// IsValidEmail returns true if the provided string is a valid email address format.
// Usage:
//
//	valid := utils.IsValidEmail("user@example.com") // returns true
//	valid := utils.IsValidEmail("not-an-email")     // returns false
func IsValidEmail(email string) bool {
	// This is a reasonably strict regex for email validation
	var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
	return emailRegex.MatchString(email)
}
