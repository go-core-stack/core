// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

// Initial reference and motivation taken from
// https://gitlab.com/project-emco/core/emco-base/-/blob/main/src/orchestrator/pkg/infra/db

package db

import (
	"context"
)

type Store interface {
}

type StoreClient interface {
	// Get the Data Store interface given the client interface
	GetDataStore(dbName string) Store

	// Health Check, if the Store is connectable and healthy
	// returns the status of health of the server by means of
	// error if error is nil the health of the DB store can be
	// considered healthy
	HealthCheck(ctx context.Context) error
}
