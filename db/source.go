// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

// Initial reference and motivation taken from
// https://gitlab.com/project-emco/core/emco-base/-/blob/main/src/orchestrator/pkg/infra/db

package db

import (
	"sync"
)

var (
	// Source identifier to be used by the library, while making connection
	// with mongo db, if not set it will fallback to
	// constant DefaultSourceIdentifier
	sourceIdentifier = ""

	// Once the Source Identifier is used it will not allow changing it again,
	// through the life of the program
	sourceIdentifierUsed = false

	// mutex to work with source Identifier ensuring thread safety
	sourceIdentifierLock sync.RWMutex
)

// Sets Source identifier to specified value, returns the status true if
// successfull, this will fail under only one scenario if the identifier
// is already in use by any of the connection
func SetSourceIdentifier(identifier string) bool {
	sourceIdentifierLock.Lock()
	defer sourceIdentifierLock.Unlock()
	if sourceIdentifierUsed {
		// source identifier already in use by some connection
		return false
	}
	sourceIdentifier = identifier
	return true
}

// for internal use only, it will also make source identifier in use
func getSourceIdentifier() string {
	sourceIdentifierLock.RLock()
	defer sourceIdentifierLock.RUnlock()
	sourceIdentifierUsed = true
	if sourceIdentifier != "" {
		return sourceIdentifier
	}
	return defaultSourceIdentifier
}
