// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

// Initial reference and motivation taken from
// https://gitlab.com/project-emco/core/emco-base/-/blob/main/src/orchestrator/pkg/infra/db

package db

const (
	defaultSourceIdentifier = "MongoClientCore"
)

const (
	// Operation string for mongo add/insert operation
	MongoAddOp = "insert"

	// Operation string for mongo update operation
	MongoUpdateOp = "update"

	// Operation string for mongo delete operation
	MongoDeleteOp = "delete"
)
