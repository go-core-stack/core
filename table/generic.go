// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package table

import (
	"context"
	"log"
	"reflect"

	"github.com/Prabhjot-Sethi/core/db"
	"github.com/Prabhjot-Sethi/core/errors"
	"github.com/Prabhjot-Sethi/core/reconciler"
)

/*
Package table provides a generic Table abstraction for managing collections of entries
in a database, with built-in support for reconciliation, key management, and CRUD operations.

# Overview

The Table type is a generic structure that simplifies the implementation of strongly-typed
tables backed by a database. It provides:

- Type safety for keys and entries.
- Automatic key type registration with the underlying db.StoreCollection.
- CRUD operations (Insert, Update, Find, Delete, etc.).
- Integration with a reconciler for event-driven updates.
- Sanity checks to prevent common mistakes (e.g., pointer types for entries or keys).

# Usage

To use this library, define your key and entry types, then create a Table instance:

    type MyKey struct {
        ID string
    }
    type MyEntry struct {
        Name string
        Value int
    }

    var myTable table.Table[MyKey, MyEntry]

    // Initialize with a db.StoreCollection (e.g., from your database layer)
    err := myTable.Initialize(myCollection)
    if err != nil {
        // handle error
    }

    // Insert an entry
    entry := MyEntry{Name: "foo", Value: 42}
    key := MyKey{ID: "abc"}
    err = myTable.Insert(ctx, &key, &entry)

    // Find an entry
    found, err := myTable.Find(ctx, &key)

    // Update an entry
    entry.Value = 100
    err = myTable.Update(ctx, &key, &entry)

    // Delete an entry
    err = myTable.DeleteKey(ctx, &key)

# Notes

- The entry type E must NOT be a pointer type.
- The key type K must NOT be a pointer type.
- The Table must be initialized before use.
- All operations are context-aware for cancellation and timeouts.

*/

// Table is a generic table type providing common functions and types to specific
// structures each table is built using. It ensures sanity checks and provides
// common functionality for database-backed tables.
//
// K: Key type (must NOT be a pointer type, typically a struct or primitive)
// E: Entry type (must NOT be a pointer type)
type Table[K any, E any] struct {
	reconciler.ManagerImpl
	col db.StoreCollection
}

// Initialize sets up the Table with the provided db.StoreCollection.
// It performs sanity checks on the entry and key types and registers the key type with the collection.
// Must be called before any other operation.
//
// Returns an error if the table is already initialized, the entry or key type is a pointer,
// or if the collection setup fails.
func (t *Table[K, E]) Initialize(col db.StoreCollection) error {
	if t.col != nil {
		return errors.Wrapf(errors.AlreadyExists, "Table is already initialized")
	}

	var e E
	if reflect.TypeOf(e).Kind() == reflect.Pointer {
		return errors.Wrapf(errors.InvalidArgument, "Table entry type must not be a pointer")
	}

	var k K
	if reflect.TypeOf(k).Kind() == reflect.Pointer {
		return errors.Wrapf(errors.InvalidArgument, "Table key type must not be a pointer")
	}

	err := col.SetKeyType(reflect.PointerTo(reflect.TypeOf(k)))
	if err != nil {
		return err
	}

	// Register callback for collection changes
	err = col.Watch(context.Background(), nil, t.callback)
	if err != nil {
		return err
	}

	// Initialize the reconciler manager
	err = t.ManagerImpl.Initialize(context.Background(), t)
	if err != nil {
		return err
	}

	t.col = col
	return nil
}

// callback is invoked on collection changes and notifies the reconciler.
func (t *Table[K, E]) callback(op string, wKey any) {
	t.NotifyCallback(wKey)
}

// keyOnly is a helper struct for extracting keys from the collection.
type keyOnly[K any] struct {
	Key K `bson:"_id,omitempty"`
}

// ReconcilerGetAllKeys returns all keys in the table.
// Used by the reconciler to enumerate all managed entries.
func (t *Table[K, E]) ReconcilerGetAllKeys() []any {
	list := []keyOnly[K]{}
	keys := []any{}
	err := t.col.FindMany(context.Background(), nil, &list)
	if err != nil {
		log.Panicf("got error while fetching all keys %s", err)
	}
	for _, k := range list {
		keys = append(keys, &k.Key)
	}
	return []any(keys)
}

// Insert adds a new entry to the table with the given key.
// Returns an error if the table is not initialized or the insert fails.
func (t *Table[K, E]) Insert(ctx context.Context, key *K, entry *E) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.InsertOne(ctx, key, entry)
}

// Locate finds an entry by key, inserts it if it doesn't exist, or updates it if it does.
// Returns an error if the table is not initialized or the operation fails.
func (t *Table[K, E]) Locate(ctx context.Context, key *K, entry *E) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.UpdateOne(ctx, key, entry, true)
}

// Update modifies an existing entry with the given key.
// Returns an error if the table is not initialized or the update fails.
func (t *Table[K, E]) Update(ctx context.Context, key *K, entry *E) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.UpdateOne(ctx, key, entry, false)
}

// Find retrieves an entry by key.
// Returns the entry and error if not found or if the table is not initialized.
func (t *Table[K, E]) Find(ctx context.Context, key *K) (*E, error) {
	var data E
	if t.col == nil {
		return nil, errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	err := t.col.FindOne(ctx, key, &data)
	if err != nil {
		return nil, errors.Wrapf(errors.NotFound, "failed to find entry with key %v: %s", key, err)
	}
	return &data, err
}

// FindMany retrieves multiple entries matching the provided filter.
// Returns a slice of entries and error if none found or if the table is not initialized.
func (t *Table[K, E]) FindMany(ctx context.Context, filter any) ([]*E, error) {
	if t.col == nil {
		return nil, errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	var data []*E
	err := t.col.FindMany(ctx, filter, &data)
	if err != nil {
		return nil, errors.Wrapf(errors.NotFound, "failed to find any entry: %s", err)
	}

	return data, nil
}

// DeleteKey removes an entry by key from the table.
// Returns an error if the table is not initialized or the delete fails.
func (t *Table[K, E]) DeleteKey(ctx context.Context, key *K) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.DeleteOne(ctx, key)
}
