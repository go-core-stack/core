// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

// Initial reference and motivation taken from
// https://gitlab.com/project-emco/core/emco-base/-/blob/main/src/orchestrator/pkg/infra/db

package db

import (
	"context"
	"reflect"
)

// WatchCallbackfn responsible for
type WatchCallbackfn func(op string, key interface{})

// interface definition for a collection in store
type StoreCollection interface {
	// Set KeyType for the collection, this is not mandatory
	// while the key type will be used by the interface implementer
	// mainly for Watch Callback for providing decoded key, if not
	// set watch will be working with the default decoders of
	// interface implementer
	// only pointer key type is supported as of now
	// returns error if the key type is not a pointer
	SetKeyType(keyType reflect.Type) error

	// insert one entry to the collection for the given key and data
	InsertOne(ctx context.Context, key interface{}, data interface{}) error

	// update one entry in the collection for the given key and data
	// if upsert flag is set, it would insert an entry if it doesn't
	// exist while updating
	UpdateOne(ctx context.Context, key interface{}, data interface{}, upsert bool) error

	// Find one entry from the store collection for the given key, where the data
	// value is returned based on the object type passed to it
	FindOne(ctx context.Context, key interface{}, data interface{}) error

	// Find multiple entries from the store collection for the given filter, where the data
	// value is returned as a list based on the object type passed to it
	FindMany(ctx context.Context, filter interface{}, data interface{}) error

	// remove one entry from the collection matching the given key
	DeleteOne(ctx context.Context, key interface{}) error

	// Delete Many entries matching the delete criteria
	// returns number of entries deleted and if there is any error processing the request
	DeleteMany(ctx context.Context, filter interface{}) (int64, error)

	// watch allows getting notified whenever a change happens to a document
	// in the collection
	// allow provisiong for a filter to be passed on, where the callback
	// function to receive only conditional notifications of the events
	// listener is interested about
	Watch(ctx context.Context, filter interface{}, cb WatchCallbackfn) error
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
