// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

// Initial reference and motivation taken from
// https://gitlab.com/project-emco/core/emco-base/-/blob/main/src/orchestrator/pkg/infra/db

package db

import (
	"context"
)

// interface definition for a collection in store
type StoreCollection interface {
	// insert one entry to the collection for the given key and data
	InsertOne(ctx context.Context, key interface{}, data interface{}) error

	// remove one entry from the collection matching the given key
	DeleteOne(ctx context.Context, key interface{}) error
}

// interface definition for a store, responsible for holding group
// of collections
type Store interface {
	// Gets collection corresponding to the collection name
	GetCollection(col string) StoreCollection
}

// interface definition for Client corresponding to a store and
type StoreClient interface {
	// Get the Data Store interface given the client interface
	GetDataStore(dbName string) Store

	// Gets collection corresponding to the collection name inside
	// the requested database name
	GetCollection(dbName, col string) StoreCollection

	// Health Check, if the Store is connectable and healthy
	// returns the status of health of the server by means of
	// error if error is nil the health of the DB store can be
	// considered healthy
	HealthCheck(ctx context.Context) error
}
